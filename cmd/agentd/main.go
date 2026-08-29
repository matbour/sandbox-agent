// Command agentd polls Linear for labelled issues, runs a coding agent on each one inside a
// Docker Sandboxes microVM, and opens a draft pull request with the result.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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
)

func main() {
	cfgPath := flag.String("secrets", defaultSecrets(), "secrets directory (config.env, linear-api-key, github-app.pem)")
	once := flag.Bool("once", false, "poll once, run what is found, and exit")
	check := flag.Bool("check", false, "check Linear and GitHub access, list matching issues, and exit")
	try := flag.String("try", "", "run -task on this local Git repository in a sandbox; no Linear, no push")
	task := flag.String("task", "", "task text for -try")
	model := flag.String("model", "", "model for -try (default AGENT_MODEL)")
	status := flag.Bool("status", false, "show the service, running sandboxes and recent jobs")
	show := flag.String("show", "", "show the latest job for an issue (e.g. ENG-750)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *status || *show != "" {
		cfg, err := config.LoadPartial(*cfgPath)
		if err != nil {
			fail(log, "config", err)
		}
		if *show != "" {
			err = printJob(os.Stdout, cfg.StateDir, *show)
		} else {
			err = printStatus(os.Stdout, cfg.StateDir, 20)
		}
		if err != nil {
			fail(log, "status", err)
		}
		return
	}

	if *try != "" {
		cfg, err := config.LoadPartial(*cfgPath)
		if err != nil {
			fail(log, "config", err)
		}
		r := &job.Runner{Cfg: cfg, Log: log}
		m := cfg.AgentModel
		if *model != "" {
			m = *model
		}
		ex, dir, err := r.Try(ctx, *try, *task, m)
		if ex != nil {
			fmt.Printf("cost $%.2f, %d turns\n\n%s\n\n", ex.CostUSD, ex.Turns, ex.Summary())
			for _, w := range ex.Warnings {
				fmt.Println("warning:", w)
			}
		}
		fmt.Println("job directory:", dir)
		if err != nil {
			fail(log, "try", err)
		}
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fail(log, "config", err)
	}
	app, err := github.NewApp(cfg.GitHubAppID, cfg.GitHubInstall, cfg.GitHubKeyPath)
	if err != nil {
		fail(log, "GitHub App key", err)
	}
	r := &job.Runner{Cfg: cfg, Linear: linear.New(cfg.LinearAPIKey), App: app, Log: log}

	if *check {
		if err := runCheck(ctx, r); err != nil {
			fail(log, "check", err)
		}
		return
	}

	log.Info("agentd started", "repo", cfg.Repo, "labels", cfg.Labels(), "default_model", cfg.AgentModel,
		"teams", cfg.TeamKeys,
		"every", cfg.PollInterval)
	for {
		issues, err := r.Linear.IssuesWithLabel(ctx, cfg.Labels(), cfg.TeamKeys)
		if err != nil {
			log.Error("poll Linear", "err", err)
		}
		for _, is := range issues {
			if ctx.Err() != nil {
				break
			}
			r.Handle(ctx, is)
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			log.Info("stopping")
			return
		case <-time.After(cfg.PollInterval):
		}
	}
}

func runCheck(ctx context.Context, r *job.Runner) error {
	tok, err := r.App.InstallationToken(ctx)
	if err != nil {
		return fmt.Errorf("GitHub installation token: %w", err)
	}
	owner, name, _ := strings.Cut(r.Cfg.Repo, "/")
	repo := &github.Repo{Token: tok, Owner: owner, Name: name, HTTP: r.App.HTTP}
	branch, err := repo.DefaultBranch(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("GitHub: %s reachable, default branch %s, token expires %s\n",
		r.Cfg.Repo, branch, tok.ExpiresAt.Format(time.RFC3339))
	issues, err := r.Linear.IssuesWithLabel(ctx, r.Cfg.Labels(), r.Cfg.TeamKeys)
	if err != nil {
		return err
	}
	fmt.Printf("Linear: %d open issue(s) labelled %s\n", len(issues), strings.Join(r.Cfg.Labels(), ", "))
	for _, l := range r.Cfg.ModelLabels {
		fmt.Printf("  label %-14s -> %s\n", l.Label, l.Model)
	}
	fmt.Printf("  label %-14s -> %s (default)\n", r.Cfg.TriggerLabel, r.Cfg.AgentModel)
	for _, is := range issues {
		fmt.Printf("  %s  %s\n", is.Identifier, is.Title)
	}
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
