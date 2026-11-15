// Package providers lists the sandbox implementations, for every command that starts sandboxes
// (agentd, sandbox task). Adding one here makes it available to all of them.
package providers

import (
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/firecracker"
	"github.com/matbour/sandbox-agent/internal/sandbox/gvisor"
	"github.com/matbour/sandbox-agent/internal/sandbox/sbx"
)

// Registry maps a provider's name (SANDBOX_PROVIDER, -provider) to its factory.
var Registry = sandbox.Registry{
	"sbx":         sbx.New,
	"firecracker": firecracker.New,
	"gvisor":      gvisor.New,
}
