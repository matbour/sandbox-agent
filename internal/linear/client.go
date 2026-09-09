// Package linear is agentd's Linear client. The GraphQL operations live in queries.graphql and are
// compiled by genqlient into generated.go, checked against Linear's schema (schema.graphql, from
// the official SDK repository). This package adds authentication (an OAuth application with
// client credentials) and maps the generated types to small domain types.
package linear

//go:generate go tool genqlient

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Khan/genqlient/graphql"
)

const endpoint = "https://api.linear.app/graphql"

// Client talks to Linear as an OAuth application (client credentials, actor app).
type Client struct {
	ClientID     string
	ClientSecret string
	// TokenPath, if set, caches the application token across restarts (mode 0600).
	TokenPath string
	HTTP      *http.Client // for the OAuth token endpoint and as the base of GraphQL calls

	mu       sync.Mutex
	token    *appToken
	gqlOnce  sync.Once
	gql      graphql.Client
	endpoint string // tests only
	tokenURL string // tests only
}

// client returns the GraphQL client, whose transport adds the Authorization header.
func (c *Client) client() graphql.Client {
	c.gqlOnce.Do(func() {
		base := c.HTTP
		if base == nil {
			base = &http.Client{Timeout: 30 * time.Second}
		}
		tr := base.Transport
		if tr == nil {
			tr = http.DefaultTransport
		}
		url := endpoint
		if c.endpoint != "" {
			url = c.endpoint
		}
		c.gql = graphql.NewClient(url, &http.Client{Timeout: base.Timeout, Transport: authTransport{c: c, base: tr}})
	})
	return c.gql
}

// authTransport sets the Authorization header. An application token can be revoked or expire
// early: on a 401 it mints a new one and retries once.
type authTransport struct {
	c    *Client
	base http.RoundTripper
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		auth, err := t.c.auth(req.Context(), attempt > 0)
		if err != nil {
			return nil, err
		}
		r := req.Clone(req.Context())
		if attempt > 0 && req.GetBody != nil {
			if r.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
		r.Header.Set("Authorization", auth)
		resp, err := t.base.RoundTrip(r)
		if err == nil && resp.StatusCode == http.StatusUnauthorized && attempt == 0 && req.GetBody != nil {
			resp.Body.Close()
			continue
		}
		return resp, err
	}
}

// Viewer returns the name of the identity the client acts as.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	_, name, err := c.Me(ctx)
	return name, err
}

// Me returns the id and name of the identity the client acts as.
func (c *Client) Me(ctx context.Context) (id, name string, err error) {
	r, err := Viewer(ctx, c.client())
	if err != nil {
		return "", "", err
	}
	return r.Viewer.Id, r.Viewer.Name, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
