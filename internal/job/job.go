// Package job runs one Linear issue through the sandbox and opens a draft pull request.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/gitops"
	"github.com/matbour/sandbox-agent/internal/linear"
	"github.com/matbour/sandbox-agent/internal/patch"
	"github.com/matbour/sandbox-agent/internal/sbx"
)

// Marker starts every comment the worker posts, so its own comments are not fed back to the agent.
const Marker = "🤖 sandbox-agent"

// Runner holds what every job needs.
type Runner struct {
	Cfg    *config.Config
	Linear *linear.Client
	App    *github.App
	Log    *slog.Logger
}

// Outcome is what a job produced.
type Outcome struct {
	PR       *github.PullRequest
	CostUSD  float64
	Turns    int
	Warnings []string
	Dir      string
}

// agentResult is the subset of `claude -p --output-format json` the worker reads.
type agentResult struct {
	Result  string  `json:"result"`
	IsError bool    `json:"is_error"`
	Subtype string  `json:"subtype"`
	CostUSD float64 `json:"total_cost_usd"`
	Turns   int     `json:"num_turns"`
}

// Excluded from the change set whatever the repository's .gitignore says.
const exclude = `node_modules/
.bun/
.cache/
.turbo/
.next/
dist/
coverage/
__pycache__/
.pytest_cache/
*.pyc
.DS_Store
`

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// Handle claims the issue (removes the trigger label), runs it, and reports on the issue.
func (r *Runner) Handle(ctx context.Context, is linear.Issue) {
	log := r.Log.With("issue", is.Identifier)
	var names, keep []string
	for _, l := range is.Labels.Nodes {
		names = append(names, l.Name)
		if !r.Cfg.IsTrigger(l.Name) {
			keep = append(keep, l.ID)
		}
	}
	model, _ := r.Cfg.ModelFor(names)
	if err := r.Linear.SetLabels(ctx, is.ID, keep); err != nil {
		log.Error("cannot remove trigger label, skipping", "err", err)
		return
	}
	r.comment(ctx, is, fmt.Sprintf("%s: picked up, working on it with `%s`.", Marker, model))
	log.Info("job started", "model", model)
	start := time.Now()
	out, err := r.Run(ctx, is, model)
	took := time.Since(start).Round(time.Second)
	if out != nil {
		// The clone and the workspace (with node_modules) take gigabytes: keep only the logs,
		// the prompt and the patch.
		_ = os.RemoveAll(filepath.Join(out.Dir, "repo"))
		_ = os.RemoveAll(filepath.Join(out.Dir, "workspace"))
		update(out.Dir, func(st *State) {
			st.CostUSD, st.Turns = out.CostUSD, out.Turns
			if err != nil {
				st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
			} else {
				st.Stage, st.PR = StageDone, out.PR.HTMLURL
			}
		})
	}
	if err != nil {
		log.Error("job failed", "err", err, "took", took)
		msg := fmt.Sprintf("%s: failed after %s.\n\n```\n%s\n```", Marker, took, truncate(err.Error(), 3000))
		if out != nil && out.CostUSD > 0 {
			msg += fmt.Sprintf("\n\nAgent cost: $%.2f, %d turns.", out.CostUSD, out.Turns)
		}
		msg += fmt.Sprintf("\n\nAdd the `%s` label again to retry", r.Cfg.TriggerLabel)
		if retry, _ := r.Cfg.ModelFor([]string{r.Cfg.RetryLabel}); r.Cfg.RetryLabel != "" && retry != model {
			msg += fmt.Sprintf(", or `%s` to retry with `%s`", r.Cfg.RetryLabel, retry)
		}
		msg += ". Model labels: " + modelLabelList(r.Cfg) + "."
		r.comment(ctx, is, msg)
		return
	}
	log.Info("job done", "pr", out.PR.HTMLURL, "cost", out.CostUSD, "took", took)
	msg := fmt.Sprintf("%s: draft pull request ready for review: %s\n\nTook %s, `%s`, agent cost $%.2f, %d turns.",
		Marker, out.PR.HTMLURL, took, model, out.CostUSD, out.Turns)
	r.comment(ctx, is, msg)
}

func (r *Runner) comment(ctx context.Context, is linear.Issue, body string) {
	if err := r.Linear.Comment(ctx, is.ID, body); err != nil {
		r.Log.Warn("cannot comment on issue", "issue", is.Identifier, "err", err)
	}
}

