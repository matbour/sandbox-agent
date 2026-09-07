package github

import (
	"context"
	"fmt"
	"net/http"
)

// DefaultBranch returns the repository's default branch.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	err := r.call(ctx, http.MethodGet, "", nil, &out)
	return out.DefaultBranch, err
}

// CloneURL is the HTTPS URL; credentials are passed through Git config, never in the URL.
func (r *Repo) CloneURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", r.Owner, r.Name)
}
