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
	NodeID  string `json:"node_id"`
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

// PR is the subset of a pull request the worker uses.
type PR struct {
	Number  int    `json:"number"`
	NodeID  string `json:"node_id"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// GetPR returns pull request n.
func (r *Repo) GetPR(ctx context.Context, n int) (*PR, error) {
	var pr PR
	err := r.call(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d", n), nil, &pr)
	return &pr, err
}

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

// Check is the outcome of one CI check or commit status.
type Check struct {
	Name       string
	Conclusion string // success, failure, pending, ...
	Summary    string
}

// Checks returns the check runs and commit statuses for a commit.
func (r *Repo) Checks(ctx context.Context, sha string) ([]Check, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Output     struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	var status struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
		} `json:"statuses"`
	}
	if err := r.call(ctx, http.MethodGet, "/commits/"+sha+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	if err := r.call(ctx, http.MethodGet, "/commits/"+sha+"/status", nil, &status); err != nil {
		return nil, err
	}
	var out []Check
	for _, c := range runs.CheckRuns {
		concl := c.Conclusion
		if c.Status != "completed" {
			concl = c.Status
		}
		out = append(out, Check{Name: c.Name, Conclusion: concl, Summary: strings.TrimSpace(c.Output.Title + " " + c.Output.Summary)})
	}
	for _, s := range status.Statuses {
		out = append(out, Check{Name: s.Context, Conclusion: s.State, Summary: s.Description})
	}
	return out, nil
}

// Comment posts a conversation comment on pull request (or issue) n.
func (r *Repo) Comment(ctx context.Context, n int, body string) error {
	return r.call(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", n), map[string]string{"body": body}, nil)
}

// PRNumber extracts the number from a pull request URL (…/pull/123).
func PRNumber(url string) (int, bool) {
	i := strings.LastIndex(url, "/pull/")
	if i < 0 {
		return 0, false
	}
	n := 0
	for _, c := range url[i+len("/pull/"):] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}

// MarkReady takes a draft pull request out of draft. The REST API cannot do it; GraphQL can.
func (r *Repo) MarkReady(ctx context.Context, nodeID string) error {
	body := map[string]any{
		"query":     `mutation($id: ID!) { markPullRequestReadyForReview(input: { pullRequestId: $id }) { pullRequest { isDraft } } }`,
		"variables": map[string]string{"id": nodeID},
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	a := &App{HTTP: r.HTTP}
	if err := a.call(ctx, http.MethodPost, "/graphql", "token "+r.Token.Value, body, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("github: mark ready: %s", out.Errors[0].Message)
	}
	return nil
}
