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
	write(t, dir, LinearKeyFile, "lin_api_x\n", 0o600)
	write(t, dir, GitHubKeyFile, "pem", 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.LinearAPIKey != "lin_api_x" || c.GitHubKeyPath != filepath.Join(dir, GitHubKeyFile) || c.Repo != "o/r" {
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
	write(t, dir, LinearKeyFile, "k", 0o644)
	write(t, dir, GitHubKeyFile, "pem", 0o600)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "chmod 0600") {
		t.Fatalf("expected permission error, got %v", err)
	}
	os.Chmod(filepath.Join(dir, LinearKeyFile), 0o600)
	os.Chmod(dir, 0o755)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "chmod 0700") {
		t.Fatalf("expected directory permission error, got %v", err)
	}
}

func TestLoadReportsMissing(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "linear-api-key") || !strings.Contains(err.Error(), "github-app.pem") {
		t.Fatalf("expected missing settings, got %v", err)
	}
}
