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
	LinearAPIKey   string
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
	CPUs           string // host cores for the sbx daemon and its VMs
	SandboxCPUs    int
	SandboxMemory  string
	AgentModel     string // default model
	OpusLabel      string // label that runs the issue with OpusModel instead
	OpusModel      string
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

// Secrets directory layout (at the repository root by default, ignored by Git):
//
//	secrets/config.env       non-secret settings that name private things (KEY=VALUE)
//	secrets/linear-api-key   Linear personal API key
//	secrets/github-app.pem   GitHub App private key
//	secrets/setup.sh         optional sandbox setup script (+ setup.notes.md)
//
// The directory and every file read from it must not be accessible to group or others.
const (
	ConfigFile    = "config.env"
	LinearKeyFile = "linear-api-key"
	GitHubKeyFile = "github-app.pem"
	SetupFile     = "setup.sh"
)

// Load reads the secrets directory, lets the environment override settings, and requires the
// Linear and GitHub settings.
func Load(secretsDir string) (*Config, error) { return load(secretsDir, true) }

// LoadPartial is Load without the Linear and GitHub requirements, for local runs.
func LoadPartial(secretsDir string) (*Config, error) { return load(secretsDir, false) }

// CheckPrivate fails if path is accessible to group or others.
func CheckPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		want := "0600"
		if st.IsDir() {
			want = "0700"
		}
		return fmt.Errorf("%s has mode %04o: run chmod %s %s", path, st.Mode().Perm(), want, path)
	}
	return nil
}

// readSecret returns the trimmed content of dir/name, or "" if it does not exist.
func readSecret(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return "", nil
	}
	if err := CheckPrivate(p); err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return strings.TrimSpace(string(b)), err
}

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
		defer f.Close()
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
	linearKey := os.Getenv("LINEAR_API_KEY")
	if linearKey == "" {
		var err error
		if linearKey, err = readSecret(secretsDir, LinearKeyFile); err != nil {
			return nil, err
		}
	}
	setupDefault := ""
	if p := filepath.Join(secretsDir, SetupFile); fileExists(p) {
		setupDefault = p
	}
	home, _ := os.UserHomeDir()
	c := &Config{
		LinearAPIKey:   linearKey,
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
		SandboxMemory:  get("SANDBOX_MEMORY", "6g"),
		AgentModel:     get("AGENT_MODEL", "claude-sonnet-5-5"),
		OpusLabel:      get("OPUS_LABEL", "agent-opus"),
		OpusModel:      get("OPUS_MODEL", "claude-opus-5-5"),
		AgentBudgetUSD: get("AGENT_BUDGET_USD", "5"),
		DenyNetwork:    list(get("DENY_NETWORK", "")),
		DenyPaths:      list(get("DENY_PATHS", "")),
		AuthorName:     get("COMMIT_AUTHOR_NAME", ""),
		AuthorEmail:    get("COMMIT_AUTHOR_EMAIL", ""),
	}
	var err error
	if c.PollInterval, err = time.ParseDuration(get("POLL_INTERVAL", "30s")); err != nil {
		return nil, fmt.Errorf("POLL_INTERVAL: %w", err)
	}
	if c.AgentTimeout, err = time.ParseDuration(get("AGENT_TIMEOUT", "45m")); err != nil {
		return nil, fmt.Errorf("AGENT_TIMEOUT: %w", err)
	}
	for k, dst := range map[string]*int{
		"SANDBOX_CPUS": &c.SandboxCPUs, "AGENT_MAX_TURNS": &c.AgentMaxTurns,
		"MAX_FILES": &c.MaxFiles, "MAX_PATCH_BYTES": &c.MaxPatchBytes,
	} {
		def := map[string]string{"SANDBOX_CPUS": "4", "AGENT_MAX_TURNS": "150",
			"MAX_FILES": "200", "MAX_PATCH_BYTES": "2000000"}[k]
		if *dst, err = strconv.Atoi(get(k, def)); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	if (c.AuthorName == "") != (c.AuthorEmail == "") {
		return nil, fmt.Errorf("set both COMMIT_AUTHOR_NAME and COMMIT_AUTHOR_EMAIL, or neither")
	}
	if !full {
		return c, nil
	}
	var missing []string
	for k, v := range map[string]string{
		filepath.Join(secretsDir, LinearKeyFile) + " (missing or empty)": c.LinearAPIKey, "GITHUB_APP_ID": c.GitHubAppID,
		"GITHUB_INSTALLATION_ID": c.GitHubInstall, "GITHUB_REPO": c.Repo,
	} {
		if v == "" {
			missing = append(missing, k)
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

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
