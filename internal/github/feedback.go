package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// Feedback is what people and bots said on a pull request, oldest first within each kind.
type Feedback struct {
	Author string
	Kind   string // comment, review (state), review comment
	Path   string // file, for review comments
	Line   int
	Body   string
}

// PRFeedback collects conversation comments, review summaries and inline review comments, all
// pages.
func (r *Repo) PRFeedback(ctx context.Context, n int) ([]Feedback, error) {
	var out []Feedback

	page := gh.ListOptions{PerPage: 100}
	for {
		cs, resp, err := r.gh().Issues.ListComments(ctx, r.Owner, r.Name, n, &gh.IssueListCommentsOptions{ListOptions: page})
		if err != nil {
			return nil, fmt.Errorf("github: comments: %w", err)
		}

		for _, c := range cs {
			out = append(out, Feedback{Author: c.GetUser().GetLogin(), Kind: "comment", Body: c.GetBody()})
		}

		if resp.NextPage == 0 {
			break
		}

		page.Page = resp.NextPage
	}

	page = gh.ListOptions{PerPage: 100}
	for {
		rs, resp, err := r.gh().PullRequests.ListReviews(ctx, r.Owner, r.Name, n, &page)
		if err != nil {
			return nil, fmt.Errorf("github: reviews: %w", err)
		}

		for _, v := range rs {
			if strings.TrimSpace(v.GetBody()) != "" {
				out = append(out, Feedback{Author: v.GetUser().GetLogin(),
					Kind: "review (" + strings.ToLower(v.GetState()) + ")", Body: v.GetBody()})
			}
		}

		if resp.NextPage == 0 {
			break
		}

		page.Page = resp.NextPage
	}

	page = gh.ListOptions{PerPage: 100}
	for {
		cs, resp, err := r.gh().PullRequests.ListComments(ctx, r.Owner, r.Name, n,
			&gh.PullRequestListCommentsOptions{ListOptions: page})
		if err != nil {
			return nil, fmt.Errorf("github: review comments: %w", err)
		}

		for _, c := range cs {
			out = append(out, Feedback{Author: c.GetUser().GetLogin(), Kind: "review comment",
				Path: c.GetPath(), Line: c.GetLine(), Body: c.GetBody()})
		}

		if resp.NextPage == 0 {
			break
		}

		page.Page = resp.NextPage
	}

	return out, nil
}

// Comment posts a conversation comment on pull request (or issue) n.
func (r *Repo) Comment(ctx context.Context, n int, body string) error {
	if _, _, err := r.gh().Issues.CreateComment(ctx, r.Owner, r.Name, n, gh.IssueCommentRequest{Body: body}); err != nil {
		return fmt.Errorf("github: comment: %w", err)
	}

	return nil
}
