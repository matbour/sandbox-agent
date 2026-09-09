package linear

import (
	"context"
	"fmt"
)

// IssuesWithLabel returns open issues carrying any of the labels, optionally restricted to team
// keys.
func (c *Client) IssuesWithLabel(ctx context.Context, labels []string, teams []string) ([]Issue, error) {
	var anyOf []map[string]any
	for _, l := range labels {
		anyOf = append(anyOf, map[string]any{
			"labels": map[string]any{"some": map[string]any{"name": map[string]any{"eqIgnoreCase": l}}}})
	}
	filter := map[string]any{
		"or":    anyOf,
		"state": map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}},
	}
	if len(teams) > 0 {
		filter["team"] = map[string]any{"key": map[string]any{"in": teams}}
	}
	r, err := IssuesWithLabel(ctx, c.client(), &filter)
	if err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(r.Issues.Nodes))
	for i := range r.Issues.Nodes {
		out = append(out, issueFrom(&r.Issues.Nodes[i].IssueFields))
	}
	return out, nil
}

// IssueByID returns an issue by id or identifier (ENG-750).
func (c *Client) IssueByID(ctx context.Context, id string) (*Issue, error) {
	r, err := IssueByID(ctx, c.client(), id) // the schema's issue(id) is non-null: not found is an error
	if err != nil {
		return nil, fmt.Errorf("linear: issue %s: %w", id, err)
	}
	is := issueFrom(&r.Issue.IssueFields)
	return &is, nil
}

// SetLabels replaces the issue's label set.
func (c *Client) SetLabels(ctx context.Context, issueID string, labelIDs []string) error {
	if labelIDs == nil {
		labelIDs = []string{}
	}
	_, err := SetLabels(ctx, c.client(), issueID, labelIDs)
	return err
}

// Assign sets the issue's assignee.
func (c *Client) Assign(ctx context.Context, issueID, userID string) error {
	_, err := Assign(ctx, c.client(), issueID, &userID)
	return err
}
