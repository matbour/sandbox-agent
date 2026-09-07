// Package sbx is the Docker Sandboxes provider (the sbx CLI): one KVM microVM per sandbox, egress
// through a host proxy that injects the API key. See docs/docker-sandboxes.md.
package sbx

import (
	"context"
	"fmt"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Provider starts Docker Sandboxes.
type Provider struct{ CPUSet string }

// New builds the provider.
func New(o sandbox.Options) (sandbox.Provider, error) {
	if o.CPUSet == "" {
		return nil, fmt.Errorf("sbx: a CPU set is required (CPUS)")
	}
	return &Provider{CPUSet: o.CPUSet}, nil
}

// Name returns "sbx".
func (p *Provider) Name() string { return "sbx" }

// Prepare makes sure the sbx daemon runs pinned to the CPU set, so the VMs inherit it.
func (p *Provider) Prepare(ctx context.Context) error { return EnsureDaemon(ctx, p.CPUSet) }

// Start creates a Claude sandbox on the workspace.
func (p *Provider) Start(ctx context.Context, sp sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := Create(ctx, sp.Name, sp.Workspace, Options{CPUs: sp.CPUs, Memory: sp.Memory, DenyNetwork: sp.DenyNetwork})
	if err != nil {
		return nil, err
	}
	sb.cpus = p.CPUSet
	return sb, nil
}

var (
	_ sandbox.Provider     = (*Provider)(nil)
	_ sandbox.Sandbox      = (*Sandbox)(nil)
	_ sandbox.CacheTrimmer = (*Sandbox)(nil)
)
