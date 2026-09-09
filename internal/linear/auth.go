package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type appToken struct {
	Value   string    `json:"access_token"`
	Expires time.Time `json:"expires"`
	Scopes  string    `json:"scopes"`
}

// AppScopes are requested for the application token: read and write, plus agent mode (the app
// can be mentioned and delegated issues, which opens agent sessions).
const AppScopes = "read,write,app:assignable,app:mentionable"

// New returns a client authenticated as an OAuth application with client credentials.
func New(clientID, clientSecret, tokenPath string) *Client {
	return &Client{ClientID: clientID, ClientSecret: clientSecret, TokenPath: tokenPath,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

const tokenEndpoint = "https://api.linear.app/oauth/token"

// auth returns the Authorization header value, minting an application token when needed.
func (c *Client) auth(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == nil && !fresh && c.TokenPath != "" {
		if b, err := os.ReadFile(c.TokenPath); err == nil {
			var t appToken
			if json.Unmarshal(b, &t) == nil && t.Value != "" && t.Scopes == AppScopes {
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
		"scope":         {AppScopes},
	}
	url := tokenEndpoint
	if c.tokenURL != "" {
		url = c.tokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(form.Encode()))
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
	return &appToken{Value: out.AccessToken, Scopes: AppScopes,
		Expires: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}
