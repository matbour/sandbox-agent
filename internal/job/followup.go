package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/gitops"
	"github.com/matbour/sandbox-agent/internal/linear"
)

// MentionPattern matches a mention of the agent's Linear name in a comment ("@Sandbox Agent",
// "@sandbox-agent", or a Markdown mention link), with flexible spacing and case.
func MentionPattern(name string) *regexp.Regexp {
	words := strings.Fields(name)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)@\s*` + strings.Join(words, `[\s_-]*`) + `\b`)
}

// HandleMention updates the pull request of the comment's issue, replying in the comment's thread.
func (r *Runner) HandleMention(ctx context.Context, c linear.Comment) {
	requester := "someone"
	if c.User != nil {
		requester = c.User.Name
	}
	rep := commentReporter{lc: r.Linear, log: r.Log, Issue: c.Issue.ID, Parent: c.ID}
	owner := ""
	if c.User != nil {
		owner = c.User.ID
	}
	if !r.UpdatePR(ctx, *c.Issue, requester, c.Body, c.ID, rep, owner) {
		r.Research(ctx, *c.Issue, requester, c.Body, rep)
	}
}

// UpdatePR merges the latest base branch into the issue's agent pull request, runs the agent with
// the request, the PR feedback and the failed checks, and pushes to the same branch. Without a
// pull request it says so and returns false.
func (r *Runner) UpdatePR(ctx context.Context, is linear.Issue, requester, request, ref string, rep Reporter, owner string) bool {
	log := r.Log.With("issue", is.Identifier)
	ctx = withReporter(ctx, rep)
	prURL, model := LastPR(r.Cfg.StateDir, is.Identifier)
	if prURL == "" {
		return false
	}
	var words []string
	for _, w := range strings.Fields(request) {
		words = append(words, strings.Trim(w, "`.,;:!?()[]"))
	}
	if m, ok := r.Cfg.ModelFor(words); ok {
		model = m // an explicit model label in the request wins
	}
	if model == "" {
		model = r.Cfg.AgentModel
	}
	rep.Start(ctx, fmt.Sprintf("on it, updating %s with `%s` (latest `main` merged first).", prURL, model))
	log.Info("update started", "pr", prURL, "model", model)
	start := time.Now()
	out, err := r.runUpdate(ctx, is, requester, request, ref, prURL, model)
	if err != nil && ctx.Err() != nil { // stopped from Linear: already answered there
		log.Info("update stopped")
		return true
	}
	if err != nil {
		took := time.Since(start).Round(time.Second)
		log.Error("update failed", "err", err, "took", took)
		msg := fmt.Sprintf("update failed after %s.\n\n```\n%s\n```", took, truncate(err.Error(), 3000))
		if out != nil && out.CostUSD > 0 {
			msg += fmt.Sprintf("\n\nAgent cost: $%.2f, %d turns.", out.CostUSD, out.Turns)
		}
		rep.Fail(ctx, msg+"\n\nMention me again to retry.")
		return true
	}
	cost, turns := out.CostUSD, out.Turns
	f := r.finish(ctx, out.Dir, is, prURL, out.Decision, model, owner)
	cost, turns = cost+f.CostUSD, turns+f.Turns
	took := time.Since(start).Round(time.Second)
	log.Info("update done", "pr", prURL, "ready", f.Ready, "cost", cost, "took", took)
	msg := fmt.Sprintf("%s\n\n%s\n\n%s\n\nTook %s, `%s`, agent cost $%.2f, %d turns.",
		out.Summary, f.Message, prURL, took, model, cost, turns)
	if f.Ready {
		rep.Done(ctx, msg)
	} else {
		rep.Ask(ctx, msg)
	}
	return true
}

