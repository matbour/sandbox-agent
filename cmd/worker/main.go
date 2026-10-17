// Command worker runs inside the sandbox, in the workspace (D33, docs/decision/protocol.md). It
// reads one Task as JSON on stdin, runs the setup script and the agent CLI, and writes Events as
// JSON lines on stdout, ending with exactly one of ask, done or fail. Its stderr is the run's log.
//
// It knows nothing about Linear, GitHub or Git, and holds no secret of its own: the model
// credential, if any, is in its environment (D43). Its output is hostile input for the manager,
// which reads it with task.Read.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/matbour/sandbox-agent/internal/task"
)

// maxTask bounds the task read from stdin.
const maxTask = 8 << 20

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := &emitter{enc: json.NewEncoder(os.Stdout)}

	t, err := readTask(os.Stdin)
	if err != nil {
		out.emit(task.Event{Kind: task.KindFail, Reason: "worker: " + err.Error()})

		return
	}

	out.emit(run(ctx, t, out))
}

func readTask(r io.Reader) (task.Task, error) {
	var t task.Task

	b, err := io.ReadAll(io.LimitReader(r, maxTask+1))
	if err != nil {
		return t, err
	}

	if len(b) > maxTask {
		return t, fmt.Errorf("task over %d bytes", maxTask)
	}

	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("task: %w", err)
	}

	if t.Prompt == "" {
		return t, fmt.Errorf("task without a prompt")
	}

	return t, nil
}

// run does the setup, then the agent, and returns the terminal event.
func run(ctx context.Context, t task.Task, out *emitter) task.Event {
	if t.Setup != "" {
		if ev, ok := setup(ctx, t, out); !ok {
			return ev
		}
	}

	switch t.Agent {
	case task.AgentClaude, "":
		return claude(ctx, t, out)
	case task.AgentFake:
		return fake(ctx, t, out)
	default:
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("unknown agent %q", t.Agent)}
	}
}

// emitter writes events, one JSON object per line, from several goroutines.
type emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (e *emitter) emit(ev task.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	_ = e.enc.Encode(ev)
}

// logf writes to the run's log (stderr).
func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, "worker: "+format+"\n", args...) }
