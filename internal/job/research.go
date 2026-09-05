package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/gitops"
	"github.com/matbour/sandbox-agent/internal/linear"
)

// notesFile holds a research job's answer in its directory.
const notesFile = "notes.md"

// Research answers a question about an issue: the agent reads the code (and the web) in a
// sandbox, changes nothing, and its answer is posted as notes. The notes are given to later runs
// on the same issue as context.
func (r *Runner) Research(ctx context.Context, is linear.Issue, requester, request string, rep Reporter) {
	cfg := r.Cfg
	log := r.Log.With("issue", is.Identifier)
	ctx = withReporter(ctx, rep)
	model := cfg.AgentModel
	var words []string
	for _, w := range strings.Fields(request) {
		words = append(words, strings.Trim(w, "`.,;:!?()[]"))
	}
	if m, ok := cfg.ModelFor(words); ok {
		model = m
	}
	rep.Start(ctx, fmt.Sprintf("looking into it with `%s`: reading the code and, if needed, the docs.", model))
	log.Info("research started", "model", model)

	start := time.Now()
	stamp := start.UTC().Format("20060102-150405")
	slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(is.Identifier), "-"), "-")
	dir := filepath.Join(cfg.StateDir, "jobs", slug+"-research-"+stamp)
	repoDir, ws := filepath.Join(dir, "repo"), filepath.Join(dir, "workspace")
	notes, ex, err := func() (string, *Execution, error) {
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return "", nil, err
		}
		update(dir, func(st *State) {
			st.Issue, st.Title, st.URL, st.Model, st.Kind = is.Identifier, "research: "+is.Title, is.URL, model, "research"
			st.PID, st.Started = os.Getpid(), time.Now().UTC()
		})
		stage(ctx, dir, StageCloning)
		owner, name, _ := strings.Cut(cfg.Repo, "/")
		tok, err := r.App.InstallationToken(ctx, name)
		if err != nil {
			return "", nil, fmt.Errorf("GitHub token: %w", err)
		}
		repo := &github.Repo{Token: tok, Owner: owner, Name: name, HTTP: r.App.HTTP}
		base := cfg.BaseBranch
		if base == "" {
			if base, err = repo.DefaultBranch(ctx); err != nil {
				return "", nil, err
			}
		}
		if err := gitops.Clone(ctx, repo.CloneURL(), base, repoDir, tok.Value); err != nil {
			return "", nil, err
		}
		if err := prepare(ctx, repoDir, ws); err != nil {
			return "", nil, err
		}
		ex, err := r.execute(ctx, is, model, dir, repoDir, ws, slug, stamp, execOpts{readOnly: true,
			prompter: func(n string) string { return ResearchPrompt(is, requester, request, n) }})
		if err != nil {
			return "", ex, err
		}
		answer := strings.TrimSpace(ex.agent.Result)
		_ = os.WriteFile(filepath.Join(dir, notesFile), []byte(answer), 0o600)
		return answer, ex, nil
	}()
	_ = os.RemoveAll(repoDir)
	_ = os.RemoveAll(ws)
	took := time.Since(start).Round(time.Second)
	update(dir, func(st *State) {
		if ex != nil {
			st.CostUSD, st.Turns = ex.CostUSD, ex.Turns
		}
		if err != nil {
			st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
		} else {
			st.Stage, st.Result = StageDone, "research notes"
		}
	})
	if err != nil && ctx.Err() != nil {
		log.Info("research stopped")
		return
	}
	if err != nil {
		log.Error("research failed", "err", err, "took", took)
		rep.Fail(ctx, fmt.Sprintf("research failed after %s.\n\n```\n%s\n```\n\nMention me again to retry.",
			took, truncate(err.Error(), 3000)))
		return
	}
	log.Info("research done", "cost", ex.CostUSD, "took", took)
	rep.Done(ctx, fmt.Sprintf("%s\n\n---\n_Research notes, %s, `%s`, agent cost $%.2f. Assign the issue to me to implement it; I will start from these notes._",
		notes, took, model, ex.CostUSD))
}

// LastResearch returns the notes of the latest research job on an issue, if any.
func LastResearch(root, issue string) string {
	jobs, _ := Jobs(root)
	for _, j := range jobs {
		if j.Kind == "research" && strings.EqualFold(j.Issue, issue) && j.Stage == StageDone {
			if b, err := os.ReadFile(filepath.Join(j.Dir, notesFile)); err == nil {
				return truncate(string(b), 12000)
			}
		}
	}
	return ""
}
