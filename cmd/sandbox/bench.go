package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/matbour/sandbox-agent/backends"
	"github.com/matbour/sandbox-agent/backends/firecracker"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// benchAgent is what "the agent is up" means: its binary loads and answers. On a fresh sandbox
// this reads the 300 MB Claude Code binary from the image, which a template or a page cache may
// already hold.
var benchAgent = []string{"claude", "--version"}

// size is one machine size of the benchmark.
type size struct {
	CPUs   int
	Memory string
}

func (s size) String() string { return fmt.Sprintf("%dc/%s", s.CPUs, s.Memory) }

// parseSizes reads "2:2g,1:1g,1:512m".
func parseSizes(v string) ([]size, error) {
	var out []size

	for f := range strings.SplitSeq(v, ",") {
		c, m, ok := strings.Cut(strings.TrimSpace(f), ":")

		n, err := strconv.Atoi(c)
		if !ok || err != nil || n < 1 || m == "" {
			return nil, fmt.Errorf("bad size %q: want <vCPUs>:<memory>, e.g. 2:2g", f)
		}

		out = append(out, size{CPUs: n, Memory: m})
	}

	return out, nil
}

// sample is one start: Provider.Start, then the agent's first answer, then Close.
type sample struct {
	Start, Agent, Close time.Duration
}

// Ready is the time from nothing to an agent that answers.
func (s sample) Ready() time.Duration { return s.Start + s.Agent }

// runBench measures sandbox starts per provider and machine size, up to the agent answering:
//
//	sandbox bench -providers firecracker,firecracker-cold,gvisor,sbx -sizes 2:2g,1:1g,1:512m -n 10
//
// "first" is the first start of the process: it pays for what the provider builds once (the
// Firecracker template, which a fresh state directory forces) and whatever the host has not
// cached yet. "warm" are the next starts. firecracker-cold boots every VM instead of restoring
// the template (D49), to compare the two.
func runBench(args []string) int {
	fs := flag.NewFlagSet("sandbox bench", flag.ExitOnError)
	providers := fs.String("providers", "firecracker,firecracker-cold,gvisor,sbx",
		"providers to measure: "+strings.Join(append(backends.Registry.Names(), "firecracker-cold"), ", "))
	sizes := fs.String("sizes", "1:1g,2:2g", "machine sizes, <vCPUs>:<memory>, comma-separated")
	n := fs.Int("n", 10, "warm starts per provider and size")
	cpuset := fs.String("cpuset", "", "host cores for the sandboxes, e.g. 4-7 (sbx needs one)")
	out := fs.String("out", "build/bench/start.tsv", "TSV file the samples are appended to; empty: none")
	_ = fs.Parse(args)

	sz, err := parseSizes(*sizes)
	if err == nil && *n < 1 {
		err = errors.New("-n must be at least 1")
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox bench: %v\n", err)

		return 2
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tsv := io.Discard

	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "sandbox bench: %v\n", err)

			return 1
		}

		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sandbox bench: %v\n", err)

			return 1
		}
		defer func() { _ = f.Close() }()

		tsv = f
	}

	date, commit := time.Now().Format(time.RFC3339), gitCommit()
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(table, "provider\tsize\tfirst ready\twarm ready p50\tp90\tstart p50\tagent p50\tclose p50\t")

	failed := false

	for name := range strings.SplitSeq(*providers, ",") {
		name = strings.TrimSpace(name)

		for _, s := range sz {
			if ctx.Err() != nil {
				return 1
			}

			first, warm, err := benchOne(ctx, name, s, *n, *cpuset)
			if err != nil {
				failed = true

				fmt.Fprintf(os.Stderr, "%s %s: %v\n", name, s, err)
				_, _ = fmt.Fprintf(table, "%s\t%s\tfailed\t\t\t\t\t\t\n", name, s)

				continue
			}

			for i, x := range append([]sample{first}, warm...) {
				kind := "warm"
				if i == 0 {
					kind = "first"
				}

				_, _ = fmt.Fprintf(tsv, "%s\t%s\t%s\t%d\t%s\t%s\t%d\t%d\t%d\t%d\n", date, name, commit, s.CPUs, s.Memory,
					kind, i, x.Start.Milliseconds(), x.Agent.Milliseconds(), x.Close.Milliseconds())
			}

			_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", name, s, ms(first.Ready()),
				ms(pct(warm, sample.Ready, 50)), ms(pct(warm, sample.Ready, 90)),
				ms(pct(warm, func(x sample) time.Duration { return x.Start }, 50)),
				ms(pct(warm, func(x sample) time.Duration { return x.Agent }, 50)),
				ms(pct(warm, func(x sample) time.Duration { return x.Close }, 50)))
		}
	}

	_ = table.Flush()

	if failed {
		return 1
	}

	return 0
}