// Run does the work: clone, sandbox, agent, validate, push, draft PR.
func (r *Runner) Run(ctx context.Context, is linear.Issue, model string) (*Outcome, error) {
	cfg := r.Cfg
	stamp := time.Now().UTC().Format("20060102-150405")
	slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(is.Identifier), "-"), "-")
	dir := filepath.Join(cfg.StateDir, "jobs", slug+"-"+stamp)
	repoDir, ws := filepath.Join(dir, "repo"), filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return nil, err
	}
	out := &Outcome{Dir: dir}
	update(dir, func(st *State) {
		st.Issue, st.Title, st.URL, st.Model = is.Identifier, is.Title, is.URL, model
		st.Stage, st.PID, st.Started = StageCloning, os.Getpid(), time.Now().UTC()
	})

	owner, name, _ := strings.Cut(cfg.Repo, "/")
	tok, err := r.App.InstallationToken(ctx, name)
	if err != nil {
		return out, fmt.Errorf("GitHub token: %w", err)
	}
	repo := &github.Repo{Token: tok, Owner: owner, Name: name, HTTP: r.App.HTTP}
	base := cfg.BaseBranch
	if base == "" {
		if base, err = repo.DefaultBranch(ctx); err != nil {
			return out, err
		}
	}

	// 1. Clean clone on the host, exported without .git into the workspace.
	if err := gitops.Clone(ctx, repo.CloneURL(), base, repoDir, tok.Value); err != nil {
		return out, err
	}
	if err := prepare(ctx, repoDir, ws); err != nil {
		return out, err
	}

	ex, err := r.Execute(ctx, is, model, dir, repoDir, ws, slug, stamp)
	if ex != nil {
		out.CostUSD, out.Turns, out.Warnings = ex.CostUSD, ex.Turns, ex.Warnings
	}
	if err != nil {
		return out, err
	}
	g := &gitops.Git{Dir: repoDir, WorkTree: ws}

	// Commit, push a new branch, open a draft PR.
	setStage(dir, StagePublishing)
	title, body := ParseSummary(ex.agent.Result, is.Title)
	msg := fmt.Sprintf("%s: %s\n\n%s\n", is.Identifier, title, is.URL)
	if cfg.AuthorEmail != "" {
		// Deploy platforms (Vercel) only build commits authored by a team member: commit as the
		// operator, and credit the agent in a trailer.
		g.Env = []string{
			"GIT_AUTHOR_NAME=" + cfg.AuthorName, "GIT_AUTHOR_EMAIL=" + cfg.AuthorEmail,
			"GIT_COMMITTER_NAME=" + cfg.AuthorName, "GIT_COMMITTER_EMAIL=" + cfg.AuthorEmail,
		}
		msg += "\nCo-authored-by: sandbox-agent <sandbox-agent@users.noreply.github.com>\n"
	}
	if _, err := g.Run(ctx, "commit", "--no-verify", "-q", "-m", msg); err != nil {
		return out, err
	}
	branch := "agent/" + slug + "-" + stamp
	pg := &gitops.Git{Dir: repoDir, Token: tok.Value}
	if _, err := pg.Run(ctx, "push", "--no-verify", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return out, err
	}
	prBody := prDescription(is, body, ex.Warnings, out)
	pr, err := repo.CreateDraftPR(ctx, branch, base, fmt.Sprintf("%s: %s", is.Identifier, title), prBody)
	if err != nil {
		return out, err
	}
	out.PR = pr
	_ = os.RemoveAll(repoDir)
	_ = os.RemoveAll(ws)
	return out, nil
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
	return r.execute(ctx, is, model, dir, repoDir, ws, slug, stamp, nil, false)
}

