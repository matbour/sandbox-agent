package runnerapi

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matbour/sandbox-agent/backends/firecracker"
	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/task"
)

// TestFirecrackerFleet runs eight fake runs at once through the real API on two runners with
// different capacities, each starting real microVMs: the scheduling of D42 on one machine. It
// needs what the Firecracker provider's tests need (make build, build/<arch>/, the kvm group).
//
//	SANDBOX_IT=1 go test ./internal/runnerapi/ -run Fleet -v
func TestFirecrackerFleet(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real microVMs; set SANDBOX_IT=1")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	f := newFixture(t, "big", "small")
	state := t.TempDir()

	for name, c := range map[string]Capacity{"big": {CPUs: 3, MemoryMiB: 3072}, "small": {CPUs: 1, MemoryMiB: 1024}} {
		p := firecracker.NewWith(firecracker.Defaults(firecracker.Config{StateDir: state}, root))
		a := &Agent{Manager: f.url, Token: f.tokens[name], Name: name, Provider: p, Capacity: c, WorkDir: t.TempDir()}

		go func() { _ = a.Run(f.ctx) }()
	}

	const runs = 8

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		times []time.Duration
	)

	start := time.Now()

	for i := range runs {
		wg.Add(1)

		go func() {
			defer wg.Done()

			ws := t.TempDir()
			spec := runner.Spec{ID: fmt.Sprintf("fleet-%d", i), CPUs: 1, Memory: "1g",
				Task: task.Task{Agent: task.AgentFake, Prompt: fmt.Sprintf("run %d\nFAKE: sleep 2s\nFAKE: write out.txt run %d", i, i)}}

			began := time.Now()

			res, err := f.srv.Execute(f.ctx, spec, ws, nil)
			if err != nil || res.Terminal.Kind != task.KindDone {
				t.Errorf("%s: %+v %v\n%s", spec.ID, res.Terminal, err, res.Log)

				return
			}

			if b, err := os.ReadFile(filepath.Join(ws, "out.txt")); err != nil || string(b) != fmt.Sprintf("run %d\n", i) {
				t.Errorf("%s: out.txt %q %v", spec.ID, b, err)
			}

			mu.Lock()
			times = append(times, time.Since(began))
			mu.Unlock()
		}()
	}

	wg.Wait()

	// 4 slots in all for 8 runs of about 2 s each: two waves.
	t.Logf("%d runs on 2 runners (4 vCPUs in all) in %s; per run, queue wait included: %v",
		runs, time.Since(start).Round(time.Millisecond), times)
}
