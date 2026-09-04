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
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/job"
	"github.com/matbour/sandbox-agent/internal/linear"
	"github.com/matbour/sandbox-agent/internal/web"
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
		if n, err := job.Import(cfg.StateDir); err == nil && n > 0 {
			fmt.Fprintf(os.Stderr, "imported %d job(s) into %s\n", n, filepath.Join(cfg.StateDir, job.StateFile))
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
	lc := linear.New(cfg.LinearAPIKey)
	if cfg.LinearClientID != "" {
		lc = linear.NewApp(cfg.LinearClientID, cfg.LinearSecret, filepath.Join(cfg.StateDir, "linear-token.json"))
		if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
			fail(log, "state dir", err)
		}
	}
	r := &job.Runner{Cfg: cfg, Linear: lc, App: app, Log: log}

	if *check {
		if err := runCheck(ctx, r); err != nil {
			fail(log, "check", err)
		}
		return
	}

	if n, err := job.Import(cfg.StateDir); err != nil {
		log.Warn("import old job status files", "err", err)
	} else if n > 0 {
		log.Info("imported old job status files", "jobs", n)
	}

	// Mentions of the agent in Linear comments update the issue's pull request.
	var mention *regexp.Regexp
	meID, meName, err := r.Linear.Me(ctx)
	if err != nil {
		log.Warn("cannot read the Linear identity; mentions disabled", "err", err)
	} else {
		mention = job.MentionPattern(meName)
		log.Info("mentions enabled", "as", "@"+meName, "since", job.MentionWatermark(cfg.StateDir))
	}
	mentioned := func(body string) bool { return mention != nil && mention.MatchString(body) }

	overviewURL := cfg.OverviewURL
	wake := make(chan struct{}, 1)
	if cfg.WebAddr != "" {
		srv := &web.Server{
			StateDir: cfg.StateDir, LinearWebhookSecrets: secretLines(cfg.LinearHookKey),
			Labels: cfg.Labels, Mention: mentioned, Log: log,
			AgentSession: func(ev web.AgentSessionEvent) {
				dir := filepath.Join(cfg.StateDir, "sessions")
				if os.MkdirAll(dir, 0o700) == nil {
					_ = os.WriteFile(filepath.Join(dir, ev.SessionID+"-"+ev.Action+".json"), ev.Raw, 0o600)
				}
				job.EnqueueSession(cfg.StateDir, job.Session{ID: ev.SessionID, Action: ev.Action,
					IssueID: ev.IssueID, Issue: ev.Issue, CommentID: ev.CommentID, Request: ev.Request,
					Requester: ev.Requester, Received: time.Now().UTC()})
				// Linear marks a session unresponsive without an activity within 10 s.
				go func() {
					actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
					defer cancel()
					if err := r.Linear.AgentActivity(actx, ev.SessionID, "thought", "Received. Queued for the sandbox."); err != nil {
						log.Warn("cannot acknowledge agent session", "session", ev.SessionID, "err", err)
					}
				}()
			},
			Trigger: func() {
				select {
				case wake <- struct{}{}:
				default:
				}
			},
		}
		if cfg.AccessTeam != "" {
			srv.Access = &web.Access{Team: cfg.AccessTeam, AUD: cfg.AccessAUD}
		}
		go func() {
			if err := srv.ListenAndServe(ctx, cfg.WebAddr); err != nil {
				log.Error("http server", "err", err)
			}
		}()
		log.Info("http server", "addr", cfg.WebAddr, "linear_webhook", cfg.LinearHookKey != "",
			"access", cfg.AccessTeam != "")
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
			// A job runs on its own context: a stop signal (systemctl restart) lets the current job
			// finish instead of killing its sandbox halfway; the loop then exits. Its own timeouts
			// (AGENT_TIMEOUT, setup) still bound it.
			if ctx.Err() == nil {
				log.Info("job starting; a stop request now waits for it to finish", "issue", is.Identifier)
			}
			r.Handle(context.WithoutCancel(ctx), is)
		}
		for _, sess := range job.TakeSessions(cfg.StateDir) {
			if ctx.Err() != nil {
				job.EnqueueSession(cfg.StateDir, sess) // keep it for the next start
				continue
			}
			log.Info("agent session", "session", sess.ID, "action", sess.Action, "issue", sess.Issue)
			r.HandleSession(context.WithoutCancel(ctx), sess, overviewURL)
		}
		if mention != nil && ctx.Err() == nil {
			since := job.MentionWatermark(cfg.StateDir)
			if recent := time.Now().Add(-6 * time.Hour); since.Before(recent) {
				since = recent
			}
			words := strings.Fields(meName)
			comments, err := r.Linear.CommentsSince(ctx, since, words[len(words)-1])
			if err != nil {
				log.Error("poll Linear comments", "err", err)
			}
			for _, c := range comments {
				// A real mention also opens an agent session, handled above: give its webhook a
				// minute to arrive before treating the comment as a plain-text mention.
				if time.Since(c.CreatedAt) < time.Minute || job.MentionHandled(cfg.StateDir, c.ID) {
					continue
				}
				if ctx.Err() != nil || c.Issue == nil || (c.User != nil && c.User.ID == meID) ||
					!mentioned(c.Body) || !job.ClaimMention(cfg.StateDir, c.ID) {
					continue
				}
				r.HandleMention(context.WithoutCancel(ctx), c)
			}
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			log.Info("stopping")
			return
		case <-time.After(cfg.PollInterval):
		case <-wake:
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
	who, err := r.Linear.Viewer(ctx)
	if err != nil {
		return err
	}
	mode := "personal API key"
	if r.Linear.IsApp() {
		mode = "OAuth application (client credentials)"
	}
	fmt.Printf("Linear: acting as %q via %s\n", who, mode)
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

// secretLines splits a secrets file holding one secret per line.
func secretLines(s string) [][]byte {
	var out [][]byte
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, []byte(l))
		}
	}
	return out
}
