package runner

import (
	"context"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/task"
)

// Local executes runs in-process on a provider: agentd without remote runners.
type Local struct {
	Provider sandbox.Provider
}

// Name is the provider's.
func (l Local) Name() string { return l.Provider.Name() }

// Execute runs spec on the provider.
func (l Local) Execute(ctx context.Context, spec Spec, workspace string, events func(task.Event)) (Result, error) {
	return Execute(ctx, l.Provider, spec, workspace, Hooks{Events: events})
}
