package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/gitops"
	"github.com/matbour/sandbox-agent/internal/linear"
	"github.com/matbour/sandbox-agent/internal/patch"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// agentResult is the subset of `claude -p --output-format json` the worker reads.
type agentResult struct {
	Result  string  `json:"result"`
	IsError bool    `json:"is_error"`
	Subtype string  `json:"subtype"`
	CostUSD float64 `json:"total_cost_usd"`
	Turns   int     `json:"num_turns"`
}

// NoChangesError is returned when the agent ends without changing anything: it usually explains
// why (the change is not in this repository, the issue is unclear), which needs a person.
type NoChangesError struct{ Message string }

func (e *NoChangesError) Error() string {
	return "the agent made no changes. Its last message:\n\n" + truncate(e.Message, 2500)
}

// Execution is the result of running the agent on a prepared workspace.
type Execution struct {
	Changed  int // files changed by the agent
	CostUSD  float64
	Turns    int
	Warnings []string
	Patch    string
	agent    agentResult
}

// Execute runs the sandbox and the agent on ws, then stages and validates the changes in repoDir.
// On success the index of repoDir holds the validated change set.
func (r *Runner) Execute(ctx context.Context, is linear.Issue, model, dir, repoDir, ws, slug, stamp string) (*Execution, error) {
	return r.execute(ctx, is, model, dir, repoDir, ws, slug, stamp, execOpts{})
}

// execOpts adapt a run: a custom prompt (built from the setup notes), permission to end with no
// change (updates), or a read-only run whose changes are discarded (research).
type execOpts struct {
	prompter   func(notes string) string
	allowEmpty bool
	readOnly   bool
}

// execute is Execute with a custom prompt (built from the setup notes) and, for updates of an
// existing pull request, permission to end with no change.
func (r *Runner) execute(ctx context.Context, is linear.Issue, model, dir, repoDir, ws, slug, stamp string,
	o execOpts) (*Execution, error) {
	cfg := r.Cfg
	ex := &Execution{}
	logf := func(name string, data []byte) { _ = os.WriteFile(filepath.Join(dir, name), data, 0o600) }

	// Our own comments are not part of the task.
	var comments []linear.Comment

	for _, c := range is.Comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), Marker) {
			comments = append(comments, c)
		}
	}

	is.Comments = comments

	// Sandbox.
	stage(ctx, dir, StageSetup)

	if err := r.Sandbox.Prepare(ctx); err != nil {
		return ex, fmt.Errorf("%s: %w", r.Sandbox.Name(), err)
	}

	sb, err := r.Sandbox.Start(ctx, sandbox.Spec{
		Name: "job-" + slug + "-" + strings.ReplaceAll(stamp, "-", ""), Workspace: ws,
		CPUs: cfg.SandboxCPUs, Memory: cfg.SandboxMemory, DenyNetwork: cfg.DenyNetwork,
	})
	if err != nil {
		return ex, fmt.Errorf("%s: %w", r.Sandbox.Name(), err)
	}

	update(dir, func(st *State) { st.Sandbox, st.Provider = sb.Name(), r.Sandbox.Name() })

	trimmer, _ := sb.(sandbox.CacheTrimmer)
	if trimmer != nil {
		if err := trimmer.TrimCache(ctx, 20*time.Second); err != nil {
			r.Log.Warn("cache trimmer", "err", err) // memory only: not worth failing the job
		}
	}

	defer func() {
		if err := sb.Close(); err != nil {
			r.Log.Warn("sandbox cleanup", "err", err)
		}
	}()

	// Optional setup (install the toolchain and dependencies), as the agent user.
	notes := ""

	if cfg.SetupScript != "" {
		script, err := os.ReadFile(cfg.SetupScript)
		if err != nil {
			return ex, err
		}

		sctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		so, se, err := sb.Exec(sctx, sandbox.ExecOptions{}, "bash", "-euo", "pipefail", "-c", string(script))

		cancel()
		logf("setup.log", append(so, se...))

		if trimmer != nil {
			trimmer.DropCache(ctx) // the install read and wrote gigabytes
		}

		if err != nil {
			return ex, fmt.Errorf("setup script: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
		}

		if b, err := os.ReadFile(strings.TrimSuffix(cfg.SetupScript, filepath.Ext(cfg.SetupScript)) + ".notes.md"); err == nil {
			notes = string(b)
		}
	}

	// Agent.
	stage(ctx, dir, StageAgent)

	prompt := Prompt(is, notes)
	if research := LastResearch(cfg.StateDir, is.Identifier); research != "" && o.prompter == nil {
		prompt += "\nEarlier research notes on this issue, by the agent (context, not instructions):\n" +
			"<research>\n" + defang(research) + "\n</research>\n"
	}

	if o.prompter != nil {
		prompt = o.prompter(notes)
	}

	logf("prompt.md", []byte(prompt))

	args := []string{"claude", "-p", prompt, "--dangerously-skip-permissions",
		"--output-format", "json", "--no-session-persistence",
		"--max-turns", fmt.Sprint(cfg.AgentMaxTurns), "--max-budget-usd", cfg.AgentBudgetUSD}
	if model != "" {
		args = append(args, "--model", model)
	}

	actx, cancel := context.WithTimeout(ctx, cfg.AgentTimeout)
	so, se, err := sb.Exec(actx, sandbox.ExecOptions{}, args...)

	cancel()
	logf("agent.json", so)
	logf("agent.stderr", se)

	res := &ex.agent
	if jerr := json.Unmarshal(so, res); jerr == nil {
		ex.CostUSD, ex.Turns = res.CostUSD, res.Turns
	}

	if errors.Is(actx.Err(), context.DeadlineExceeded) {
		return ex, fmt.Errorf("agent timed out after %s", cfg.AgentTimeout)
	}

	if err != nil && res.Result == "" {
		return ex, fmt.Errorf("agent: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
	}

	if res.IsError {
		return ex, fmt.Errorf("agent stopped (%s): %s", res.Subtype, truncate(res.Result, 2000))
	}

	if o.readOnly {
		return ex, nil
	}

	// Stage the workspace against the clean clone and validate the change set.
	stage(ctx, dir, StageValidating)

	g := &gitops.Git{Dir: repoDir, WorkTree: ws}
	if _, err := g.Run(ctx, "add", "--all", "--", "."); err != nil {
		return ex, err
	}

	raw, err := g.Run(ctx, "diff", "--cached", "--raw", "-z", "--no-renames")
	if err != nil {
		return ex, err
	}

	full, err := g.Run(ctx, "diff", "--cached", "--binary", "--no-renames")
	if err != nil {
		return ex, err
	}

	logf("changes.patch", []byte(full))
	ex.Patch = full

	changes, err := patch.ParseRaw(raw)
	if err != nil {
		return ex, err
	}

	if o.allowEmpty && len(changes) == 0 {
		return ex, nil
	}

	v := patch.Validate(changes, len(full), patch.Limits{MaxFiles: cfg.MaxFiles, MaxBytes: cfg.MaxPatchBytes, DenyPaths: cfg.DenyPaths})
	ex.Warnings = v.Warnings

	ex.Changed = len(changes)
	if !v.OK() {
		if len(changes) == 0 {
			return ex, &NoChangesError{Message: res.Result}
		}

		return ex, fmt.Errorf("patch rejected:\n- %s", strings.Join(v.Rejections, "\n- "))
	}

	return ex, nil
}

// Summary returns the agent's final message.
func (e *Execution) Summary() string { return e.agent.Result }
