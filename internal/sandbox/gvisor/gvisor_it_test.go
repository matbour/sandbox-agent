package gvisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/sandboxtest"
)

// Needs rootless Podman, runsc with its AppArmor profile (docs/host.md), the agent image
// (scripts/build-rootfs.sh) and `make build` for the worker.
//
//	SANDBOX_IT=1 go test ./internal/sandbox/gvisor/ -run Conformance -v
func TestConformance(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real sandboxes; set SANDBOX_IT=1")
	}

	sandboxtest.Run(t, provider(t), t.TempDir())
}

// TestGuest checks what the conformance suite does not: the guest runs on gVisor's kernel, has
// the tools and the worker, reaches the model API, and its limits are in place.
func TestGuest(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real sandboxes; set SANDBOX_IT=1")
	}

	p := provider(t)
	ctx := context.Background()

	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	sb, err := p.Start(ctx, sandbox.Spec{Name: "gvisor-check", Workspace: t.TempDir(), CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	for name, c := range map[string]struct {
		o    sandbox.ExecOptions
		cmd  string
		want string
	}{
		"gvisor kernel": {sandbox.ExecOptions{}, "dmesg | grep -c gVisor >/dev/null && echo gvisor", "gvisor"},
		"root":          {sandbox.ExecOptions{User: "root"}, "id -u", "0"},
		"hostname":      {sandbox.ExecOptions{}, "hostname", "gvisor-check"},
		"tools": {sandbox.ExecOptions{}, "git --version >/dev/null && bun --version >/dev/null && claude --version >/dev/null && echo ok",
			"ok"},
		"worker read-only": {sandbox.ExecOptions{User: "root"}, "echo x >> " + workerPath + " 2>/dev/null || echo refused", "refused"},
		"https": {sandbox.ExecOptions{}, `curl -s -o /dev/null -w '%{http_code}' -X POST https://api.anthropic.com/v1/messages`,
			"401"},
	} {
		t.Run(name, func(t *testing.T) {
			so, se, err := sb.Exec(ctx, c.o, "sh", "-c", c.cmd)
			if got := strings.TrimSpace(string(so)); err != nil || got != c.want {
				t.Errorf("%q: got %q, want %q (%v, stderr %s)", c.cmd, got, c.want, err, se)
			}
		})
	}

	t.Run("memory limit", func(t *testing.T) {
		out, err := findScopeFile("sandbox-agent-gvisor-check.scope", "memory.max")

		if err != nil || strings.TrimSpace(string(out)) != "1073741824" {
			t.Errorf("scope memory.max %q, %v", out, err)
		}
	})
}

func provider(t *testing.T) *Provider {
	t.Helper()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	return NewWith(Defaults(Config{}, root))
}

// findScopeFile finds a scope's cgroup file under the account's systemd tree.
func findScopeFile(scope, file string) ([]byte, error) {
	matches, _ := filepath.Glob("/sys/fs/cgroup/user.slice/user-*.slice/user@*.service/*/" + scope + "/" + file)
	if len(matches) == 0 {
		return nil, os.ErrNotExist
	}

	return os.ReadFile(matches[0])
}
