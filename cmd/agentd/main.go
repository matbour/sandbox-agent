// Command agentd turns Linear issues into pull requests. It watches Linear (labels, mentions,
// delegation to the agent), runs a coding agent in a sandbox for each request, and publishes the
// result on GitHub. It also answers status queries and runs local trials.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/job"
	"github.com/matbour/sandbox-agent/internal/linear"
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/firecracker"
	"github.com/matbour/sandbox-agent/internal/sandbox/sbx"
)

// providers are the sandbox implementations agentd can use (SANDBOX_PROVIDER).
var providers = sandbox.Registry{
	"sbx":         sbx.New,
	"firecracker": firecracker.New,
}

func main() {
	secrets := flag.String("secrets", defaultSecrets(), "secrets directory (config.env, Linear and GitHub credentials)")
	once := flag.Bool("once", false, "poll once, run what is found, and exit")
	check := flag.Bool("check", false, "check Linear and GitHub access, list matching issues, and exit")
	try := flag.String("try", "", "run -task on this local Git repository in a sandbox; no Linear, no push")
	task := flag.String("task", "", "task text for -try")
	model := flag.String("model", "", "model for -try (default AGENT_MODEL)")
	status := flag.Bool("status", false, "show the service, the queue and recent jobs")
	show := flag.String("show", "", "show the latest job for an issue (e.g. ENG-750)")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case *status || *show != "":
		cfg, err := config.LoadPartial(*secrets)
		if err != nil {
			fail(log, "config", err)
		}

		if err := runStatus(cfg, *show); err != nil {
			fail(log, "status", err)
		}
	case *try != "":
		cfg, err := config.LoadPartial(*secrets)
		if err != nil {
			fail(log, "config", err)
		}

		if err := runTry(ctx, cfg, log, *try, *task, *model); err != nil {
			fail(log, "try", err)
		}
	default:
		cfg, err := config.Load(*secrets)
		if err != nil {
			fail(log, "config", err)
		}

		r, err := newRunner(cfg, log)
		if err != nil {
			fail(log, "setup", err)
		}

		if *check {
			err = runCheck(ctx, r)
		} else {
			err = serve(ctx, r, *once)
		}

		if err != nil {
			fail(log, "agentd", err)
		}
	}
}

// newRunner builds the job runner with every external client: GitHub App, Linear, sandbox.
func newRunner(cfg *config.Config, log *slog.Logger) (*job.Runner, error) {
	app, err := github.NewApp(cfg.GitHubAppID, cfg.GitHubInstall, cfg.GitHubKeyPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}

	lc := linear.New(cfg.LinearClientID, cfg.LinearSecret, filepath.Join(cfg.StateDir, "linear-token.json"))

	sp, err := providers.New(cfg.Provider, sandbox.Options{CPUSet: cfg.CPUs})
	if err != nil {
		return nil, err
	}

	return &job.Runner{Cfg: cfg, Linear: lc, App: app, Log: log, Sandbox: sp}, nil
}

func fail(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}

// defaultSecrets is <repository>/secrets, found from the binary's location (<repository>/bin/agentd).
func defaultSecrets() string {
	exe, err := os.Executable()
	if err != nil {
		return "secrets"
	}

	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "secrets"
	}

	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "secrets")
}
