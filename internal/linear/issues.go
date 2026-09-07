package linear

import (
	"context"
	"fmt"
)

// Label is an issue label.
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Person is a Linear user.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Issue is the subset of a Linear issue the worker uses.
type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Team        struct {
		Key string `json:"key"`
	} `json:"team"`
	Creator  *Person `json:"creator"`
	Assignee *Person `json:"assignee"`
	Delegate *Person `json:"delegate"` // an agent the issue is delegated to
	Labels   struct {
		Nodes []Label `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		Nodes []struct {
			Body string `json:"body"`
			User *struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"nodes"`
	} `json:"comments"`
}

const issueFields = `id identifier title description url team { key } creator { id name } assignee { id name } delegate { id name }
labels { nodes { id name } }
comments(first: 50) { nodes { body user { name } } }`

// IssuesWithLabel returns open issues carrying any of the labels, optionally restricted to team keys.
func (c *Client) IssuesWithLabel(ctx context.Context, labels []string, teams []string) ([]Issue, error) {
	var clauses []map[string]any
	for _, l := range labels {
		clauses = append(clauses, map[string]any{
			"labels": map[string]any{"some": map[string]any{"name": map[string]any{"eqIgnoreCase": l}}}})
	}
	filter := map[string]any{
		"or":    clauses,
		"state": map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}},
	}
	if len(teams) > 0 {
		filter["team"] = map[string]any{"key": map[string]any{"in": teams}}
	}
	var out struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	q := `query($filter: IssueFilter) { issues(filter: $filter, first: 20) { nodes { ` + issueFields + ` } } }`
	if err := c.do(ctx, q, map[string]any{"filter": filter}, &out); err != nil {
		return nil, err
	}
	return out.Issues.Nodes, nil
}

// IssueByID returns an issue with the fields the worker uses.
func (c *Client) IssueByID(ctx context.Context, id string) (*Issue, error) {
	var out struct {
		Issue *Issue `json:"issue"`
	}
	err := c.do(ctx, `query($id: String!) { issue(id: $id) { `+issueFields+` } }`, map[string]any{"id": id}, &out)
	if err == nil && out.Issue == nil {
		err = fmt.Errorf("linear: issue %s not found", id)
	}
	return out.Issue, err
}

// SetLabels replaces the issue's label set.
func (c *Client) SetLabels(ctx context.Context, issueID string, labelIDs []string) error {
	if labelIDs == nil {
		labelIDs = []string{}
	}
	q := `mutation($id: String!, $labels: [String!]) {
  issueUpdate(id: $id, input: { labelIds: $labels }) { success } }`
	return c.do(ctx, q, map[string]any{"id": issueID, "labels": labelIDs}, nil)
}

// Assign sets the issue's assignee.
func (c *Client) Assign(ctx context.Context, issueID, userID string) error {
	q := `mutation($id: String!, $u: String) { issueUpdate(id: $id, input: { assigneeId: $u }) { success } }`
	return c.do(ctx, q, map[string]any{"id": issueID, "u": userID}, nil)
}