// benchOne measures one provider at one size, with a provider of its own so that "first" is
// the first start it ever makes.
func benchOne(ctx context.Context, name string, s size, n int, cpuset string) (first sample, warm []sample, err error) {
	state, err := os.MkdirTemp("", "sandbox-bench-")
	if err != nil {
		return first, nil, err
	}
	defer func() { _ = os.RemoveAll(state) }()

	p, err := benchProvider(name, cpuset, filepath.Join(state, "state"))
	if err != nil {
		return first, nil, err
	}

	if err := p.Prepare(ctx); err != nil {
		return first, nil, fmt.Errorf("prepare: %w", err)
	}

	ws := filepath.Join(state, "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		return first, nil, err
	}

	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("bench\n"), 0o644); err != nil {
		return first, nil, err
	}

	for i := range n + 1 {
		x, err := startOnce(ctx, p, sandbox.Spec{
			Name: fmt.Sprintf("bench-%d-%d", os.Getpid()%100000, i), Workspace: ws, CPUs: s.CPUs, Memory: s.Memory,
		})
		if err != nil {
			return first, nil, fmt.Errorf("start %d: %w", i, err)
		}

		if i == 0 {
			first = x
		} else {
			warm = append(warm, x)
		}
	}

	return first, warm, nil
}

func startOnce(ctx context.Context, p sandbox.Provider, spec sandbox.Spec) (sample, error) {
	var x sample

	t0 := time.Now()

	sb, err := p.Start(ctx, spec)
	if err != nil {
		return x, err
	}

	x.Start = time.Since(t0)

	t1 := time.Now()
	_, stderr, err := sb.Exec(ctx, sandbox.ExecOptions{}, benchAgent...)
	x.Agent = time.Since(t1)

	t2 := time.Now()
	closeErr := sb.Close()
	x.Close = time.Since(t2)

	if err != nil {
		return x, fmt.Errorf("%s: %w: %s", strings.Join(benchAgent, " "), err, strings.TrimSpace(string(stderr)))
	}

	return x, closeErr
}

// benchProvider builds the named provider. The Firecracker ones get a state directory of their
// own, so their first start builds the template and the tools disk as on a new host.
func benchProvider(name, cpuset, state string) (sandbox.Provider, error) {
	cold := name == "firecracker-cold"
	if name != "firecracker" && !cold {
		return backends.Registry.New(name, sandbox.Options{CPUSet: cpuset})
	}

	root, err := binaryRoot()
	if err != nil {
		return nil, err
	}

	return firecracker.NewWith(firecracker.Defaults(firecracker.Config{
		StateDir: state, CPUSet: cpuset, NoSnapshots: cold, Log: slog.Default(),
	}, root)), nil
}

// binaryRoot is the repository this binary was built in: <root>/bin/sandbox or
// <root>/bin/linux-<arch>/sandbox, as the providers' own factories assume.
func binaryRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(exe)
	if strings.HasPrefix(filepath.Base(dir), "linux-") {
		dir = filepath.Dir(dir)
	}

	return filepath.Dir(dir), nil
}

// pct is the p-th percentile of f over xs, nearest rank.
func pct(xs []sample, f func(sample) time.Duration, p int) time.Duration {
	if len(xs) == 0 {
		return 0
	}

	d := make([]time.Duration, 0, len(xs))
	for _, x := range xs {
		d = append(d, f(x))
	}

	slices.Sort(d)

	i := (p*len(d)+99)/100 - 1

	return d[max(i, 0)]
}

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "-"
	}

	return strings.TrimSpace(string(out))
}
