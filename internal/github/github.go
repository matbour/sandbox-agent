// Package github authenticates as a GitHub App installation and opens pull requests.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const api = "https://api.github.com"

// App holds the App credentials. The private key never leaves the host.
type App struct {
	ID             string
	InstallationID string
	key            *rsa.PrivateKey
	HTTP           *http.Client
}

func (a *App) call(ctx context.Context, method, path, auth string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, api+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := a.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return fmt.Errorf("github: %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Repo is a repository as seen by an installation token.
type Repo struct {
	Token *Token
	Owner string
	Name  string
	HTTP  *http.Client
}

func (r *Repo) call(ctx context.Context, method, path string, in, out any) error {
	a := &App{HTTP: r.HTTP}
	return a.call(ctx, method, "/repos/"+r.Owner+"/"+r.Name+path, "token "+r.Token.Value, in, out)
}
