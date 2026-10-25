package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/task"
)

// agentRun is what both ways of running the agent need.
type agentRun struct {
	name   string // the sandbox's, and the run's ID
	prompt string
	model  string
	env    []string
	dir    string // the job directory
	ws     string // the workspace on the host
	logf   func(name string, data []byte)
}

// Executor runs one run through the worker: in-process on a provider (runner.Local), or on a
// runner that leased it (D42).
type Executor interface {
	Name() string
	Execute(ctx context.Context, spec runner.Spec, workspace string, events func(task.Event)) (runner.Result, error)
}

// setupTimeout bounds the setup script.
const setupTimeout = 20 * time.Minute

// runDirect runs the setup script, then claude, one command at a time from the host. It fills
// ex.agent with Claude Code's JSON result.
func (r *Runner) runDirect(ctx context.Context, a agentRun, ex *Execution) error {
	cfg := r.Cfg

	if err := r.Sandbox.Prepare(ctx); err != nil {
		return fmt.Errorf("%s: %w", r.Sandbox.Name(), err)
	}

	sb, err := r.Sandbox.Start(ctx, sandbox.Spec{Name: a.name, Workspace: a.ws,
		CPUs: cfg.SandboxCPUs, Memory: cfg.SandboxMemory, DenyNetwork: cfg.DenyNetwork})
	if err != nil {
		return fmt.Errorf("%s: %w", r.Sandbox.Name(), err)
	}

	update(a.dir, func(st *State) { st.Sandbox, st.Provider = sb.Name(), r.Sandbox.Name() })

	defer func() {
		if err := sb.Close(); err != nil {
			r.Log.Warn("sandbox cleanup", "err", err)
		}
	}()

	trimmer, _ := sb.(sandbox.CacheTrimmer)
	if trimmer != nil {
		if err := trimmer.TrimCache(ctx, 20*time.Second); err != nil {
			r.Log.Warn("cache trimmer", "err", err) // memory only: not worth failing the job
		}
	}

	if cfg.SetupScript != "" {
		script, err := os.ReadFile(cfg.SetupScript)
		if err != nil {
			return err
		}

		sctx, cancel := context.WithTimeout(ctx, setupTimeout)
		so, se, err := sb.Exec(sctx, sandbox.ExecOptions{}, "bash", "-euo", "pipefail", "-c", string(script))

		cancel()
		a.logf("setup.log", append(so, se...))

		if trimmer != nil {
			trimmer.DropCache(ctx) // the install read and wrote gigabytes
		}

		if err != nil {
			return fmt.Errorf("setup script: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
		}
	}

	stage(ctx, a.dir, StageAgent)
	a.logf("prompt.md", []byte(a.prompt))

	args := []string{"claude", "-p", a.prompt, "--dangerously-skip-permissions",
		"--output-format", "json", "--no-session-persistence",
		"--max-turns", fmt.Sprint(cfg.AgentMaxTurns), "--max-budget-usd", cfg.AgentBudgetUSD}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}

	actx, cancel := context.WithTimeout(ctx, cfg.AgentTimeout)
	so, se, err := sb.Exec(actx, sandbox.ExecOptions{Env: a.env}, args...)

	cancel()

	// The token must not come back in anything that is logged or published (D43).
	if leaks(cfg.ClaudeToken, so, se) {
		return errSecretLeak
	}

	a.logf("agent.json", so)
	a.logf("agent.stderr", se)

	res := &ex.agent
	if jerr := json.Unmarshal(so, res); jerr == nil {
		ex.CostUSD, ex.Turns = res.CostUSD, res.Turns
	}

	if errors.Is(actx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("agent timed out after %s", cfg.AgentTimeout)
	}

	if err != nil && res.Result == "" {
		return fmt.Errorf("agent: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
	}

	if res.IsError {
		return fmt.Errorf("agent stopped (%s): %s", res.Subtype, truncate(res.Result, 2000))
	}

	// A provider without a shared directory brings the workspace back first.
	if s, ok := sb.(sandbox.Syncer); ok {
		return s.SyncWorkspace(ctx, excludePatterns())
	}

	return nil
}

// runExecutor sends the run to the executor. The worker's events are hostile input, already
// read by task.Read on the executor's side; here they go to events.jsonl in the job directory,
// each checked for the subscription token first, and thoughts to the reporter, throttled. The
// terminal event becomes ex.agent, so what follows does not change.
func (r *Runner) runExecutor(ctx context.Context, a agentRun, ex *Execution) error {
	cfg := r.Cfg

	script := ""
	if cfg.SetupScript != "" {
		b, err := os.ReadFile(cfg.SetupScript)
		if err != nil {
			return err
		}

		script = string(b)
	}

	budget, _ := strconv.ParseFloat(cfg.AgentBudgetUSD, 64)

	spec := runner.Spec{
		ID: a.name, CPUs: cfg.SandboxCPUs, Memory: cfg.SandboxMemory, Exclude: excludePatterns(), SecretEnv: a.env,
		Task: task.Task{Prompt: a.prompt, Model: a.model, Agent: cfg.AgentBackend, Setup: script,
			Limits: task.Limits{MaxTurns: cfg.AgentMaxTurns, BudgetUSD: budget,
				AgentTimeoutSec: int(cfg.AgentTimeout.Seconds()), SetupTimeoutSec: int(setupTimeout.Seconds())}},
	}

	stage(ctx, a.dir, StageAgent)
	a.logf("prompt.md", []byte(a.prompt))
	update(a.dir, func(st *State) { st.Sandbox, st.Provider = a.name, r.Exec.Name() })

	log, err := os.OpenFile(filepath.Join(a.dir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	ev := &eventSink{ctx: ctx, log: json.NewEncoder(log), secret: cfg.ClaudeToken}

	res, err := r.Exec.Execute(ctx, spec, a.ws, ev.event)

	term := res.Terminal
	if ev.leaked || leaks(cfg.ClaudeToken, res.Log, []byte(term.Title), []byte(term.Body), []byte(term.Question), []byte(term.Reason)) {
		return errSecretLeak
	}

	if err != nil {
		return err
	}

	ev.event(term)
	a.logf("worker.log", res.Log)

	ex.CostUSD, ex.Turns = term.CostUSD, term.Turns
	ex.agent.CostUSD, ex.agent.Turns = term.CostUSD, term.Turns

	if term.Kind == task.KindFail {
		return fmt.Errorf("agent: %s", truncate(term.Reason, 2000))
	}

	ex.agent.Result = finalMessage(term)

	return nil
}

// finalMessage renders a terminal event back into the final-message format the rest of the job
// reads (ParseDecision, ParseSummary), so both ways of running the agent end the same.
func finalMessage(e task.Event) string {
	var b bytes.Buffer

	if e.Kind == task.KindDone {
		b.WriteString("STATUS: ready\n")
	} else {
		fmt.Fprintf(&b, "STATUS: question\nQUESTION: %s\n", e.Question)
	}

	if e.Title != "" {
		fmt.Fprintf(&b, "TITLE: %s\n\n", e.Title)
	}

	b.WriteString(e.Body)

	return b.String()
}

// eventSink logs the worker's events and forwards thoughts to the reporter, at most one every
// thoughtEvery, so a chatty agent does not flood Linear.
type eventSink struct {
	ctx    context.Context
	log    *json.Encoder
	secret string

	mu     sync.Mutex
	last   time.Time
	leaked bool
}

const thoughtEvery = 30 * time.Second

func (s *eventSink) event(e task.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, _ := json.Marshal(e)
	if leaks(s.secret, b) {
		s.leaked = true

		return
	}

	_ = s.log.Encode(e)

	if e.Kind != task.KindThought || time.Since(s.last) < thoughtEvery {
		return
	}

	if rep, ok := s.ctx.Value(reporterKey{}).(Reporter); ok {
		s.last = time.Now()
		rep.Progress(s.ctx, "Agent: "+truncate(e.Text, 300))
	}
}
