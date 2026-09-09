package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/gitops"
	"github.com/matbour/sandbox-agent/internal/linear"
)

// Outcome is what a job produced.
type Outcome struct {
	Decision Decision // what the agent said about its work
	PR       *github.PullRequest
	CostUSD  float64
	Turns    int
	Warnings []string
	Dir      string
}

// Handle claims the issue (removes the trigger label), runs it, and reports on the issue.
func (r *Runner) Handle(ctx context.Context, is linear.Issue) {
	var names, keep []string
	for _, l := range is.Labels {
		names = append(names, l.Name)
		if !r.Cfg.IsTrigger(l.Name) {
			keep = append(keep, l.ID)
		}
	}
	model, _ := r.Cfg.ModelFor(names)
	if err := r.Linear.SetLabels(ctx, is.ID, keep); err != nil {
		r.Log.Error("cannot remove trigger label, skipping", "issue", is.Identifier, "err", err)
		return
	}
	owner := ""
	if is.Assignee != nil {
		owner = is.Assignee.ID
	} else if is.Creator != nil {
		owner = is.Creator.ID
	}
	r.NewPR(ctx, is, model, commentReporter{lc: r.Linear, log: r.Log, Issue: is.ID}, true, owner)
}

// NewPR runs the issue and opens a draft pull request, reporting through rep. retryHint adds the
// label instructions to a failure message.
func (r *Runner) NewPR(ctx context.Context, is linear.Issue, model string, rep Reporter, retryHint bool, owner string) *Outcome {
	log := r.Log.With("issue", is.Identifier)
	ctx = withReporter(ctx, rep)
	rep.Start(ctx, fmt.Sprintf("picked up, working on it with `%s`.", model))
	log.Info("job started", "model", model)
	start := time.Now()
	out, err := r.Run(ctx, is, model)
	var f Finish
	if err == nil {
		f = r.finish(ctx, out.Dir, is, out.PR.HTMLURL, out.Decision, model, owner)
		out.CostUSD, out.Turns = out.CostUSD+f.CostUSD, out.Turns+f.Turns
	}
	took := time.Since(start).Round(time.Second)
	if out != nil {
		cleanWorkdir(out.Dir)
		update(out.Dir, func(st *State) {
			st.CostUSD, st.Turns = out.CostUSD, out.Turns
			if err != nil {
				st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
			} else {
				st.Stage, st.PR = StageDone, out.PR.HTMLURL
			}
		})
	}
	if err != nil && ctx.Err() != nil { // stopped from Linear: already answered there
		if out != nil {
			update(out.Dir, func(st *State) { st.Stage, st.Error = StageFailed, "stopped from Linear" })
		}
		log.Info("job stopped", "took", took)
		return out
	}
	var none *NoChangesError
	if errors.As(err, &none) {
		_, text := ParseDecision(none.Message)
		title, body := ParseSummary(text, "")
		msg := "**Needs a decision**: I made no changes. " + strings.TrimSpace(title+"\n\n"+body)
		if owner != "" {
			if aerr := r.Linear.Assign(ctx, is.ID, owner); aerr == nil {
				msg += "\n\nI assigned the issue back to you."
			}
		}
		if out != nil {
			update(out.Dir, func(st *State) { st.Stage, st.Error, st.Result = StageDone, "", "needs a decision" })
		}
		log.Info("job ended without changes", "took", took)
		rep.Ask(ctx, fmt.Sprintf("%s\n\nTook %s, `%s`, agent cost $%.2f.", msg, took, model, out.CostUSD))
		return out
	}
	if err != nil {
		log.Error("job failed", "err", err, "took", took)
		msg := fmt.Sprintf("failed after %s.\n\n```\n%s\n```", took, truncate(err.Error(), 3000))
		if out != nil && out.CostUSD > 0 {
			msg += fmt.Sprintf("\n\nAgent cost: $%.2f, %d turns.", out.CostUSD, out.Turns)
		}
		if retryHint {
			msg += fmt.Sprintf("\n\nAdd the `%s` label again to retry", r.Cfg.TriggerLabel)
			if retry, _ := r.Cfg.ModelFor([]string{r.Cfg.RetryLabel}); r.Cfg.RetryLabel != "" && retry != model {
				msg += fmt.Sprintf(", or `%s` to retry with `%s`", r.Cfg.RetryLabel, retry)
			}
			msg += ". Model labels: " + modelLabelList(r.Cfg) + "."
		}
		rep.Fail(ctx, msg)
		return out
	}
	log.Info("job done", "pr", out.PR.HTMLURL, "ready", f.Ready, "cost", out.CostUSD, "took", took)
	msg := fmt.Sprintf("%s\n\n%s\n\nTook %s, `%s`, agent cost $%.2f, %d turns.",
		f.Message, out.PR.HTMLURL, took, model, out.CostUSD, out.Turns)
	if f.Ready {
		rep.Done(ctx, msg)
	} else {
		rep.Ask(ctx, msg)
	}
	return out
}