// execute is Execute with a custom prompt (built from the setup notes) and, for updates of an
// existing pull request, permission to end with no change.
func (r *Runner) execute(ctx context.Context, is linear.Issue, model, dir, repoDir, ws, slug, stamp string,
	prompter func(notes string) string, allowEmpty bool) (*Execution, error) {
	cfg := r.Cfg
	ex := &Execution{}
	logf := func(name string, data []byte) { _ = os.WriteFile(filepath.Join(dir, name), data, 0o600) }

	// Our own comments are not part of the task.
	var comments = is.Comments.Nodes[:0:0]
	for _, c := range is.Comments.Nodes {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), Marker) {
			comments = append(comments, c)
		}
	}
	is.Comments.Nodes = comments

	// Sandbox.
	setStage(dir, StageSetup)
	if err := sbx.EnsureDaemon(ctx, cfg.CPUs); err != nil {
		return ex, err
	}
	sb, err := sbx.Create(ctx, "job-"+slug+"-"+strings.ReplaceAll(stamp, "-", ""), ws, sbx.Options{
		CPUs: cfg.SandboxCPUs, Memory: cfg.SandboxMemory, DenyNetwork: cfg.DenyNetwork,
	})
	if err != nil {
		return ex, err
	}
	update(dir, func(st *State) { st.Sandbox = sb.Name })
	defer func() {
		if err := sb.RemoveHard(cfg.CPUs); err != nil {
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
		so, se, err := sb.Exec(sctx, "", "bash", "-euo", "pipefail", "-c", string(script))
		cancel()
		logf("setup.log", append(so, se...))
		if err != nil {
			return ex, fmt.Errorf("setup script: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
		}
		if b, err := os.ReadFile(strings.TrimSuffix(cfg.SetupScript, filepath.Ext(cfg.SetupScript)) + ".notes.md"); err == nil {
			notes = string(b)
		}
	}

	// Agent.
	setStage(dir, StageAgent)
	prompt := Prompt(is, notes)
	if prompter != nil {
		prompt = prompter(notes)
	}
	logf("prompt.md", []byte(prompt))
	args := []string{"claude", "-p", prompt, "--dangerously-skip-permissions",
		"--output-format", "json", "--no-session-persistence",
		"--max-turns", fmt.Sprint(cfg.AgentMaxTurns), "--max-budget-usd", cfg.AgentBudgetUSD}
	if model != "" {
		args = append(args, "--model", model)
	}
	actx, cancel := context.WithTimeout(ctx, cfg.AgentTimeout)
	so, se, err := sb.Exec(actx, "", args...)
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

	// Stage the workspace against the clean clone and validate the change set.
	setStage(dir, StageValidating)
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
	if allowEmpty && len(changes) == 0 {
		return ex, nil
	}
	v := patch.Validate(changes, len(full), patch.Limits{MaxFiles: cfg.MaxFiles, MaxBytes: cfg.MaxPatchBytes, DenyPaths: cfg.DenyPaths})
	ex.Warnings = v.Warnings
	ex.Changed = len(changes)
	if !v.OK() {
		if len(changes) == 0 {
			return ex, fmt.Errorf("the agent made no changes. Its last message:\n\n%s", truncate(res.Result, 2500))
		}
		return ex, fmt.Errorf("patch rejected:\n- %s", strings.Join(v.Rejections, "\n- "))
	}

	return ex, nil
}

func modelLabelList(cfg *config.Config) string {
	parts := []string{fmt.Sprintf("`%s` (%s)", cfg.TriggerLabel, cfg.AgentModel)}
	for _, m := range cfg.ModelLabels {
		parts = append(parts, fmt.Sprintf("`%s`", m.Label))
	}
	return strings.Join(parts, ", ")
}

func prDescription(is linear.Issue, body string, warnings []string, out *Outcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolves [%s](%s).\n\n", is.Identifier, is.URL)
	b.WriteString(body)
	if len(warnings) > 0 {
		b.WriteString("\n\n### Review carefully\n\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\n\n---\nOpened by sandbox-agent from a sandboxed Claude Code run (agent cost $%.2f, %d turns). "+
		"The description above is written by the agent: verify it against the diff.\n", out.CostUSD, out.Turns)
	return b.String()
}

// prepare exports the clean clone into the workspace and sets the host-side excludes.
func prepare(ctx context.Context, repoDir, ws string) error {
	if err := exportTree(ctx, repoDir, ws); err != nil {
		return fmt.Errorf("export working tree: %w", err)
	}
	return os.WriteFile(filepath.Join(repoDir, ".git/info/exclude"), []byte(exclude), 0o600)
}

// Try runs a task on a local Git repository, without Linear or GitHub, and leaves the result in
// the job directory (changes.patch, agent.json). Nothing is pushed.
func (r *Runner) Try(ctx context.Context, localRepo, task, model string) (*Execution, string, error) {
	stamp := time.Now().UTC().Format("20060102-150405")
	dir := filepath.Join(r.Cfg.StateDir, "jobs", "try-"+stamp)
	repoDir, ws := filepath.Join(dir, "repo"), filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return nil, dir, err
	}
	// Plain local clone: the source is the operator's own repository, not agent output.
	if b, err := exec.CommandContext(ctx, "git", "clone", "-q", "--no-hardlinks", localRepo, repoDir).CombinedOutput(); err != nil {
		return nil, dir, fmt.Errorf("git clone: %w: %s", err, b)
	}
	if err := prepare(ctx, repoDir, ws); err != nil {
		return nil, dir, err
	}
	var is linear.Issue
	is.Identifier, is.Title, is.Description = "TRY-1", firstLine(task), task
	update(dir, func(st *State) {
		st.Issue, st.Title, st.Model = "try", is.Title, model
		st.Stage, st.PID, st.Started = StageSetup, os.Getpid(), time.Now().UTC()
	})
	ex, err := r.Execute(ctx, is, model, dir, repoDir, ws, "try", stamp)
	update(dir, func(st *State) {
		if ex != nil {
			st.CostUSD, st.Turns = ex.CostUSD, ex.Turns
		}
		if err != nil {
			st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
		} else {
			st.Stage = StageDone
		}
	})
	return ex, dir, err
}

// Summary returns the agent's final message.
func (e *Execution) Summary() string { return e.agent.Result }

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return truncate(l, 80)
}

// exportTree copies HEAD's tracked files (no .git) from repoDir into dst.
func exportTree(ctx context.Context, repoDir, dst string) error {
	tarPath := filepath.Join(filepath.Dir(dst), "tree.tar")
	g := &gitops.Git{Dir: repoDir}
	if _, err := g.Run(ctx, "archive", "--format=tar", "-o", tarPath, "HEAD"); err != nil {
		return err
	}
	defer os.Remove(tarPath)
	cmd := exec.CommandContext(ctx, "tar", "-xf", tarPath, "-C", dst, "--no-same-owner")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, b)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
