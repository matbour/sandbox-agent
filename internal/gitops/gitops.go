// Package gitops runs Git on the host, on clean clones only, with hooks and risky features off.
//
// The installation token is passed through GIT_CONFIG_* environment variables, so it never appears
// in a URL, in .git/config or in process arguments.
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Git runs commands in Dir with a hardened configuration.
type Git struct {
	Dir   string
	Token string // GitHub installation token, optional
	// Env is added to Git's environment (for example GIT_AUTHOR_NAME).
	Env []string
	// WorkTree, when set, makes Git read the working tree from another directory (the sandbox
	// workspace) while the repository metadata stays in Dir/.git.
	WorkTree string
}

var hardening = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"core.symlinks=true",
	"protocol.allow=never",
	"protocol.https.allow=always",
	"submodule.recurse=false",
	"credential.helper=",
	"commit.gpgSign=false",
	"user.name=sandbox-agent",
	"user.email=sandbox-agent@users.noreply.github.com",
}

// Run executes git with args and returns stdout.
func (g *Git) Run(ctx context.Context, args ...string) (string, error) {
	full := []string{}
	for _, c := range hardening {
		full = append(full, "-c", c)
	}
	if g.WorkTree != "" {
		full = append(full, "--git-dir="+g.Dir+"/.git", "--work-tree="+g.WorkTree)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if g.WorkTree == "" {
		cmd.Dir = g.Dir
	}
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
	)
	if g.Token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.Token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		)
	}
	cmd.Env = append(env, g.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if g.Token != "" {
			msg = strings.ReplaceAll(msg, g.Token, "***")
		}
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return stdout.String(), nil
}

// Clone makes a shallow single-branch clone of url at branch into dir.
func Clone(ctx context.Context, url, branch, dir, token string) error {
	g := &Git{Dir: ".", Token: token}
	_, err := g.Run(ctx, "clone", "--depth=1", "--single-branch", "--branch", branch,
		"--no-recurse-submodules", url, dir)
	return err
}
