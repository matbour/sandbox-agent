package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/task"
)

// fake is a scripted agent: no model, no cost. It makes whole runs testable, and many of them at
// once (scheduling, R4). Lines of the prompt starting with "FAKE:" steer it:
//
//	FAKE: sleep 5s            wait (cancellation, timeouts, concurrency)
//	FAKE: write <path> <text> write a file in the workspace
//	FAKE: ask <question>      end with a question
//	FAKE: fail <reason>       end with a failure
//	FAKE: thought <text>      emit a thought
//
// Without any directive it writes FAKE.md with the prompt's first line and ends with done.
func fake(ctx context.Context, t task.Task, out *emitter) task.Event {
	out.emit(task.Event{Kind: task.KindThought, Text: "fake agent: reading the task"})

	var directives []string

	for l := range strings.SplitSeq(t.Prompt, "\n") {
		if d, ok := strings.CutPrefix(strings.TrimSpace(l), "FAKE:"); ok {
			directives = append(directives, strings.TrimSpace(d))
		}
	}

	if len(directives) == 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(t.Prompt), "\n")
		directives = []string{"write FAKE.md " + first}
	}

	turns := 0

	for _, d := range directives {
		turns++

		verb, arg, _ := strings.Cut(d, " ")

		switch verb {
		case "sleep":
			dur, err := time.ParseDuration(arg)
			if err != nil {
				return task.Event{Kind: task.KindFail, Reason: "fake: " + err.Error()}
			}

			select {
			case <-time.After(dur):
			case <-ctx.Done():
				return task.Event{Kind: task.KindFail, Reason: "fake: cancelled", Turns: turns}
			}
		case "write":
			path, text, _ := strings.Cut(arg, " ")
			if err := write(path, text); err != nil {
				return task.Event{Kind: task.KindFail, Reason: "fake: " + err.Error(), Turns: turns}
			}

			out.emit(task.Event{Kind: task.KindAction, Tool: "Write", Input: path, Result: "ok"})
		case "thought":
			out.emit(task.Event{Kind: task.KindThought, Text: arg})
		case "ask":
			return task.Event{Kind: task.KindAsk, Question: arg, Title: "Fake change", Turns: turns}
		case "fail":
			return task.Event{Kind: task.KindFail, Reason: arg, Turns: turns}
		default:
			return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("fake: unknown directive %q", verb), Turns: turns}
		}
	}

	return task.Event{Kind: task.KindDone, Title: "Fake change", Body: "Written by the fake agent backend.", Turns: turns}
}

// write creates path under the working directory (the workspace), refusing to leave it.
func write(path, text string) error {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q outside the workspace", path)
	}

	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return err
	}

	return os.WriteFile(clean, []byte(text+"\n"), 0o644)
}
