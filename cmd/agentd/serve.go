package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/job"
	"github.com/matbour/sandbox-agent/internal/web"
)

// daemon is the long-running service: it polls Linear, serves webhooks and the overview, and runs
// one job at a time.
type daemon struct {
	cfg *config.Config
	r   *job.Runner
	log *slog.Logger

	meID, meName string         // the agent's Linear identity
	mention      *regexp.Regexp // nil: mentions disabled
	wake         chan struct{}  // a webhook asks for a poll now
}

// serve runs the service until ctx is cancelled (or after one pass with once). A job runs on its
// own context: a stop request lets the current job finish, then the loop exits.
func serve(ctx context.Context, r *job.Runner, once bool) error {
	d := &daemon{cfg: r.Cfg, r: r, log: r.Log, wake: make(chan struct{}, 1)}
	if n, err := job.Import(d.cfg.StateDir); err != nil {
		d.log.Warn("import old job status files", "err", err)
	} else if n > 0 {
		d.log.Info("imported old job status files", "jobs", n)
	}
	d.initMentions(ctx)
	if d.cfg.WebAddr != "" {
		d.startHTTP(ctx)
	}
	d.log.Info("agentd started", "sandbox", r.Sandbox.Name(), "repo", d.cfg.Repo, "labels", d.cfg.Labels(),
		"default_model", d.cfg.AgentModel, "teams", d.cfg.TeamKeys, "every", d.cfg.PollInterval)
	for {
		d.runLabelledIssues(ctx)
		d.runSessions(ctx)
		d.runMentions(ctx)
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			d.log.Info("stopping")
			return nil
		case <-time.After(d.cfg.PollInterval):
		case <-d.wake:
		}
	}
}

// initMentions reads the agent's Linear identity, needed to recognise mentions and its own
// comments. Without it, mentions are disabled but the rest works.
func (d *daemon) initMentions(ctx context.Context) {
	me, err := d.r.Linear.Me(ctx)
	if err != nil {
		d.log.Warn("cannot read the Linear identity; mentions disabled", "err", err)
		return
	}
	d.meID, d.meName, d.mention = me.ID, me.Name, job.MentionPattern(me.Name)
	d.log.Info("mentions enabled", "as", "@"+me.Name, "since", job.MentionWatermark(d.cfg.StateDir))
}

func (d *daemon) mentioned(body string) bool { return d.mention != nil && d.mention.MatchString(body) }

// poke asks the loop to poll now, without blocking.
func (d *daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// runLabelledIssues runs the issues carrying a trigger label.
func (d *daemon) runLabelledIssues(ctx context.Context) {
	issues, err := d.r.Linear.IssuesWithLabel(ctx, d.cfg.Labels(), d.cfg.TeamKeys)
	if err != nil {
		d.log.Error("poll Linear", "err", err)
	}
	for _, is := range issues {
		if ctx.Err() != nil {
			return
		}
		d.log.Info("job starting; a stop request now waits for it to finish", "issue", is.Identifier)
		d.r.Handle(context.WithoutCancel(ctx), is)
	}
}

// runSessions handles the queued agent sessions, one at a time; the rest stay visible in the queue.
func (d *daemon) runSessions(ctx context.Context) {
	for ctx.Err() == nil {
		sess, ok := job.TakeSession(d.cfg.StateDir)
		if !ok {
			return
		}
		d.log.Info("agent session", "session", sess.ID, "action", sess.Action, "issue", sess.Issue)
		d.r.HandleSession(context.WithoutCancel(ctx), sess, d.cfg.OverviewURL, d.meID)
	}
}

// runMentions handles comments that mention the agent in plain text (the fallback when no agent
// session was opened for them).
func (d *daemon) runMentions(ctx context.Context) {
	if d.mention == nil || ctx.Err() != nil {
		return
	}
	since := job.MentionWatermark(d.cfg.StateDir)
	if recent := time.Now().Add(-6 * time.Hour); since.Before(recent) {
		since = recent
	}
	words := strings.Fields(d.meName)
	comments, err := d.r.Linear.CommentsSince(ctx, since, words[len(words)-1])
	if err != nil {
		d.log.Error("poll Linear comments", "err", err)
	}
	for _, c := range comments {
		// A real mention also opens an agent session, handled above: give its webhook a minute to
		// arrive before treating the comment as a plain-text mention.
		if time.Since(c.CreatedAt) < time.Minute || job.MentionHandled(d.cfg.StateDir, c.ID) {
			continue
		}
		if ctx.Err() != nil || c.Issue == nil || (c.Author != nil && c.Author.ID == d.meID) ||
			!d.mentioned(c.Body) || !job.ClaimMention(d.cfg.StateDir, c.ID) {
			continue
		}
		d.r.HandleMention(context.WithoutCancel(ctx), c)
	}
}

// startHTTP serves the Linear webhook and the overview in the background.
func (d *daemon) startHTTP(ctx context.Context) {
	srv := &web.Server{
		StateDir: d.cfg.StateDir, LinearWebhookSecrets: secretLines(d.cfg.LinearHookKey),
		Labels: d.cfg.Labels, Mention: d.mentioned, Log: d.log, Trigger: d.poke,
		AgentSession: func(ev web.AgentSessionEvent) { d.onAgentSession(ctx, ev) },
	}
	if d.cfg.AccessTeam != "" {
		srv.Access = &web.Access{Team: d.cfg.AccessTeam, AUD: d.cfg.AccessAUD}
	}
	go func() {
		if err := srv.ListenAndServe(ctx, d.cfg.WebAddr); err != nil {
			d.log.Error("http server", "err", err)
		}
	}()
	d.log.Info("http server", "addr", d.cfg.WebAddr, "linear_webhook", d.cfg.LinearHookKey != "",
		"access", d.cfg.AccessTeam != "")
}

// onAgentSession is called by the webhook for each agent session event. It must return quickly:
// it queues the session (or stops it), and acknowledges it in Linear in the background.
func (d *daemon) onAgentSession(ctx context.Context, ev web.AgentSessionEvent) {
	dir := filepath.Join(d.cfg.StateDir, "sessions")
	if os.MkdirAll(dir, 0o700) == nil {
		_ = os.WriteFile(filepath.Join(dir, ev.SessionID+"-"+ev.Action+".json"), ev.Raw, 0o600)
	}
	if ev.Signal == "stop" {
		stopped := job.StopSession(ev.SessionID)
		dropped := job.DropSessions(d.cfg.StateDir, ev.SessionID)
		d.log.Info("agent session stopped", "session", ev.SessionID, "job_cancelled", stopped, "queued_dropped", dropped)
		go d.activity(ctx, ev.SessionID, "response", "Stopped. Nothing was pushed.")
		return
	}
	job.EnqueueSession(d.cfg.StateDir, job.Session{ID: ev.SessionID, Action: ev.Action,
		IssueID: ev.IssueID, Issue: ev.Issue, CommentID: ev.CommentID, Request: ev.Request,
		Requester: ev.Requester, RequesterID: ev.RequesterID, Received: time.Now().UTC()})
	// Linear marks a session unresponsive without an activity within 10 s.
	go d.activity(ctx, ev.SessionID, "thought", "Received. Queued for the sandbox.")
}

// activity posts a session activity, bounded so a slow Linear does not pile up goroutines.
func (d *daemon) activity(ctx context.Context, session, kind, body string) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if err := d.r.Linear.AgentActivity(actx, session, kind, body); err != nil {
		d.log.Warn("cannot post agent activity", "session", session, "kind", kind, "err", err)
	}
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
