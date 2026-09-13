// Package config loads agentd settings from an env-style file and the process environment.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting agentd needs. Secrets stay on the host.
type Config struct {
	LinearClientID string // OAuth application (client credentials)
	LinearSecret   string
	LinearHookKey  string        // Linear webhook signing secret; empty disables the webhook route
	WebAddr        string        // loopback address of the HTTP server; empty disables it
	AccessTeam     string        // Cloudflare Access team name, for the overview behind the tunnel
	AccessAUD      string        // Cloudflare Access application audience tag
	OverviewURL    string        // public URL of the overview, linked from Linear agent sessions
	CIWait         time.Duration // how long to wait for a pull request's checks
	CIFixAttempts  int           // fix passes while checks fail, before handing back
	IgnoreChecks   []string      // checks that do not decide readiness (substring match)
	TriggerLabel   string
	TeamKeys       []string // empty: all teams
	PollInterval   time.Duration
	GitHubAppID    string
	GitHubInstall  string
	GitHubKeyPath  string
	Repo           string // owner/name
	BaseBranch     string // empty: the repository's default branch
	StateDir       string
	SetupScript    string // optional, run as root in the sandbox before the agent
	Provider       string // sandbox provider: sbx (default), ...
	CPUs           string // host cores for the sbx daemon and its VMs
	SandboxCPUs    int
	SandboxMemory  string
	AgentModel     string       // model for TriggerLabel
	ModelLabels    []ModelLabel // labels that select a model explicitly, weakest first
	RetryLabel     string       // label suggested after a failed run
	AgentMaxTurns  int
	AgentBudgetUSD string
	AgentTimeout   time.Duration
	DenyNetwork    []string
	DenyPaths      []string // extra path prefixes the patch must not touch
	AuthorName     string   // commit author and committer; empty: sandbox-agent
	AuthorEmail    string
	MaxFiles       int
	MaxPatchBytes  int
}

// Load reads the secrets directory, lets the environment override settings, and requires the
// Linear and GitHub settings.
func Load(secretsDir string) (*Config, error) { return load(secretsDir, true) }

// LoadPartial is Load without the Linear and GitHub requirements, for local runs.
func LoadPartial(secretsDir string) (*Config, error) { return load(secretsDir, false) }

