// Package backends lists the sandbox implementations, for every command that starts sandboxes
// (agentd, sandbox task). Adding one here makes it available to all of them.
package backends

import (
	"github.com/matbour/sandbox-agent/backends/firecracker"
	"github.com/matbour/sandbox-agent/backends/gvisor"
	"github.com/matbour/sandbox-agent/backends/sbx"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Registry maps a provider's name (SANDBOX_PROVIDER, -provider) to its factory.
var Registry = sandbox.Registry{
	"sbx":         sbx.New,
	"firecracker": firecracker.New,
	"gvisor":      gvisor.New,
}
