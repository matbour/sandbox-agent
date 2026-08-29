// Package github authenticates as a GitHub App installation and opens pull requests.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const api = "https://api.github.com"

// App holds the App credentials. The private key never leaves the host.
type App struct {
	ID             string
	InstallationID string
	key            *rsa.PrivateKey
	HTTP           *http.Client
}

// NewApp loads the PEM private key from path.
func NewApp(id, installationID, keyPath string) (*App, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(raw)
	if err != nil {
		return nil, err
	}
	return &App{ID: id, InstallationID: installationID, key: key,
		HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
}

// ParseKey accepts PKCS#1 (what GitHub issues) or PKCS#8.
func ParseKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("github: no PEM block in key file")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parse key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: key is not RSA")
	}
	return rk, nil
}

// JWT returns an App JWT valid for about nine minutes.
func (a *App) JWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.ID,
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Token is a short-lived installation token.
type Token struct {
	Value     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InstallationToken mints a token restricted to the given repository names (without owner).
func (a *App) InstallationToken(ctx context.Context, repos ...string) (*Token, error) {
	jwt, err := a.JWT(time.Now())
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	var tok Token
	err = a.call(ctx, http.MethodPost, "/app/installations/"+a.InstallationID+"/access_tokens",
		"Bearer "+jwt, body, &tok)
	return &tok, err
}

// Repo is a repository as seen by an installation token.
type Repo struct {
	Token *Token
	Owner string
	Name  string
	HTTP  *http.Client
}

// DefaultBranch returns the repository's default branch.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	err := r.call(ctx, http.MethodGet, "", nil, &out)
	return out.DefaultBranch, err
}

// PullRequest is the subset of the API response the worker uses.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// CreateDraftPR opens a draft pull request from head into base.
func (r *Repo) CreateDraftPR(ctx context.Context, head, base, title, body string) (*PullRequest, error) {
	var pr PullRequest
	err := r.call(ctx, http.MethodPost, "/pulls", map[string]any{
		"head": head, "base": base, "title": title, "body": body, "draft": true,
	}, &pr)
	return &pr, err
}

// CloneURL is the HTTPS URL; credentials are passed through Git config, never in the URL.
func (r *Repo) CloneURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", r.Owner, r.Name)
}

func (r *Repo) call(ctx context.Context, method, path string, in, out any) error {
	a := &App{HTTP: r.HTTP}
	return a.call(ctx, method, "/repos/"+r.Owner+"/"+r.Name+path, "token "+r.Token.Value, in, out)
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
