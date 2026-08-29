// Package linear is a minimal client for the Linear GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const endpoint = "https://api.linear.app/graphql"

// Client talks to Linear with a personal API key.
type Client struct {
	APIKey string
	HTTP   *http.Client
}

// New returns a client with a sane timeout.
func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Label is an issue label.
type Label struct {
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
	Labels struct {
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

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("linear: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

const issueFields = `id identifier title description url team { key }
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

// SetLabels replaces the issue's label set.
func (c *Client) SetLabels(ctx context.Context, issueID string, labelIDs []string) error {
	if labelIDs == nil {
		labelIDs = []string{}
	}
	q := `mutation($id: String!, $labels: [String!]) {
  issueUpdate(id: $id, input: { labelIds: $labels }) { success } }`
	return c.do(ctx, q, map[string]any{"id": issueID, "labels": labelIDs}, nil)
}

// Comment posts a Markdown comment on the issue.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	q := `mutation($id: String!, $body: String!) {
  commentCreate(input: { issueId: $id, body: $body }) { success } }`
	return c.do(ctx, q, map[string]any{"id": issueID, "body": body}, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
