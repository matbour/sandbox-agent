package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/task"
)

// agentRun is what both ways of running the agent need.
type agentRun struct {
	sb      sandbox.Sandbox
	trimmer sandbox.CacheTrimmer
	prompt  string
	model   string
	env     []string
	dir     string
	logf    func(name string, data []byte)
}

// setupTimeout bounds the setup script.
const setupTimeout = 20 * time.Minute

// runDirect runs the setup script, then claude, one command at a time from the host. It fills
// ex.agent with Claude Code's JSON result.
func (r *Runner) runDirect(ctx context.Context, a agentRun, ex *Execution) error {
	cfg := r.Cfg

	if cfg.SetupScript != "" {
		script, err := os.ReadFile(cfg.SetupScript)
		if err != nil {
			return err
		}

		sctx, cancel := context.WithTimeout(ctx, setupTimeout)
		so, se, err := a.sb.Exec(sctx, sandbox.ExecOptions{}, "bash", "-euo", "pipefail", "-c", string(script))

		cancel()
		a.logf("setup.log", append(so, se...))

		if a.trimmer != nil {
			a.trimmer.DropCache(ctx) // the install read and wrote gigabytes
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
	so, se, err := a.sb.Exec(actx, sandbox.ExecOptions{Env: a.env}, args...)

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

	return nil
}

// runWorker sends the task to the worker in the sandbox and reads its events as hostile input
// (task.Read). Events go to events.jsonl in the job directory, and thoughts to the reporter,
// throttled. The terminal event becomes ex.agent, so what follows does not change.
func (r *Runner) runWorker(ctx context.Context, a agentRun, ex *Execution) error {
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

	in, err := json.Marshal(task.Task{
		Prompt: a.prompt, Model: a.model, Agent: cfg.AgentBackend, Setup: script,
		Limits: task.Limits{MaxTurns: cfg.AgentMaxTurns, BudgetUSD: budget,
			AgentTimeoutSec: int(cfg.AgentTimeout.Seconds()), SetupTimeoutSec: int(setupTimeout.Seconds())},
	})
	if err != nil {
		return err
	}

	stage(ctx, a.dir, StageAgent)
	a.logf("prompt.md", []byte(a.prompt))

	log, err := os.OpenFile(filepath.Join(a.dir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	ev := &eventSink{r: r, ctx: ctx, log: json.NewEncoder(log), secret: cfg.ClaudeToken}

	pr, pw := io.Pipe()
	done := make(chan task.Event, 1)

	go func() {
		term := task.Read(pr, task.DefaultReadLimits, ev.event, func(d task.Dropped) {
			r.Log.Warn("worker event dropped", "line", d.Line, "reason", d.Reason)
		})

		_, _ = io.Copy(io.Discard, pr)

		done <- term
	}()

	// The worker enforces the setup and agent limits; this bounds the whole run if it does not.
	wctx, cancel := context.WithTimeout(ctx, setupTimeout+cfg.AgentTimeout+time.Minute)
	_, se, err := a.sb.Exec(wctx, sandbox.ExecOptions{Env: a.env, Stdin: bytes.NewReader(in), Stdout: pw}, a.sb.(sandbox.WorkerHost).WorkerPath())

	cancel()

	_ = pw.Close()
	term := <-done

	if ev.leaked || leaks(cfg.ClaudeToken, se, []byte(term.Title), []byte(term.Body), []byte(term.Question), []byte(term.Reason)) {
		return errSecretLeak
	}

	ev.event(term)
	a.logf("worker.log", se)

	ex.CostUSD, ex.Turns = term.CostUSD, term.Turns
	ex.agent.CostUSD, ex.agent.Turns = term.CostUSD, term.Turns

	switch {
	case errors.Is(wctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("the worker did not finish within %s", setupTimeout+cfg.AgentTimeout+time.Minute)
	case term.Kind == task.KindFail:
		return fmt.Errorf("agent: %s", truncate(term.Reason, 2000))
	case err != nil:
		// A worker that exits non-zero after done does not get the benefit of the doubt (D33).
		return fmt.Errorf("worker: %w: %s", err, truncate(lastLines(string(se), 20), 2000))
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
	r      *Runner
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
