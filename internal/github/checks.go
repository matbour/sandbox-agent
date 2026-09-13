package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// Check is the outcome of one CI check run or commit status.
type Check struct {
	Name       string
	Conclusion string // success, failure, pending, in_progress, ...
	Summary    string
}

// Checks returns the check runs and commit statuses of a commit, all pages.
func (r *Repo) Checks(ctx context.Context, sha string) ([]Check, error) {
	var out []Check

	opts := &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		runs, resp, err := r.gh().Checks.ListCheckRunsForRef(ctx, r.Owner, r.Name, sha, opts)
		if err != nil {
			return nil, fmt.Errorf("github: check runs: %w", err)
		}

		for _, c := range runs.CheckRuns {
			concl := c.GetConclusion()
			if c.GetStatus() != "completed" {
				concl = c.GetStatus()
			}

			out = append(out, Check{Name: c.GetName(), Conclusion: concl,
				Summary: strings.TrimSpace(c.GetOutput().GetTitle() + " " + c.GetOutput().GetSummary())})
		}

		if resp.NextPage == 0 {
			break
		}

		opts.Page = resp.NextPage
	}

	page := &gh.ListOptions{PerPage: 100}
	for {
		st, resp, err := r.gh().Repositories.GetCombinedStatus(ctx, r.Owner, r.Name, sha, page)
		if err != nil {
			return nil, fmt.Errorf("github: commit status: %w", err)
		}

		for _, s := range st.Statuses {
			out = append(out, Check{Name: s.GetContext(), Conclusion: s.GetState(), Summary: s.GetDescription()})
		}

		if resp.NextPage == 0 {
			break
		}

		page.Page = resp.NextPage
	}

	return out, nil
}