// runUpdate runs one update pass and records its outcome in the state.
func (r *Runner) runUpdate(ctx context.Context, is linear.Issue, requester, request, ref, prURL, model string) (*UpdateOutcome, error) {
	out, err := r.followUp(ctx, is, requester, request, ref, prURL, model)
	if out != nil {
		_ = os.RemoveAll(filepath.Join(out.Dir, "repo"))
		_ = os.RemoveAll(filepath.Join(out.Dir, "workspace"))
		update(out.Dir, func(st *State) {
			st.CostUSD, st.Turns = out.CostUSD, out.Turns
			if err != nil {
				st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
			} else {
				st.Stage, st.PR = StageDone, prURL
			}
		})
	}
	return out, err
}

// UpdateOutcome is what an update produced.
type UpdateOutcome struct {
	Outcome
	Summary string // one line for the Linear reply
}

func (r *Runner) followUp(ctx context.Context, is linear.Issue, requester, request, ref, prURL, model string) (*UpdateOutcome, error) {
	cfg := r.Cfg
	stamp := time.Now().UTC().Format("20060102-150405")
	slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(is.Identifier), "-"), "-")
	dir := filepath.Join(cfg.StateDir, "jobs", slug+"-update-"+stamp)
	repoDir, ws := filepath.Join(dir, "repo"), filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return nil, err
	}
	out := &UpdateOutcome{Outcome: Outcome{Dir: dir}}
	update(dir, func(st *State) {
		st.Issue, st.Title, st.URL, st.Model = is.Identifier, "update: "+is.Title, is.URL, model
		st.Kind, st.Comment = "update", ref
		st.PID, st.Started = os.Getpid(), time.Now().UTC()
	})
	stage(ctx, dir, StageCloning)

	n, ok := github.PRNumber(prURL)
	if !ok {
		return out, fmt.Errorf("cannot read the pull request number from %s", prURL)
	}
	owner, name, _ := strings.Cut(cfg.Repo, "/")
	tok, err := r.App.InstallationToken(ctx, name)
	if err != nil {
		return out, fmt.Errorf("GitHub token: %w", err)
	}
	repo := &github.Repo{Token: tok, Owner: owner, Name: name, HTTP: r.App.HTTP}
	pr, err := repo.GetPR(ctx, n)
	if err != nil {
		return out, err
	}
	if pr.State != "open" {
		state := "closed"
		if pr.Merged {
			state = "merged"
		}
		return out, fmt.Errorf("pull request #%d is %s; add the `%s` label for a new one", n, state, cfg.TriggerLabel)
	}
	if !strings.HasPrefix(pr.Head.Ref, "agent/") {
		return out, fmt.Errorf("branch %s was not created by the agent; refusing to push to it", pr.Head.Ref)
	}
	feedback, err := repo.PRFeedback(ctx, n)
	if err != nil {
		return out, err
	}
	checks, err := repo.Checks(ctx, pr.Head.SHA)
	if err != nil {
		r.Log.Warn("cannot read checks", "err", err)
	}

	// The PR branch, with the latest base merged in on the host (trusted content). Conflicts are
	// committed with their markers to a local commit only, for the agent to resolve.
	g := &gitops.Git{Dir: repoDir, Token: tok.Value, Env: r.authorEnv()}
	if err := gitops.CloneForUpdate(ctx, repo.CloneURL(), pr.Head.Ref, pr.Base.Ref, repoDir, tok.Value); err != nil {
		return out, err
	}
	if head, err := g.Run(ctx, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(head) != pr.Head.SHA {
		return out, fmt.Errorf("branch %s moved while cloning; mention me again", pr.Head.Ref)
	}
	merged, conflicts, err := gitops.MergeBase(ctx, g, pr.Base.Ref)
	if err != nil {
		return out, err
	}
	if err := prepare(ctx, repoDir, ws); err != nil {
		return out, err
	}

	prompter := func(notes string) string {
		return UpdatePrompt(is, requester, request, pr, feedback, checks, conflicts, notes)
	}
	ex, err := r.execute(ctx, is, model, dir, repoDir, ws, slug, stamp, execOpts{prompter: prompter, allowEmpty: true})
	if ex != nil {
		out.CostUSD, out.Turns, out.Warnings = ex.CostUSD, ex.Turns, ex.Warnings
	}
	if err != nil {
		return out, err
	}
	if err := gitops.CheckNoMarkers(ws, conflicts); err != nil {
		return out, err
	}

	// Commit: resolutions amend the local merge commit (markers never reach the branch); other
	// changes go in their own commit on top.
	stage(ctx, dir, StagePublishing)
	decision, result := ParseDecision(ex.agent.Result)
	out.Decision = decision
	title, body := ParseSummary(result, "Address feedback")
	gw := &gitops.Git{Dir: repoDir, WorkTree: ws, Env: r.authorEnv()}
	trailer := ""
	if cfg.AuthorEmail != "" {
		trailer = "\nCo-authored-by: sandbox-agent <sandbox-agent@users.noreply.github.com>\n"
	}
	switch {
	case len(conflicts) > 0:
		msg := fmt.Sprintf("Merge %s into %s\n\nConflicts resolved by the agent in: %s\n%s",
			pr.Base.Ref, pr.Head.Ref, strings.Join(conflicts, ", "), trailer)
		if _, err := gw.Run(ctx, "commit", "--no-verify", "-q", "--amend", "-m", msg); err != nil {
			return out, err
		}
	case ex.Changed > 0:
		msg := fmt.Sprintf("%s: %s\n\n%s\n%s", is.Identifier, title, is.URL, trailer)
		if _, err := gw.Run(ctx, "commit", "--no-verify", "-q", "-m", msg); err != nil {
			return out, err
		}
	}
	if !merged && ex.Changed == 0 {
		out.Summary = "nothing to change, and the branch was already up to date with `" + pr.Base.Ref + "`. " +
			firstSentence(ex.agent.Result)
		return out, nil
	}
	// Fast-forward only: fails if someone pushed to the branch meanwhile.
	if _, err := g.Run(ctx, "push", "--no-verify", "origin", "HEAD:refs/heads/"+pr.Head.Ref); err != nil {
		return out, fmt.Errorf("push to %s: %w", pr.Head.Ref, err)
	}
	var did []string
	if merged {
		s := "merged the latest `" + pr.Base.Ref + "`"
		if len(conflicts) > 0 {
			s += fmt.Sprintf(" and resolved conflicts in %d file(s)", len(conflicts))
		}
		did = append(did, s)
	}
	if ex.Changed > 0 {
		did = append(did, fmt.Sprintf("changed %d file(s): %s", ex.Changed, title))
	}
	out.Summary = "updated the pull request: " + strings.Join(did, "; ") + "."
	prMsg := fmt.Sprintf("Update requested by %s in [%s](%s):\n\n%s\n\n%s", requester, is.Identifier, is.URL,
		quote(request), body)
	if len(ex.Warnings) > 0 {
		prMsg += "\n\n### Review carefully\n\n- " + strings.Join(ex.Warnings, "\n- ")
	}
	prMsg += fmt.Sprintf("\n\n---\nsandbox-agent, `%s`, agent cost $%.2f, %d turns.", model, out.CostUSD, out.Turns)
	if err := repo.Comment(ctx, n, prMsg); err != nil {
		r.Log.Warn("cannot comment on the pull request", "err", err)
	}
	return out, nil
}

func (r *Runner) authorEnv() []string {
	if r.Cfg.AuthorEmail == "" {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + r.Cfg.AuthorName, "GIT_AUTHOR_EMAIL=" + r.Cfg.AuthorEmail,
		"GIT_COMMITTER_NAME=" + r.Cfg.AuthorName, "GIT_COMMITTER_EMAIL=" + r.Cfg.AuthorEmail,
	}
}

func quote(s string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 && i < 300 {
		return s[:i+1]
	}
	return truncate(s, 300)
}
