package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// PullRequest is a newly opened pull request.
type PullRequest struct {
	Number  int
	NodeID  string
	HTMLURL string
}

// CreateDraftPR opens a draft pull request from head into base.
func (r *Repo) CreateDraftPR(ctx context.Context, head, base, title, body string) (*PullRequest, error) {
	pr, _, err := r.gh().PullRequests.Create(ctx, r.Owner, r.Name, gh.CreatePullRequest{
		Title: new(title), Head: head, Base: base, Body: new(body), Draft: new(true),
	})
	if err != nil {
		return nil, fmt.Errorf("github: create pull request: %w", err)
	}

	return &PullRequest{Number: pr.GetNumber(), NodeID: pr.GetNodeID(), HTMLURL: pr.GetHTMLURL()}, nil
}

// PR is the subset of an existing pull request the worker uses.
type PR struct {
	Number  int
	NodeID  string
	Draft   bool
	State   string // open or closed
	Merged  bool
	HTMLURL string
	Head    struct{ Ref, SHA string }
	Base    struct{ Ref string }
}

// GetPR returns pull request n.
func (r *Repo) GetPR(ctx context.Context, n int) (*PR, error) {
	p, _, err := r.gh().PullRequests.Get(ctx, r.Owner, r.Name, n)
	if err != nil {
		return nil, fmt.Errorf("github: pull request #%d: %w", n, err)
	}

	out := &PR{Number: p.GetNumber(), NodeID: p.GetNodeID(), Draft: p.GetDraft(), State: p.GetState(),
		Merged: p.GetMerged(), HTMLURL: p.GetHTMLURL()}
	out.Head.Ref, out.Head.SHA = p.GetHead().GetRef(), p.GetHead().GetSHA()
	out.Base.Ref = p.GetBase().GetRef()

	return out, nil
}

// MarkReady takes a draft pull request out of draft. REST cannot; GraphQL can.
func (r *Repo) MarkReady(ctx context.Context, nodeID string) error {
	req, err := r.gh().NewRequest(ctx, "POST", "graphql", map[string]any{
		"query":     `mutation($id: ID!) { markPullRequestReadyForReview(input: { pullRequestId: $id }) { pullRequest { isDraft } } }`,
		"variables": map[string]string{"id": nodeID},
	})
	if err != nil {
		return err
	}

	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := r.gh().Do(req, &out); err != nil {
		return fmt.Errorf("github: mark ready: %w", err)
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
