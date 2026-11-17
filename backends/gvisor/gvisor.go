// Package gvisor runs sandboxes in gVisor (runsc), through rootless Podman (D51).
//
// One sandbox is one Podman container whose OCI runtime is runsc: the agent's system calls reach
// gVisor's user-space kernel, not the host's. Podman provides what rootless runsc alone cannot: a
// full user namespace from /etc/subuid, so --userns=keep-id makes the agent (UID 1000 in the
// image) the host account, and the workspace a plain bind mount, shared both ways. Its rootless
// network (pasta) carries gVisor's own network stack (runsc --network=sandbox).
//
// Podman cannot create cgroups for runsc here (the systemd manager needs an interactive
// authorization, and runsc ignores cgroups), so each sandbox runs in a transient systemd scope
// of the account's delegated tree, which carries the memory and CPU limits: every process of the
// sandbox is born in it.
//
// runsc must be allowed user namespaces by AppArmor (docs/host.md).
package gvisor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Config locates the tools and the image. Zero values get the defaults of Defaults.
type Config struct {
	Podman    string // the podman binary
	Runsc     string // runsc; resolved to its real path, which the AppArmor profile names
	Image     string // the agent image (scripts/build-rootfs.sh)
	Worker    string // cmd/worker for the image's architecture; optional
	Network   string // podman network: "pasta", or "none"
	CPUSet    string // host cores for the sandboxes (taskset)
	MaxOutput int    // per stream of one Exec
	Log       *slog.Logger
}

// Defaults fills Config from the repository layout: binaries in <root>/bin (or
// <root>/bin/linux-<arch>).
func Defaults(c Config, root string) Config {
	arch := runtime.GOARCH

	bin := filepath.Join(root, "bin")
	if _, err := os.Stat(filepath.Join(bin, "worker")); err != nil {
		bin = filepath.Join(root, "bin", "linux-"+arch)
	}

	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}

	def(&c.Podman, "podman")
	def(&c.Runsc, "runsc")
	def(&c.Image, "localhost/sandbox-agent-rootfs:"+arch)
	def(&c.Worker, filepath.Join(bin, "worker"))
	def(&c.Network, "pasta")

	if c.MaxOutput == 0 {
		c.MaxOutput = 256 << 20
	}

	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return c
}

// New is the registry factory: defaults relative to the running binary (<root>/bin/agentd).
func New(o sandbox.Options) (sandbox.Provider, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	root := filepath.Dir(filepath.Dir(exe))
	if strings.HasPrefix(filepath.Base(filepath.Dir(exe)), "linux-") {
		root = filepath.Dir(root)
	}

	return &Provider{c: Defaults(Config{CPUSet: o.CPUSet, Log: slog.Default()}, root)}, nil
}

// NewWith builds a provider from an explicit configuration (tests).
func NewWith(c Config) *Provider { return &Provider{c: c} }

// Provider starts gVisor sandboxes.
type Provider struct {
	c Config
}

// Name returns "gvisor".
func (p *Provider) Name() string { return "gvisor" }

// CarriesWorker reports whether the worker binary is there to mount in the sandbox.
func (p *Provider) CarriesWorker() bool {
	_, err := os.Stat(p.c.Worker)

	return err == nil
}

var (
	_ sandbox.Provider      = (*Provider)(nil)
	_ sandbox.WorkerCarrier = (*Provider)(nil)
	_ sandbox.Sandbox       = (*Sandbox)(nil)
	_ sandbox.WorkerHost    = (*Sandbox)(nil)
)
