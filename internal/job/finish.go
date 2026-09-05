package job

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/linear"
)

// Finish is how a job ends once its pull request is pushed.
type Finish struct {
	Ready   bool   // the pull request was marked ready for review
	Message string // for the person, in Linear
	CostUSD float64
	Turns   int // of the fix passes
}

// finish waits for the pull request's checks, runs fix passes while they fail (up to
// CI_FIX_ATTEMPTS), then marks the pull request ready for review, or leaves it as a draft and
// hands the issue back to owner with the question. Linear's GitHub integration moves the issue to
// In Review when the pull request becomes ready.
func (r *Runner) finish(ctx context.Context, dir string, is linear.Issue, prURL string, d Decision,
	model, owner string) (f Finish) {
	defer func() {
		update(dir, func(st *State) {
			if f.Ready {
				st.Result = "ready for review"
			} else {
				st.Result = "needs a decision"
			}
		})
	}()
	handBack := func(why string) Finish {
		f.Ready = false
		f.Message = "**Needs a decision** (the pull request stays a draft): " + why
		if owner != "" {
			if err := r.Linear.Assign(ctx, is.ID, owner); err != nil {
				r.Log.Warn("cannot assign the issue back", "issue", is.Identifier, "err", err)
			} else {
				f.Message += "\n\nI assigned the issue back to you."
			}
		}
		return f
	}
	if !d.Ready {
		return handBack(d.Question)
	}
	n, ok := github.PRNumber(prURL)
	if !ok {
		return handBack("cannot read the pull request number from " + prURL)
	}
	owner_, name, _ := strings.Cut(r.Cfg.Repo, "/")
	for attempt := 0; ; attempt++ {
		stage(ctx, dir, StageChecks)
		tok, err := r.App.InstallationToken(ctx, name)
		if err != nil {
			return handBack("GitHub token: " + err.Error())
		}
		repo := &github.Repo{Token: tok, Owner: owner_, Name: name, HTTP: r.App.HTTP}
		failed, err := r.waitChecks(ctx, repo, n)
		if err != nil {
			return handBack(err.Error())
		}
		if len(failed) == 0 {
			pr, err := repo.GetPR(ctx, n)
			if err == nil && pr.Draft {
				err = repo.MarkReady(ctx, pr.NodeID)
			}
			if err != nil {
				return handBack("the checks pass, but marking the pull request ready failed: " + err.Error())
			}
			f.Ready = true
			f.Message = "**Ready for review**: the checks pass and the pull request is out of draft."
			if attempt > 0 {
				f.Message += fmt.Sprintf(" It took %d fix pass(es).", attempt)
			}
			return f
		}
		if attempt >= r.Cfg.CIFixAttempts {
			return handBack(fmt.Sprintf("the checks still fail after %d fix pass(es):\n\n- %s",
				attempt, strings.Join(failed, "\n- ")))
		}
		if rep, ok := ctx.Value(reporterKey{}).(Reporter); ok {
			rep.Progress(ctx, "Checks failed, starting a fix pass:\n\n- "+strings.Join(failed, "\n- "))
		}
		request := "The checks on this pull request failed. Find the cause and fix it, without " +
			"changing what the pull request does:\n\n- " + strings.Join(failed, "\n- ")
		out, err := r.runUpdate(ctx, is, "the CI", request, fmt.Sprintf("checks-%d", attempt+1), prURL, model)
		if out != nil {
			f.CostUSD, f.Turns = f.CostUSD+out.CostUSD, f.Turns+out.Turns
		}
		if err != nil {
			return handBack("the fix pass failed: " + err.Error())
		}
		if !out.Decision.Ready {
			return handBack(out.Decision.Question)
		}
	}
}

// waitChecks waits until every check on the pull request's head has finished, and returns the
// failed ones. Checks named in IGNORE_CHECKS are skipped. It gives up after CI_TIMEOUT.
func (r *Runner) waitChecks(ctx context.Context, repo *github.Repo, n int) ([]string, error) {
	start := time.Now()
	for {
		pr, err := repo.GetPR(ctx, n)
		if err != nil {
			return nil, err
		}
		checks, err := repo.Checks(ctx, pr.Head.SHA)
		if err != nil {
			return nil, err
		}
		failed, pending, seen := classify(checks, r.Cfg.IgnoreChecks)
		// Checks take a little while to register after a push.
		settled := pending == 0 && (seen > 0 || time.Since(start) > 3*time.Minute)
		if settled {
			return failed, nil
		}
		if time.Since(start) > r.Cfg.CIWait {
			return nil, fmt.Errorf("the checks did not finish within %s (%d still running)", r.Cfg.CIWait, pending)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

func classify(checks []github.Check, ignore []string) (failed []string, pending, seen int) {
	for _, c := range checks {
		skip := false
		for _, i := range ignore {
			if i != "" && strings.Contains(strings.ToLower(c.Name), strings.ToLower(i)) {
				skip = true
			}
		}
		if skip {
			continue
		}
		seen++
		switch c.Conclusion {
		case "success", "neutral", "skipped", "cancelled", "stale":
		case "failure", "error", "timed_out", "action_required":
			line := c.Name
			if s := strings.Join(strings.Fields(c.Summary), " "); s != "" {
				line += ": " + truncate(s, 300)
			}
			failed = append(failed, line)
		default: // pending, queued, in_progress, waiting, requested, ""
			pending++
		}
	}
	return failed, pending, seen
}
