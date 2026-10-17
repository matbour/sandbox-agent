// Package task is the contract of one run between the manager and the worker (D33,
// docs/decision/protocol.md): the Task the manager writes on the worker's stdin, and the Events
// the worker writes on its stdout, one JSON object per line.
//
// It imports only the standard library. The worker, the sandbox runtime and this package must
// not depend on Linear, GitHub, Git or the configuration (boundary_test.go).
package task

import "time"

// Agent backends.
const (
	AgentClaude = "claude"
	AgentFake   = "fake" // scripted, no model: for tests and load (runners.md)
)

// Task is the worker's input. It holds no Linear or GitHub identity: no IDs, no URLs, no token.
type Task struct {
	Prompt string `json:"prompt"` // rendered by the manager; the issue's text inside is quoted as data
	Model  string `json:"model,omitempty"`
	Agent  string `json:"agent"`
	Setup  string `json:"setup,omitempty"` // bash script run before the agent; empty: none
	Limits Limits `json:"limits"`
}

// Limits bound the run. Durations are seconds, so the JSON stays plain.
type Limits struct {
	MaxTurns        int     `json:"max_turns,omitempty"`
	BudgetUSD       float64 `json:"budget_usd,omitempty"`
	AgentTimeoutSec int     `json:"agent_timeout_s,omitempty"`
	SetupTimeoutSec int     `json:"setup_timeout_s,omitempty"`
}

// AgentTimeout returns the agent's wall clock limit, 45 minutes by default.
func (l Limits) AgentTimeout() time.Duration { return seconds(l.AgentTimeoutSec, 45*time.Minute) }

// SetupTimeout returns the setup script's wall clock limit, 20 minutes by default.
func (l Limits) SetupTimeout() time.Duration { return seconds(l.SetupTimeoutSec, 20*time.Minute) }

func seconds(n int, def time.Duration) time.Duration {
	if n <= 0 {
		return def
	}

	return time.Duration(n) * time.Second
}

// Event kinds. The worker never emits "response": done is a claim the manager checks, and only
// the manager closes a session (protocol.md).
const (
	KindThought = "thought"
	KindAction  = "action"
	KindAsk     = "ask"
	KindDone    = "done"
	KindFail    = "fail"
)

// Event is one line of the worker's output. Which fields are set depends on Kind.
type Event struct {
	Kind string `json:"kind"`

	Text string `json:"text,omitempty"` // thought

	Tool   string `json:"tool,omitempty"` // action
	Input  string `json:"input,omitempty"`
	Result string `json:"result,omitempty"`

	Question string `json:"question,omitempty"` // ask
	Title    string `json:"title,omitempty"`    // ask, done
	Body     string `json:"body,omitempty"`     // ask, done

	Reason string `json:"reason,omitempty"` // fail

	CostUSD float64 `json:"cost_usd,omitempty"` // ask, done, fail: self-reported
	Turns   int     `json:"turns,omitempty"`
}

// Terminal reports whether e ends the run.
func (e Event) Terminal() bool {
	return e.Kind == KindAsk || e.Kind == KindDone || e.Kind == KindFail
}
