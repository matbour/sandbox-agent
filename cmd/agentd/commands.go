package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/job"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// runStatus prints the service state and recent jobs, or one job when issue is set.
func runStatus(cfg *config.Config, issue string) error {
	if n, err := job.Import(cfg.StateDir); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "imported %d job(s) into %s\n", n, filepath.Join(cfg.StateDir, job.StateFile))
	}

	if issue != "" {
		return printJob(os.Stdout, cfg.StateDir, issue)
	}

	return printStatus(os.Stdout, cfg.StateDir, 20)
}

// runTry runs a task on a local repository in a sandbox, without Linear or GitHub.
func runTry(ctx context.Context, cfg *config.Config, log *slog.Logger, repo, task, model string) error {
	sp, err := providers.New(cfg.Provider, sandbox.Options{CPUSet: cfg.CPUs})
	if err != nil {
		return err
	}

	r := &job.Runner{Cfg: cfg, Log: log, Sandbox: sp}
	if model == "" {
		model = cfg.AgentModel
	}

	ex, dir, err := r.Try(ctx, repo, task, model)
	if ex != nil {
		fmt.Printf("cost $%.2f, %d turns\n\n%s\n\n", ex.CostUSD, ex.Turns, ex.Summary())

		for _, w := range ex.Warnings {
			fmt.Println("warning:", w)
		}
	}

	fmt.Println("job directory:", dir)

	return err
}

// runCheck verifies GitHub and Linear access and lists the issues that would be picked up.
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

	me, err := r.Linear.Me(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Linear: acting as %q via the OAuth application\n", me.Name)

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

	fmt.Printf("Sandbox: %s\n", r.Sandbox.Name())

	return nil
}
