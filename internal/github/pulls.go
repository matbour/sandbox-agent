package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// PullRequest is the subset of the API response the worker uses.
type PullRequest struct {
	Number  int    `json:"number"`
	NodeID  string `json:"node_id"`
	HTMLURL string `json:"html_url"`
}

// CreateDraftPR opens a draft pull request from head into base.
func (r *Repo) CreateDraftPR(ctx context.Context, head, base, title, body string) (*PullRequest, error) {
	var pr PullRequest
	err := r.call(ctx, http.MethodPost, "/pulls", map[string]any{
		"head": head, "base": base, "title": title, "body": body, "draft": true,
	}, &pr)
	return &pr, err
}

// PR is the subset of a pull request the worker uses.
type PR struct {
	Number  int    `json:"number"`
	NodeID  string `json:"node_id"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// GetPR returns pull request n.
func (r *Repo) GetPR(ctx context.Context, n int) (*PR, error) {
	var pr PR
	err := r.call(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d", n), nil, &pr)
	return &pr, err
}

// MarkReady takes a draft pull request out of draft. The REST API cannot do it; GraphQL can.
func (r *Repo) MarkReady(ctx context.Context, nodeID string) error {
	body := map[string]any{
		"query":     `mutation($id: ID!) { markPullRequestReadyForReview(input: { pullRequestId: $id }) { pullRequest { isDraft } } }`,
		"variables": map[string]string{"id": nodeID},
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	a := &App{HTTP: r.HTTP}
	if err := a.call(ctx, http.MethodPost, "/graphql", "token "+r.Token.Value, body, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("github: mark ready: %s", out.Errors[0].Message)
	}
	return nil
}

// PRNumber extracts the number from a pull request URL (…/pull/123).
func PRNumber(url string) (int, bool) {
	i := strings.LastIndex(url, "/pull/")
	if i < 0 {
		return 0, false
	}
	n := 0
	for _, c := range url[i+len("/pull/"):] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}
