package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Secrets directory layout (at the repository root by default, ignored by Git):
//
//	secrets/config.env       non-secret settings that name private things (KEY=VALUE)
//	secrets/linear-client-id, linear-client-secret  Linear OAuth application (client credentials)
//	secrets/linear-webhook-secret  signing secret of the Linear webhook (optional)
//	secrets/github-app.pem   GitHub App private key
//	secrets/setup.sh         optional sandbox setup script (+ setup.notes.md)
//	secrets/anthropic-api-key  Anthropic API key, read by vsock-to-anthropic only
//	secrets/claude-oauth-token Claude subscription token (claude setup-token), development phase
//	                         only: set in the guest for providers without key injection (D43)
//	secrets/runner-tokens    on the manager: "<runner> <sha256 of its token>" lines (D48)
//	secrets/runner-token     on a runner: its own token, read by runnerd
//
// The directory and every file read from it must not be accessible to group or others.
const (
	ConfigFile      = "config.env"
	LinearIDFile    = "linear-client-id"
	LinearSecFile   = "linear-client-secret"
	LinearHookFile  = "linear-webhook-secret"
	GitHubKeyFile   = "github-app.pem"
	SetupFile       = "setup.sh"
	AnthropicFile   = "anthropic-api-key"
	ClaudeTokenFile = "claude-oauth-token"
)

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

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
