// Command runnerd runs on every machine that runs jobs (D42, docs/decision/runners.md). It dials
// the manager, leases the runs that fit its free capacity, runs each one in a fresh Firecracker
// microVM, forwards the worker's output and returns the changed workspace. It holds no Linear or
// GitHub credential, only its own token.
//
//	runnerd -manager https://manager.example:8788 -token-file secrets/runner-token \
//	    -cpus 4 -memory 8192 -cpuset 4-7
//
// The token is made on the manager with agentd -add-runner. Plain HTTP is only allowed to a
// loopback address (manager and runner on one machine).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/matbour/sandbox-agent/internal/runnerapi"
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/firecracker"
)

func main() {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()

	manager := flag.String("manager", "http://127.0.0.1:8788", "the manager's runner API")
	tokenFile := flag.String("token-file", "secrets/runner-token", "this runner's token (agentd -add-runner)")
	name := flag.String("name", host, "this runner's name, as given to agentd -add-runner")
	cpus := flag.Int("cpus", max(runtime.NumCPU()/2, 1), "vCPUs offered to runs")
	memory := flag.Int("memory", 4096, "MiB of memory offered to runs")
	cpuset := flag.String("cpuset", "", "host cores for the microVMs, e.g. 4-7 (empty: any)")
	workdir := flag.String("workdir", filepath.Join(home, ".local/state/sandbox-agent/runner"), "workspaces of the runs in progress")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*manager, *tokenFile, *name, *cpus, *memory, *cpuset, *workdir, log); err != nil {
		log.Error("runnerd", "err", err)
		os.Exit(1)
	}
}

func run(manager, tokenFile, name string, cpus, memory int, cpuset, workdir string, log *slog.Logger) error {
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		return err
	}

	if fi, err := os.Stat(tokenFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: accessible to group or others (chmod 600)", tokenFile)
	}

	p, err := firecracker.New(sandbox.Options{CPUSet: cpuset})
	if err != nil {
		return err
	}

	if c, ok := p.(sandbox.WorkerCarrier); !ok || !c.CarriesWorker() {
		return fmt.Errorf("no worker binary next to runnerd (make build)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := p.Prepare(ctx); err != nil {
		return err
	}

	a := &runnerapi.Agent{
		Manager: strings.TrimSuffix(manager, "/"), Token: strings.TrimSpace(string(b)), Name: name, Version: "dev",
		Provider: p, Capacity: runnerapi.Capacity{CPUs: cpus, MemoryMiB: memory}, WorkDir: workdir, Log: log,
	}

	err = a.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}

	return err
}
