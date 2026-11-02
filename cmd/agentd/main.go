// Command agentd turns Linear issues into pull requests. It watches Linear (labels, mentions,
// delegation to the agent), runs a coding agent in a sandbox for each request, and publishes the
// result on GitHub. It also answers status queries and runs local trials.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/job"
	"github.com/matbour/sandbox-agent/internal/linear"
	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/runnerapi"
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
	addRunner := flag.String("add-runner", "", "create a token for the runner of this name and exit (with -token-out)")
	tokenOut := flag.String("token-out", "", "with -add-runner: file to write the new token to (mode 0600)")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case *addRunner != "":
		if err := runAddRunner(*secrets, *addRunner, *tokenOut); err != nil {
			fail(log, "add-runner", err)
		}
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
		} else if err = withRunners(ctx, cfg, log, r); err == nil {
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

	return &job.Runner{Cfg: cfg, Linear: lc, App: app, Log: log, Sandbox: sp, Exec: executor(sp)}, nil
}

// executor runs jobs through the worker when the provider's sandboxes carry it (D33), in-process.
func executor(sp sandbox.Provider) job.Executor {
	if c, ok := sp.(sandbox.WorkerCarrier); ok && c.CarriesWorker() {
		return runner.Local{Provider: sp}
	}

	return nil
}

// withRunners replaces the executor with the runners' queue when RUNNER_ADDR is set, and serves
// their API until ctx ends (D42).
func withRunners(ctx context.Context, cfg *config.Config, log *slog.Logger, r *job.Runner) error {
	if cfg.RunnerAddr == "" {
		return nil
	}

	srv, err := runnerapi.NewServer(runnerapi.DefaultTokenFile(cfg.SecretsDir), log.With("component", "runners"))
	if err != nil {
		return fmt.Errorf("runners: %w", err)
	}

	srv.TempDir = cfg.StateDir

	l, err := net.Listen("tcp", cfg.RunnerAddr)
	if err != nil {
		return fmt.Errorf("runners: %w", err)
	}

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() { _ = hs.Serve(l) }()
	go srv.Expire(ctx)
	go func() {
		<-ctx.Done()
		_ = hs.Close()
	}()

	log.Info("runners' API", "addr", l.Addr().String())

	r.Exec = srv

	return nil
}

// runAddRunner creates a random token for a runner, adds its hash to the manager's token file and
// writes the token itself to out, for the runner's machine. The token is never printed.
func runAddRunner(secretsDir, name, out string) error {
	if out == "" {
		return fmt.Errorf("-token-out is required: the token is not printed")
	}

	if strings.ContainsAny(name, " \t\n") || name == "" {
		return fmt.Errorf("invalid runner name %q", name)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}

	tok := "sar_" + hex.EncodeToString(b)

	if err := os.WriteFile(out, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}

	f, err := os.OpenFile(runnerapi.DefaultTokenFile(secretsDir), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(f, "%s %s\n", name, runnerapi.HashToken(tok)); err != nil {
		_ = f.Close()

		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("runner %s added; its token is in %s (copy it to the runner's machine, then delete it here)\n", name, out)

	return nil
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
