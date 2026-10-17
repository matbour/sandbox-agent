package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/matbour/sandbox-agent/internal/task"
)

// setup runs the task's setup script with bash, in the workspace, with the setup time limit. Its
// output goes to the run's log, not to the events: it is long and says nothing to a person.
func setup(ctx context.Context, t task.Task, out *emitter) (task.Event, bool) {
	ctx, cancel := context.WithTimeout(ctx, t.Limits.SetupTimeout())
	defer cancel()

	start := time.Now()

	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", t.Setup)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

	err := cmd.Run()
	took := time.Since(start).Round(time.Second)

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("setup script timed out after %s", t.Limits.SetupTimeout())}, false
	case err != nil:
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("setup script failed after %s: %v (see the run's log)", took, err)}, false
	}

	out.emit(task.Event{Kind: task.KindAction, Tool: "setup", Input: "setup script", Result: "done in " + took.String()})

	return task.Event{}, true
}
