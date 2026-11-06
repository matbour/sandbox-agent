package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/sandboxtest"
)

// TestConformanceRestored runs the conformance suite on VMs restored from a template (D49): no
// tap, so every start restores.
//
//	SANDBOX_IT=1 go test ./internal/sandbox/firecracker/ -run ConformanceRestored -v
func TestConformanceRestored(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real microVMs; set SANDBOX_IT=1")
	}

	p := NewWith(Defaults(Config{StateDir: t.TempDir(), Taps: []string{}}, repoRoot(t)))

	sandboxtest.RunWith(t, p, t.TempDir(), sandboxtest.Options{OpenNetwork: true})
}

// TestRestore checks what a restored VM must not keep from its template: each of several VMs
// restored at once has its own hostname, its own workspace, its own random numbers, and the
// host's time.
func TestRestore(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real microVMs; set SANDBOX_IT=1")
	}

	p := NewWith(Defaults(Config{StateDir: t.TempDir(), Taps: []string{}}, repoRoot(t)))
	ctx := context.Background()

	const n = 4

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		randoms = map[string]string{}
	)

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name := fmt.Sprintf("restore-%d", i)

			ws := t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "job.txt"), []byte(name+"\n"), 0o644); err != nil {
				t.Error(err)

				return
			}

			sb, err := p.Start(ctx, sandbox.Spec{Name: name, Workspace: ws, CPUs: 1, Memory: "1g"})
			if err != nil {
				t.Error(err)

				return
			}
			defer func() { _ = sb.Close() }()

			if !sb.(*Sandbox).Timings().Restored {
				t.Errorf("%s: booted cold, not restored", name)
			}

			so, se, err := sb.Exec(ctx, sandbox.ExecOptions{}, "sh", "-c",
				"hostname; cat /etc/hostname; cat job.txt; date +%s; head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \\n'")
			if err != nil {
				t.Errorf("%s: %v %s", name, err, se)

				return
			}

			lines := strings.Split(strings.TrimSpace(string(so)), "\n")
			if len(lines) != 5 {
				t.Errorf("%s: output %q", name, so)

				return
			}

			for _, l := range lines[:3] {
				if l != name {
					t.Errorf("%s: got %q, want the VM's name (hostname, /etc/hostname, job.txt)", name, l)
				}
			}

			guest, _ := strconv.ParseInt(lines[3], 10, 64)
			if d := time.Now().Unix() - guest; d < -2 || d > 2 {
				t.Errorf("%s: guest clock %d s off the host's", name, d)
			}

			mu.Lock()
			if other, dup := randoms[lines[4]]; dup {
				t.Errorf("%s and %s read the same random bytes", name, other)
			}

			randoms[lines[4]] = name
			mu.Unlock()
		}()
	}

	wg.Wait()
}

func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	return root
}
