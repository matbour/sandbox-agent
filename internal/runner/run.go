// Package runner executes runs: one worker in one fresh sandbox, from a workspace directory to a
// terminal event and the changed workspace (D42, docs/decision/runners.md). It is what runnerd
// does for each lease, and what agentd does in-process when no runner is configured.
//
// It knows nothing about Linear, GitHub or Git (boundary_test.go in internal/task).
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/task"
)

// Spec is one run, as the manager describes it.
type Spec struct {
	ID        string    `json:"id"`         // unique; also the sandbox's name
	Task      task.Task `json:"task"`       // for the worker
	CPUs      int       `json:"cpus"`       // vCPUs
	Memory    string    `json:"memory"`     // e.g. "2g"
	Exclude   []string  `json:"exclude"`    // left in the sandbox when the workspace comes back
	SecretEnv []string  `json:"secret_env"` // KEY=VALUE for the worker only (D43); never logged
}

// Result is how the run ended.
type Result struct {
	Terminal task.Event `json:"terminal"`
	Log      []byte     `json:"log"` // the worker's stderr: setup output, agent log
}

// Slack bounds the whole run beyond the worker's own limits, in case it does not enforce them.
const Slack = time.Minute

// Execute runs spec in a new sandbox from p on workspace, a host directory, which holds the
// changed workspace when Execute returns (except the excluded paths). events receives the
// worker's non-terminal events, already read as hostile input. The sandbox is always closed.
func Execute(ctx context.Context, p sandbox.Provider, spec Spec, workspace string, events func(task.Event)) (Result, error) {
	var res Result

	if err := p.Prepare(ctx); err != nil {
		return res, fmt.Errorf("%s: %w", p.Name(), err)
	}

	sb, err := p.Start(ctx, sandbox.Spec{Name: spec.ID, Workspace: workspace, CPUs: spec.CPUs, Memory: spec.Memory})
	if err != nil {
		return res, fmt.Errorf("%s: %w", p.Name(), err)
	}
	defer func() { _ = sb.Close() }()

	wh, ok := sb.(sandbox.WorkerHost)
	if !ok || wh.WorkerPath() == "" {
		return res, fmt.Errorf("%s: no worker in the sandbox", p.Name())
	}

	in, err := json.Marshal(spec.Task)
	if err != nil {
		return res, err
	}

	pr, pw := io.Pipe()
	done := make(chan task.Event, 1)

	go func() {
		term := task.Read(pr, task.DefaultReadLimits, events, nil)

		_, _ = io.Copy(io.Discard, pr)

		done <- term
	}()

	limit := spec.Task.Limits.SetupTimeout() + spec.Task.Limits.AgentTimeout() + Slack

	wctx, cancel := context.WithTimeout(ctx, limit)
	_, se, err := sb.Exec(wctx, sandbox.ExecOptions{Env: spec.SecretEnv, Stdin: bytes.NewReader(in), Stdout: pw}, wh.WorkerPath())

	cancel()

	_ = pw.Close()
	res.Terminal, res.Log = <-done, se

	switch {
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.Is(wctx.Err(), context.DeadlineExceeded):
		res.Terminal = task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("the worker did not finish within %s", limit)}
	case err != nil && res.Terminal.Kind != task.KindFail:
		// A worker that exits non-zero after done does not get the benefit of the doubt (D33).
		res.Terminal = task.Event{Kind: task.KindFail, Reason: "worker: " + err.Error(),
			CostUSD: res.Terminal.CostUSD, Turns: res.Terminal.Turns}
	}

	if s, ok := sb.(sandbox.Syncer); ok {
		if err := s.SyncWorkspace(ctx, spec.Exclude); err != nil {
			return res, err
		}
	}

	return res, nil
}