func load(secretsDir string, full bool) (*Config, error) {
	vals := map[string]string{}
	if _, err := os.Stat(secretsDir); err == nil {
		if err := CheckPrivate(secretsDir); err != nil {
			return nil, err
		}
	} else if full {
		return nil, fmt.Errorf("secrets directory: %w", err)
	}
	if path := filepath.Join(secretsDir, ConfigFile); fileExists(path) {
		if err := CheckPrivate(path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }() // read only
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
			}
			vals[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	get := func(k, def string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		if v, ok := vals[k]; ok && v != "" {
			return v
		}
		return def
	}
	linearID, err := readSecret(secretsDir, LinearIDFile)
	if err != nil {
		return nil, err
	}
	linearSecret, err := readSecret(secretsDir, LinearSecFile)
	if err != nil {
		return nil, err
	}
	linearHook, err := readSecret(secretsDir, LinearHookFile)
	if err != nil {
		return nil, err
	}
	setupDefault := ""
	if p := filepath.Join(secretsDir, SetupFile); fileExists(p) {
		setupDefault = p
	}
	home, _ := os.UserHomeDir()
	c := &Config{
		LinearClientID: linearID,
		LinearSecret:   linearSecret,
		LinearHookKey:  linearHook,
		WebAddr:        get("WEB_ADDR", "127.0.0.1:8787"),
		AccessTeam:     get("ACCESS_TEAM", ""),
		AccessAUD:      get("ACCESS_AUD", ""),
		OverviewURL:    get("OVERVIEW_URL", ""),
		IgnoreChecks:   list(get("IGNORE_CHECKS", "claude-review")),
		TriggerLabel:   get("TRIGGER_LABEL", "agent"),
		TeamKeys:       list(get("LINEAR_TEAMS", "")),
		GitHubAppID:    get("GITHUB_APP_ID", ""),
		GitHubInstall:  get("GITHUB_INSTALLATION_ID", ""),
		GitHubKeyPath:  get("GITHUB_APP_KEY", filepath.Join(secretsDir, GitHubKeyFile)),
		Repo:           get("GITHUB_REPO", ""),
		BaseBranch:     get("BASE_BRANCH", ""),
		StateDir:       get("STATE_DIR", filepath.Join(home, ".local/state/sandbox-agent")),
		SetupScript:    get("SETUP_SCRIPT", setupDefault),
		CPUs:           get("CPUS", "4-7"),
		Provider:       get("SANDBOX_PROVIDER", "sbx"),
		SandboxMemory:  get("SANDBOX_MEMORY", "6g"),
		AgentModel:     get("AGENT_MODEL", "claude-sonnet-5-5"),
		RetryLabel:     get("RETRY_LABEL", "agent-opus"),
		AgentBudgetUSD: get("AGENT_BUDGET_USD", "5"),
		DenyNetwork:    list(get("DENY_NETWORK", "")),
		DenyPaths:      list(get("DENY_PATHS", "")),
		AuthorName:     get("COMMIT_AUTHOR_NAME", ""),
		AuthorEmail:    get("COMMIT_AUTHOR_EMAIL", ""),
	}
	if c.PollInterval, err = time.ParseDuration(get("POLL_INTERVAL", "30s")); err != nil {
		return nil, fmt.Errorf("POLL_INTERVAL: %w", err)
	}
	if c.AgentTimeout, err = time.ParseDuration(get("AGENT_TIMEOUT", "45m")); err != nil {
		return nil, fmt.Errorf("AGENT_TIMEOUT: %w", err)
	}
	if c.CIWait, err = time.ParseDuration(get("CI_TIMEOUT", "25m")); err != nil {
		return nil, fmt.Errorf("CI_TIMEOUT: %w", err)
	}
	for k, dst := range map[string]*int{
		"SANDBOX_CPUS": &c.SandboxCPUs, "AGENT_MAX_TURNS": &c.AgentMaxTurns,
		"MAX_FILES": &c.MaxFiles, "MAX_PATCH_BYTES": &c.MaxPatchBytes, "CI_FIX_ATTEMPTS": &c.CIFixAttempts,
	} {
		def := map[string]string{"SANDBOX_CPUS": "4", "AGENT_MAX_TURNS": "150",
			"MAX_FILES": "200", "MAX_PATCH_BYTES": "2000000", "CI_FIX_ATTEMPTS": "2"}[k]
		if *dst, err = strconv.Atoi(get(k, def)); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	if c.ModelLabels, err = parseModelLabels(get("MODEL_LABELS", DefaultModelLabels)); err != nil {
		return nil, fmt.Errorf("MODEL_LABELS: %w", err)
	}
	if (c.AccessTeam == "") != (c.AccessAUD == "") {
		return nil, fmt.Errorf("set both ACCESS_TEAM and ACCESS_AUD, or neither")
	}
	if (c.AuthorName == "") != (c.AuthorEmail == "") {
		return nil, fmt.Errorf("set both COMMIT_AUTHOR_NAME and COMMIT_AUTHOR_EMAIL, or neither")
	}
	if !full {
		return c, nil
	}
	var missing []string
	for k, v := range map[string]string{
		"GITHUB_APP_ID":          c.GitHubAppID,
		"GITHUB_INSTALLATION_ID": c.GitHubInstall, "GITHUB_REPO": c.Repo,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	for name, v := range map[string]string{LinearIDFile: c.LinearClientID, LinearSecFile: c.LinearSecret} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if st, err := os.Stat(c.GitHubKeyPath); err != nil || st.Size() == 0 {
		missing = append(missing, c.GitHubKeyPath+" (missing or empty)")
	} else if err := CheckPrivate(c.GitHubKeyPath); err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing settings:\n  %s", strings.Join(missing, "\n  "))
	}
	if !strings.Contains(c.Repo, "/") {
		return nil, fmt.Errorf("GITHUB_REPO must be owner/name")
	}
	return c, nil
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
