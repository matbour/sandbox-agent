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
	"path/filepath"
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

// CloneForUpdate clones branch head (blobless, full history, so merges find their base) and
// fetches base as origin/<base>.
func CloneForUpdate(ctx context.Context, url, head, base, dir, token string) error {
	g := &Git{Dir: ".", Token: token}
	if _, err := g.Run(ctx, "clone", "--filter=blob:none", "--single-branch", "--branch", head,
		"--no-recurse-submodules", url, dir); err != nil {
		return err
	}
	g.Dir = dir
	_, err := g.Run(ctx, "fetch", "--filter=blob:none", "origin", "+refs/heads/"+base+":refs/remotes/origin/"+base)
	return err
}

// MergeBase merges origin/<base> into HEAD. merged is false when HEAD already contains it. On
// conflicts, the files with markers are committed to a local merge commit and listed, so the
// agent can resolve them and the host can amend that commit before pushing.
func MergeBase(ctx context.Context, g *Git, base string) (merged bool, conflicts []string, err error) {
	ref := "origin/" + base
	if _, err := g.Run(ctx, "merge-base", "--is-ancestor", ref, "HEAD"); err == nil {
		return false, nil, nil
	}
	head, err := g.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return false, nil, err
	}
	msg := fmt.Sprintf("Merge %s into %s", base, strings.TrimSpace(head))
	if _, mErr := g.Run(ctx, "merge", "--no-ff", "--no-verify", "-m", msg, ref); mErr == nil {
		return true, nil, nil
	} else {
		out, err := g.Run(ctx, "diff", "--name-only", "--diff-filter=U", "-z")
		if err != nil {
			return false, nil, err
		}
		for _, f := range strings.Split(out, "\x00") {
			if f != "" {
				conflicts = append(conflicts, f)
			}
		}
		if len(conflicts) == 0 {
			return false, nil, mErr
		}
	}
	if _, err := g.Run(ctx, "add", "--all"); err != nil {
		return false, nil, err
	}
	if _, err := g.Run(ctx, "commit", "--no-verify", "-q", "-m", msg+" (with conflicts)"); err != nil {
		return false, nil, err
	}
	return true, conflicts, nil
}

// CheckNoMarkers fails if any of files (relative to dir) still has conflict markers.
func CheckNoMarkers(dir string, files []string) error {
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue // deleted while resolving
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") {
				bad = append(bad, f)
				break
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unresolved merge conflicts in: %s", strings.Join(bad, ", "))
	}
	return nil
}
