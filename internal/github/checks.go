package github

import (
	"context"
	"net/http"
	"strings"
)

// Check is the outcome of one CI check or commit status.
type Check struct {
	Name       string
	Conclusion string // success, failure, pending, ...
	Summary    string
}

// Checks returns the check runs and commit statuses for a commit.
func (r *Repo) Checks(ctx context.Context, sha string) ([]Check, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Output     struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	var status struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
		} `json:"statuses"`
	}
	if err := r.call(ctx, http.MethodGet, "/commits/"+sha+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	if err := r.call(ctx, http.MethodGet, "/commits/"+sha+"/status", nil, &status); err != nil {
		return nil, err
	}
	var out []Check
	for _, c := range runs.CheckRuns {
		concl := c.Conclusion
		if c.Status != "completed" {
			concl = c.Status
		}
		out = append(out, Check{Name: c.Name, Conclusion: concl, Summary: strings.TrimSpace(c.Output.Title + " " + c.Output.Summary)})
	}
	for _, s := range status.Statuses {
		out = append(out, Check{Name: s.Context, Conclusion: s.State, Summary: s.Description})
	}
	return out, nil
}
