package job

import "github.com/matbour/sandbox-agent/internal/task"

// The final message's format belongs to the protocol (internal/task): the worker parses it in the
// guest, and the manager still parses it for the agent runs that do not go through a worker.

// Decision is what the agent says about its work.
type Decision = task.Decision

// ParseDecision reads the STATUS and QUESTION lines of the agent's final message.
func ParseDecision(result string) (Decision, string) { return task.ParseDecision(result) }

// ParseSummary extracts the PR title and body from the agent's final message.
func ParseSummary(result, fallbackTitle string) (string, string) {
	return task.ParseSummary(result, fallbackTitle)
}
