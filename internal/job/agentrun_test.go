package job

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/matbour/sandbox-agent/internal/task"
)

func TestFinalMessageRoundTrip(t *testing.T) {
	d, rest := ParseDecision(finalMessage(task.Event{Kind: task.KindDone, Title: "Add multiply", Body: "Adds it."}))
	if title, body := ParseSummary(rest, "fallback"); !d.Ready || title != "Add multiply" || body != "Adds it." {
		t.Errorf("done: ready %v title %q body %q", d.Ready, title, body)
	}

	d, _ = ParseDecision(finalMessage(task.Event{Kind: task.KindAsk, Question: "Which API?"}))
	if d.Ready || d.Question != "Which API?" {
		t.Errorf("ask: %+v", d)
	}
}

func TestEventSinkKeepsTheTokenOut(t *testing.T) {
	var log bytes.Buffer

	s := &eventSink{ctx: context.Background(), log: json.NewEncoder(&log), secret: "sk-ant-oat01-secret-value"}
	s.event(task.Event{Kind: task.KindThought, Text: "fine"})
	s.event(task.Event{Kind: task.KindAction, Tool: "Bash", Result: "token is sk-ant-oat01-secret-value"})

	if !s.leaked || bytes.Contains(log.Bytes(), []byte("secret-value")) || !bytes.Contains(log.Bytes(), []byte("fine")) {
		t.Errorf("leaked %v, log %s", s.leaked, log.String())
	}
}
