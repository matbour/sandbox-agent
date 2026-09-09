package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSecretsDir(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	write(t, dir, ConfigFile, "GITHUB_APP_ID=1\nGITHUB_INSTALLATION_ID=2\nGITHUB_REPO=o/r\n", 0o600)
	write(t, dir, LinearIDFile, "id\n", 0o600)
	write(t, dir, LinearSecFile, "secret\n", 0o600)
	write(t, dir, GitHubKeyFile, "pem", 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.LinearClientID != "id" || c.LinearSecret != "secret" || c.GitHubKeyPath != filepath.Join(dir, GitHubKeyFile) || c.Repo != "o/r" {
		t.Fatalf("got %+v", c)
	}
	if c.AgentModel != "claude-sonnet-5-5" {
		t.Fatalf("default model %q", c.AgentModel)
	}
}

func TestLoadRejectsOpenPermissions(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	write(t, dir, ConfigFile, "GITHUB_APP_ID=1\nGITHUB_INSTALLATION_ID=2\nGITHUB_REPO=o/r\n", 0o600)
	write(t, dir, LinearIDFile, "id", 0o600)
	write(t, dir, LinearSecFile, "secret", 0o644)
	write(t, dir, GitHubKeyFile, "pem", 0o600)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "chmod 0600") {
		t.Fatalf("expected permission error, got %v", err)
	}
	os.Chmod(filepath.Join(dir, LinearSecFile), 0o600)
	os.Chmod(dir, 0o755)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "chmod 0700") {
		t.Fatalf("expected directory permission error, got %v", err)
	}
}

func TestLoadReportsMissing(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "linear-client-id") || !strings.Contains(err.Error(), "github-app.pem") {
		t.Fatalf("expected missing settings, got %v", err)
	}
}

func TestModelFor(t *testing.T) {
	c := &Config{TriggerLabel: "agent", AgentModel: "default-model"}
	var err error
	if c.ModelLabels, err = parseModelLabels(DefaultModelLabels); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		labels []string
		model  string
		ok     bool
	}{
		{[]string{"agent"}, "default-model", true},
		{[]string{"Bug", "AGENT-HAIKU"}, "claude-haiku-5-5", true},
		{[]string{"agent-opus", "agent-haiku"}, "claude-opus-5-5", true},
		{[]string{"agent", "agent-fable"}, "claude-fable-5-2", true},
		{[]string{"bug"}, "default-model", false},
	}
	for _, tc := range cases {
		m, ok := c.ModelFor(tc.labels)
		if m != tc.model || ok != tc.ok {
			t.Errorf("%v: got %s/%v, want %s/%v", tc.labels, m, ok, tc.model, tc.ok)
		}
	}
	if !c.IsTrigger("Agent-Sonnet") || c.IsTrigger("agentic") {
		t.Error("IsTrigger")
	}
	if _, err := parseModelLabels("agent-x"); err == nil {
		t.Error("expected parse error")
	}
}

func TestLinearCredentials(t *testing.T) {
	base := func(t *testing.T) string {
		dir := t.TempDir()
		os.Chmod(dir, 0o700)
		write(t, dir, ConfigFile, "GITHUB_APP_ID=1\nGITHUB_INSTALLATION_ID=2\nGITHUB_REPO=o/r\n", 0o600)
		write(t, dir, GitHubKeyFile, "pem", 0o600)
		return dir
	}
	dir := base(t)
	write(t, dir, LinearIDFile, "id\n", 0o600)
	write(t, dir, LinearSecFile, "", 0o600)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), LinearSecFile) || strings.Contains(err.Error(), LinearIDFile) {
		t.Fatalf("expected missing client secret, got %v", err)
	}
}
