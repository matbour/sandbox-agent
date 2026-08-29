// Package linear is a minimal client for the Linear GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const endpoint = "https://api.linear.app/graphql"

// Client talks to Linear as an OAuth application (client credentials, actor app) when ClientID
// is set, else with a personal API key.
type Client struct {
	APIKey       string
	ClientID     string
	ClientSecret string
	// TokenPath, if set, caches the application token across restarts (mode 0600).
	TokenPath string
	HTTP      *http.Client

	mu    sync.Mutex
	token *appToken
}

type appToken struct {
	Value   string    `json:"access_token"`
	Expires time.Time `json:"expires"`
}

// New returns a client authenticated with a personal API key.
func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// NewApp returns a client authenticated as an OAuth application with client credentials.
func NewApp(clientID, clientSecret, tokenPath string) *Client {
	return &Client{ClientID: clientID, ClientSecret: clientSecret, TokenPath: tokenPath,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// IsApp reports whether the client acts as the application.
func (c *Client) IsApp() bool { return c.ClientID != "" }

const tokenEndpoint = "https://api.linear.app/oauth/token"

// auth returns the Authorization header value, minting an application token when needed.
func (c *Client) auth(ctx context.Context, fresh bool) (string, error) {
	if !c.IsApp() {
		return c.APIKey, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == nil && !fresh && c.TokenPath != "" {
		if b, err := os.ReadFile(c.TokenPath); err == nil {
			var t appToken
			if json.Unmarshal(b, &t) == nil && t.Value != "" {
				c.token = &t
			}
		}
	}
	// Renew a day before expiry (tokens last 30 days, with no refresh token).
	if fresh || c.token == nil || time.Until(c.token.Expires) < 24*time.Hour {
		t, err := c.mint(ctx)
		if err != nil {
			return "", err
		}
		c.token = t
		if c.TokenPath != "" {
			b, _ := json.Marshal(t)
			_ = os.WriteFile(c.TokenPath, b, 0o600)
		}
	}
	return "Bearer " + c.token.Value, nil
}

func (c *Client) mint(ctx context.Context) (*appToken, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"scope":         {"read,write"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
		return nil, fmt.Errorf("linear: client credentials token: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 30 * 24 * 3600
	}
	return &appToken{Value: out.AccessToken, Expires: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
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
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		auth, err := c.auth(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		if resp, err = c.HTTP.Do(req); err != nil {
			return err
		}
		// An application token can be revoked or expire early: mint a new one once.
		if resp.StatusCode == http.StatusUnauthorized && c.IsApp() && attempt == 0 {
			resp.Body.Close()
			continue
		}
		break
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

// Viewer returns the name of the identity the client acts as.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var out struct {
		Viewer struct {
			Name string `json:"name"`
		} `json:"viewer"`
	}
	err := c.do(ctx, `{ viewer { name } }`, nil, &out)
	return out.Viewer.Name, err
}

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
