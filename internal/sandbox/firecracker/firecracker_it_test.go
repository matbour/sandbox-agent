package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/sandboxtest"
)

// Needs `make build` (bin/guest-agent), the rootfs and kernel in build/<arch>/ (scripts/
// build-rootfs.sh, firecracker/lib.sh), firecracker on PATH and the kvm group. The network part
// uses the host's fc-tap* devices if there are any (deploy/network/).
//
//	SANDBOX_IT=1 go test ./internal/sandbox/firecracker/ -run Conformance -v
func TestConformance(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real microVMs; set SANDBOX_IT=1")
	}

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	p := NewWith(Defaults(Config{StateDir: t.TempDir()}, root))

	sandboxtest.RunWith(t, p, t.TempDir(), sandboxtest.Options{OpenNetwork: true})
}

// TestGuest checks what the conformance suite does not: root, the network of D44 from a command,
// and the tools the agent needs.
func TestGuest(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real microVMs; set SANDBOX_IT=1")
	}

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	p := NewWith(Defaults(Config{StateDir: t.TempDir()}, root))
	if len(p.c.Taps) == 0 {
		t.Skip("no fc-tap* on this host (deploy/network/)")
	}

	ctx := context.Background()

	sb, err := p.Start(ctx, sandbox.Spec{Name: "guest-check", Workspace: t.TempDir(), CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	for name, c := range map[string]struct {
		o    sandbox.ExecOptions
		cmd  string
		want string
	}{
		"root":     {sandbox.ExecOptions{User: "root"}, "id -u", "0"},
		"hostname": {sandbox.ExecOptions{}, "hostname", "guest-check"},
		"tools":    {sandbox.ExecOptions{}, "git --version >/dev/null && bun --version >/dev/null && claude --version >/dev/null && echo ok", "ok"},
		"https": {sandbox.ExecOptions{}, `bun -e 'const r = await fetch("https://api.anthropic.com/v1/messages", {method: "POST"}); console.log(r.status)'`,
			"401"},
	} {
		t.Run(name, func(t *testing.T) {
			so, se, err := sb.Exec(ctx, c.o, "sh", "-c", c.cmd)
			if got := strings.TrimSpace(string(so)); err != nil || got != c.want {
				t.Errorf("%q: got %q, want %q (%v, stderr %s)", c.cmd, got, c.want, err, se)
			}
		})
	}
}
