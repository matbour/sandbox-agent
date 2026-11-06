package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// TestStartBench measures microVM starts on this machine: sequential starts phase by phase,
// starts at once, and the host memory an idle guest costs. Results go to docs/benchmarks.md.
//
//	SANDBOX_BENCH=1 go test ./internal/sandbox/firecracker/ -run StartBench -v
func TestStartBench(t *testing.T) {
	if os.Getenv("SANDBOX_BENCH") == "" {
		t.Skip("set SANDBOX_BENCH=1")
	}

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	p := NewWith(Defaults(Config{StateDir: t.TempDir(), Taps: []string{}}, root))
	ctx := context.Background()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("bench\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := func(name string) *Sandbox {
		sb, err := p.Start(ctx, sandbox.Spec{Name: name, Workspace: ws, CPUs: 1, Memory: "1g"})
		if err != nil {
			t.Fatal(err)
		}

		return sb.(*Sandbox)
	}

	// Warm the tools disk cache and the page cache once.
	_ = start("bench-warm").Close()

	const n = 20

	var all []Timings

	for i := range n {
		sb := start(fmt.Sprintf("bench-%d", i))
		all = append(all, sb.Timings())
		_ = sb.Close()
	}

	t.Logf("sequential, %d starts, 1 vCPU, 1 GiB, no network: p50 / p90 / max", n)

	for _, f := range []struct {
		name string
		get  func(Timings) time.Duration
	}{
		{"disks", func(x Timings) time.Duration { return x.Disks }},
		{"process", func(x Timings) time.Duration { return x.Process }},
		{"configure", func(x Timings) time.Duration { return x.Configure }},
		{"guest", func(x Timings) time.Duration { return x.Guest }},
		{"total", Timings.Total},
	} {
		var d []time.Duration
		for _, x := range all {
			d = append(d, f.get(x))
		}

		slices.Sort(d)
		t.Logf("  %-9s %6s %6s %6s", f.name, ms(d[n/2]), ms(d[n*9/10]), ms(d[n-1]))
	}

	for _, k := range []int{4, 8} {
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			total []time.Duration
		)

		began := time.Now()

		for i := range k {
			wg.Add(1)

			go func() {
				defer wg.Done()

				sb := start(fmt.Sprintf("bench-par%d-%d", k, i))
				mu.Lock()
				total = append(total, sb.Timings().Total())
				mu.Unlock()

				_ = sb.Close()
			}()
		}

		wg.Wait()
		slices.Sort(total)
		t.Logf("%d at once: all ready in %s, slowest start %s", k, ms(time.Since(began)), ms(total[k-1]))
	}

	// Idle memory: the firecracker process's RSS and PSS once the guest agent is up.
	var sbs []*Sandbox
	for i := range 4 {
		sbs = append(sbs, start(fmt.Sprintf("bench-idle-%d", i)))
	}

	time.Sleep(2 * time.Second)

	for _, sb := range sbs {
		rss, pss := memKiB(sb.VMMPid())
		t.Logf("idle guest (1 GiB configured): RSS %d MiB, PSS %d MiB", rss/1024, pss/1024)

		_ = sb.Close()
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%dms", d.Milliseconds()) }

// memKiB reads Rss and Pss from /proc/<pid>/smaps_rollup.
func memKiB(pid int) (rss, pss int) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "smaps_rollup"))
	if err != nil {
		return 0, 0
	}

	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}

		n, _ := strconv.Atoi(f[1])

		switch f[0] {
		case "Rss:":
			rss = n
		case "Pss:":
			pss = n
		}
	}

	return rss, pss
}
