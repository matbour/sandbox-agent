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

// New returns a client authenticated with a personal API key.
func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}}
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

// Me returns the id and name of the identity the client acts as.
func (c *Client) Me(ctx context.Context) (id, name string, err error) {
	var out struct {
		Viewer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"viewer"`
	}
	err = c.do(ctx, `{ viewer { id name } }`, nil, &out)
	return out.Viewer.ID, out.Viewer.Name, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
