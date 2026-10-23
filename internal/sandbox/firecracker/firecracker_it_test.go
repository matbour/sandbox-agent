package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/sandboxtest"
	"github.com/matbour/sandbox-agent/internal/task"
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

	ws := t.TempDir()

	sb, err := p.Start(ctx, sandbox.Spec{Name: "guest-check", Workspace: ws, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	t.Run("worker", func(t *testing.T) {
		wp := sb.(sandbox.WorkerHost).WorkerPath()
		if wp == "" {
			t.Fatal("no worker in the guest (make build)")
		}

		in := `{"prompt":"x\nFAKE: write out.txt from the worker","agent":"fake"}`

		var events strings.Builder

		_, se, err := sb.Exec(ctx, sandbox.ExecOptions{Stdin: strings.NewReader(in), Stdout: &events}, wp)

		term := task.Read(strings.NewReader(events.String()), task.DefaultReadLimits, nil, nil)
		if err != nil || term.Kind != task.KindDone {
			t.Fatalf("worker: %v, terminal %+v, stderr %s", err, term, se)
		}

		if err := sb.(sandbox.Syncer).SyncWorkspace(ctx, nil); err != nil {
			t.Fatal(err)
		}

		if b, err := os.ReadFile(filepath.Join(ws, "out.txt")); err != nil || strings.TrimSpace(string(b)) != "from the worker" {
			t.Errorf("out.txt on the host: %q %v", b, err)
		}
	})

	// E-RT-4: the worker binary cannot be changed from inside, not even by root.
	t.Run("worker read-only", func(t *testing.T) {
		wp := sb.(sandbox.WorkerHost).WorkerPath()
		for _, u := range []string{"", "root"} {
			if _, _, err := sb.Exec(ctx, sandbox.ExecOptions{User: u}, "sh", "-c", "echo x >> "+wp); err == nil {
				t.Errorf("user %q could write the worker", u)
			}
		}
	})

	for name, c := range map[string]struct {
		o    sandbox.ExecOptions
		cmd  string
		want string
	}{
		"root":     {sandbox.ExecOptions{User: "root"}, "id -u", "0"},
		"hostname": {sandbox.ExecOptions{}, "hostname", "guest-check"},
		"tools":    {sandbox.ExecOptions{}, "git --version >/dev/null && bun --version >/dev/null && claude --version >/dev/null && curl --version >/dev/null && echo ok", "ok"},
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
