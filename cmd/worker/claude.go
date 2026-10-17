package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/matbour/sandbox-agent/internal/task"
)

// maxLine bounds one line of Claude Code's stream; a longer one (a huge tool result) is skipped.
const maxLine = 16 << 20

// claude runs Claude Code non-interactively and turns its stream-json output into events: text
// becomes thoughts, each tool call with its result becomes an action, and the final message is
// parsed into done or ask (STATUS:, QUESTION:, TITLE:, D26).
func claude(ctx context.Context, t task.Task, out *emitter) task.Event {
	ctx, cancel := context.WithTimeout(ctx, t.Limits.AgentTimeout())
	defer cancel()

	args := []string{"-p", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence"}
	if t.Limits.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(t.Limits.MaxTurns))
	}

	if t.Limits.BudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(t.Limits.BudgetUSD, 'f', -1, 64))
	}

	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}

	// The prompt goes on stdin: one argument is limited to 128 KiB on Linux.
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(t.Prompt)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return task.Event{Kind: task.KindFail, Reason: err.Error()}
	}

	if err := cmd.Start(); err != nil {
		return task.Event{Kind: task.KindFail, Reason: "start claude: " + err.Error()}
	}

	final, perr := follow(stdout, out)
	werr := cmd.Wait()

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("agent timed out after %s", t.Limits.AgentTimeout())}
	case final == nil && perr != nil:
		return task.Event{Kind: task.KindFail, Reason: "reading the agent's output: " + perr.Error()}
	case final == nil:
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("the agent ended without a result (%v)", werr)}
	}

	return decide(final)
}

// streamLine is the part of Claude Code's stream-json lines the worker reads.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`

	// type "result"
	IsError bool    `json:"is_error"`
	Result  string  `json:"result"`
	CostUSD float64 `json:"total_cost_usd"`
	Turns   int     `json:"num_turns"`
}

// follow reads the stream until it ends and returns the result line, if any.
func follow(r io.Reader, out *emitter) (*streamLine, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	pending := map[string]task.Event{}

	var final *streamLine

	for {
		line, err := readLine(br)
		if len(line) > 0 {
			var l streamLine
			if json.Unmarshal(line, &l) == nil {
				if l.Type == "result" {
					final = &l
				} else {
					translate(l, pending, out)
				}
			}
		}

		if errors.Is(err, io.EOF) {
			return final, nil
		}

		if err != nil {
			return final, err
		}
	}
}

// readLine returns the next line, or nil for a line over maxLine (skipped).
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte

	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) <= maxLine {
			line = append(line, chunk...)
		} else {
			line = line[:0:0]
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if line == nil && len(chunk) > 0 {
			logf("skipped a stream line over %d bytes", maxLine)
		}

		return bytes.TrimSpace(line), err
	}
}

// translate turns one assistant or user message into events. A tool call is held until its
// result arrives, so the action carries both.
func translate(l streamLine, pending map[string]task.Event, out *emitter) {
	for _, c := range l.Message.Content {
		switch {
		case l.Type == "assistant" && c.Type == "text" && strings.TrimSpace(c.Text) != "":
			out.emit(task.Event{Kind: task.KindThought, Text: c.Text})
		case l.Type == "assistant" && c.Type == "tool_use":
			pending[c.ID] = task.Event{Kind: task.KindAction, Tool: c.Name, Input: compact(c.Input)}
		case l.Type == "user" && c.Type == "tool_result":
			ev, ok := pending[c.ToolUseID]
			if !ok {
				continue
			}

			delete(pending, c.ToolUseID)

			ev.Result = toolResult(c.Content)
			if c.IsError {
				ev.Result = "error: " + ev.Result
			}

			out.emit(ev)
		}
	}
}

// compact renders a tool's input on one line, shortened (the manager caps it again).
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return ""
	}

	return shorten(b.String(), 2000)
}

// toolResult reads a tool_result's content: a string, or a list of text blocks.
func toolResult(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return shorten(s, 2000)
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}

	return shorten(strings.Join(parts, "\n"), 2000)
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}

	return s
}

// decide turns the agent's result into the terminal event.
func decide(l *streamLine) task.Event {
	if l.IsError {
		return task.Event{Kind: task.KindFail, Reason: fmt.Sprintf("agent stopped (%s): %s", l.Subtype, shorten(l.Result, 2000)),
			CostUSD: l.CostUSD, Turns: l.Turns}
	}

	d, rest := task.ParseDecision(l.Result)
	title, body := task.ParseSummary(rest, "")

	if d.Ready {
		return task.Event{Kind: task.KindDone, Title: title, Body: body, CostUSD: l.CostUSD, Turns: l.Turns}
	}

	return task.Event{Kind: task.KindAsk, Question: d.Question, Title: title, Body: body, CostUSD: l.CostUSD, Turns: l.Turns}
}