// Run does the work: clone, sandbox, agent, validate, push, draft PR.
func (r *Runner) Run(ctx context.Context, is linear.Issue, model string) (*Outcome, error) {
	cfg := r.Cfg
	w, err := r.newWorkdir(is.Identifier, "")
	if err != nil {
		return nil, err
	}
	dir, repoDir, ws, slug, stamp := w.dir, w.repo, w.ws, w.slug, w.stamp
	out := &Outcome{Dir: dir}
	update(dir, func(st *State) {
		st.Issue, st.Title, st.URL, st.Model = is.Identifier, is.Title, is.URL, model
		st.PID, st.Started = os.Getpid(), time.Now().UTC()
	})
	stage(ctx, dir, StageCloning)

	owner, name, _ := strings.Cut(cfg.Repo, "/")
	tok, err := r.App.InstallationToken(ctx, name)
	if err != nil {
		return out, fmt.Errorf("GitHub token: %w", err)
	}
	repo := &github.Repo{Token: tok, Owner: owner, Name: name, HTTP: r.App.HTTP}
	base := cfg.BaseBranch
	if base == "" {
		if base, err = repo.DefaultBranch(ctx); err != nil {
			return out, err
		}
	}

	// 1. Clean clone on the host, exported without .git into the workspace.
	if err := gitops.Clone(ctx, repo.CloneURL(), base, repoDir, tok.Value); err != nil {
		return out, err
	}
	if err := prepare(ctx, repoDir, ws); err != nil {
		return out, err
	}

	ex, err := r.Execute(ctx, is, model, dir, repoDir, ws, slug, stamp)
	if ex != nil {
		out.CostUSD, out.Turns, out.Warnings = ex.CostUSD, ex.Turns, ex.Warnings
	}
	if err != nil {
		return out, err
	}
	g := &gitops.Git{Dir: repoDir, WorkTree: ws}

	// Commit, push a new branch, open a draft PR.
	stage(ctx, dir, StagePublishing)
	decision, result := ParseDecision(ex.agent.Result)
	out.Decision = decision
	title, body := ParseSummary(result, is.Title)
	msg := fmt.Sprintf("%s: %s\n\n%s\n", is.Identifier, title, is.URL)
	if cfg.AuthorEmail != "" {
		// Deploy platforms (Vercel) only build commits authored by a team member: commit as the
		// operator, and credit the agent in a trailer.
		g.Env = []string{
			"GIT_AUTHOR_NAME=" + cfg.AuthorName, "GIT_AUTHOR_EMAIL=" + cfg.AuthorEmail,
			"GIT_COMMITTER_NAME=" + cfg.AuthorName, "GIT_COMMITTER_EMAIL=" + cfg.AuthorEmail,
		}
		msg += "\nCo-authored-by: sandbox-agent <sandbox-agent@users.noreply.github.com>\n"
	}
	if _, err := g.Run(ctx, "commit", "--no-verify", "-q", "-m", msg); err != nil {
		return out, err
	}
	branch := "agent/" + slug + "-" + stamp
	pg := &gitops.Git{Dir: repoDir, Token: tok.Value}
	if _, err := pg.Run(ctx, "push", "--no-verify", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return out, err
	}
	prBody := prDescription(is, body, ex.Warnings, out)
	pr, err := repo.CreateDraftPR(ctx, branch, base, fmt.Sprintf("%s: %s", is.Identifier, title), prBody)
	if err != nil {
		return out, err
	}
	out.PR = pr
	return out, nil
}

func prDescription(is linear.Issue, body string, warnings []string, out *Outcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolves [%s](%s).\n\n", is.Identifier, is.URL)
	b.WriteString(body)
	if len(warnings) > 0 {
		b.WriteString("\n\n### Review carefully\n\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\n\n---\nOpened by sandbox-agent from a sandboxed Claude Code run (agent cost $%.2f, %d turns). "+
		"The description above is written by the agent: verify it against the diff.\n", out.CostUSD, out.Turns)
	return b.String()
}
