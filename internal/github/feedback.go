package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Feedback is what people and bots said on a pull request, oldest first.
type Feedback struct {
	Author string
	Kind   string // comment, review, review comment
	Path   string // file, for review comments
	Line   int
	Body   string
}

// PRFeedback collects conversation comments, review summaries and inline review comments.
func (r *Repo) PRFeedback(ctx context.Context, n int) ([]Feedback, error) {
	type user struct {
		Login string `json:"login"`
	}
	var issueComments []struct {
		User user   `json:"user"`
		Body string `json:"body"`
	}
	var reviews []struct {
		User  user   `json:"user"`
		Body  string `json:"body"`
		State string `json:"state"`
	}
	var inline []struct {
		User user   `json:"user"`
		Body string `json:"body"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}
	if err := r.call(ctx, http.MethodGet, fmt.Sprintf("/issues/%d/comments?per_page=100", n), nil, &issueComments); err != nil {
		return nil, err
	}
	if err := r.call(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews?per_page=100", n), nil, &reviews); err != nil {
		return nil, err
	}
	if err := r.call(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d/comments?per_page=100", n), nil, &inline); err != nil {
		return nil, err
	}
	var out []Feedback
	for _, c := range issueComments {
		out = append(out, Feedback{Author: c.User.Login, Kind: "comment", Body: c.Body})
	}
	for _, v := range reviews {
		if strings.TrimSpace(v.Body) != "" {
			out = append(out, Feedback{Author: v.User.Login, Kind: "review (" + strings.ToLower(v.State) + ")", Body: v.Body})
		}
	}
	for _, c := range inline {
		out = append(out, Feedback{Author: c.User.Login, Kind: "review comment", Path: c.Path, Line: c.Line, Body: c.Body})
	}
	return out, nil
}

// Comment posts a conversation comment on pull request (or issue) n.
func (r *Repo) Comment(ctx context.Context, n int, body string) error {
	return r.call(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", n), map[string]string{"body": body}, nil)
}
