// Package sandbox defines what agentd needs from a sandbox, so that several isolation
// technologies can be plugged in and compared: Docker Sandboxes (microVM, package sbx) first,
// gVisor and the project's own runtime next.
//
// Every provider must guarantee, and sandboxtest checks:
//   - the job's workspace is read-write and is the working directory of commands. It is either
//     shared with the host, or a copy that Syncer brings back (Firecracker has no shared
//     directory);
//   - commands do not run as root on the host;
//   - the agent can reach the model API without the API key entering the sandbox (the key is
//     injected outside, by a proxy). Waived for Firecracker in the development phase (D43);
//   - the host's loopback, the LAN and cloud metadata addresses are not reachable. Waived for
//     Firecracker in the development phase (D44);
//   - Close removes everything the sandbox created, except the workspace.
package sandbox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Spec describes the sandbox for one job.
type Spec struct {
	Name        string   // unique, lowercase letters, digits and hyphens
	Workspace   string   // host directory mounted read-write
	CPUs        int      // 0: provider default
	Memory      string   // e.g. "6g"; empty: provider default
	DenyNetwork []string // extra hosts to block for this sandbox, on top of the provider's policy
}

// ExecOptions adjust one command.
type ExecOptions struct {
	User string   // empty: the sandbox's unprivileged user; "root": root inside the sandbox
	Dir  string   // empty: the workspace
	Env  []string // extra KEY=VALUE pairs
}

// Sandbox is a running sandbox.
type Sandbox interface {
	Name() string
	// WorkDir is the workspace path as seen inside the sandbox.
	WorkDir() string
	// Exec runs argv and returns its output. Cancelling ctx stops the command.
	Exec(ctx context.Context, o ExecOptions, argv ...string) (stdout, stderr []byte, err error)
	// Close stops the sandbox and removes it. It must work after ctx was cancelled.
	Close() error
}

// Provider starts sandboxes.
type Provider interface {
	Name() string
	// Prepare checks prerequisites and starts what the provider needs (a daemon, for
	// example). agentd calls it before each job, while no other job is running.
	Prepare(ctx context.Context) error
	Start(ctx context.Context, s Spec) (Sandbox, error)
}

// Syncer is implemented by sandboxes whose workspace is a copy rather than a shared directory.
// SyncWorkspace makes the host's workspace match the sandbox's: files created, changed and
// deleted, except paths matching exclude (patterns as in guestexec.Excluded). Callers sync before
// reading the workspace. What comes back is hostile input and is validated after, as before (D5).
type Syncer interface {
	SyncWorkspace(ctx context.Context, exclude []string) error
}

// CacheTrimmer is implemented by sandboxes whose memory is not given back to the host on its own
// (a VM keeps its page cache).
type CacheTrimmer interface {
	TrimCache(ctx context.Context, interval time.Duration) error
	DropCache(ctx context.Context)
}

// Options are the host-level settings a provider may use.
type Options struct {
	CPUSet string // host cores for the sandboxes, e.g. "4-7"
}

// Factory builds a provider.
type Factory func(Options) (Provider, error)

// Registry maps provider names to factories.
type Registry map[string]Factory

// New builds the named provider.
func (r Registry) New(name string, o Options) (Provider, error) {
	f, ok := r[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox provider %q (known: %s)", name, strings.Join(r.Names(), ", "))
	}

	return f(o)
}

// Names lists the registered providers.
func (r Registry) Names() []string {
	var out []string
	for n := range r {
		out = append(out, n)
	}

	sort.Strings(out)

	return out
}
