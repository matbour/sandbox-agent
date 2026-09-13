// Package github talks to GitHub as a GitHub App installation: ghinstallation signs the App's
// JWT, go-github does the REST calls. One GraphQL call (leaving draft) has no REST equivalent and
// goes through go-github's raw request support.
package github

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v93/github"
)

// App is a GitHub App installation. Its private key never leaves the host.
type App struct {
	ID             int64
	InstallationID int64
	HTTP           *http.Client // base client for installation-token calls (timeouts)

	apps    *gh.Client // authenticated as the App (JWT)
	baseURL string     // tests only: a fake API
}

// NewApp loads the App's PEM private key from keyPath.
func NewApp(id, installationID, keyPath string) (*App, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	return newApp(id, installationID, key, "")
}

func newApp(id, installationID string, key []byte, baseURL string) (*App, error) {
	appID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("github: App ID %q: %w", id, err)
	}

	instID, err := strconv.ParseInt(installationID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("github: installation ID %q: %w", installationID, err)
	}

	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, key)
	if err != nil {
		return nil, fmt.Errorf("github: App key: %w", err)
	}

	a := &App{ID: appID, InstallationID: instID, HTTP: &http.Client{Timeout: 30 * time.Second}, baseURL: baseURL}
	if baseURL != "" {
		tr.BaseURL = strings.TrimSuffix(baseURL, "/")
	}

	opts := []gh.ClientOptionsFunc{gh.WithHTTPClient(&http.Client{Transport: tr, Timeout: 30 * time.Second})}
	a.apps, err = gh.NewClient(append(opts, baseOpts(baseURL)...)...)

	return a, err
}

// Token is a short-lived installation token.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// InstallationToken mints a token (one hour), restricted to the given repository names (without
// owner) when any are given.
func (a *App) InstallationToken(ctx context.Context, repos ...string) (*Token, error) {
	var opts *gh.InstallationTokenOptions
	if len(repos) > 0 {
		opts = &gh.InstallationTokenOptions{Repositories: repos}
	}

	t, _, err := a.apps.Apps.CreateInstallationToken(ctx, a.InstallationID, opts)
	if err != nil {
		return nil, fmt.Errorf("github: installation token: %w", err)
	}

	return &Token{Value: t.GetToken(), ExpiresAt: t.GetExpiresAt().Time}, nil
}

// Repo is a repository seen through an installation token.
type Repo struct {
	Token *Token
	Owner string
	Name  string
	HTTP  *http.Client // optional base client

	client  *gh.Client
	baseURL string // tests only
}

func (r *Repo) gh() *gh.Client {
	if r.client == nil {
		opts := []gh.ClientOptionsFunc{gh.WithAuthToken(r.Token.Value)}
		if r.HTTP != nil {
			opts = append(opts, gh.WithHTTPClient(r.HTTP))
		}

		c, err := gh.NewClient(append(opts, baseOpts(r.baseURL)...)...)
		if err != nil {
			panic(err) // only reachable with a malformed test base URL
		}

		r.client = c
	}

	return r.client
}

// CloneURL is the HTTPS URL; credentials go through Git config, never in the URL.
func (r *Repo) CloneURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", r.Owner, r.Name)
}

// DefaultBranch returns the repository's default branch.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	repo, _, err := r.gh().Repositories.Get(ctx, r.Owner, r.Name)
	if err != nil {
		return "", fmt.Errorf("github: repository: %w", err)
	}

	return repo.GetDefaultBranch(), nil
}

// baseOpts points a client at a fake API in tests.
func baseOpts(base string) []gh.ClientOptionsFunc {
	if base == "" {
		return nil
	}

	return []gh.ClientOptionsFunc{gh.WithURLs(&base, &base)}
}
