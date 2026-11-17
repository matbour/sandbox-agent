package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cloneAndExport makes the clean clone of repo's HEAD, and exports its tracked files, without Git
// metadata, to the workspace the sandbox gets.
func cloneAndExport(ctx context.Context, repo, clean, ws string) error {
	if _, err := git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--", repo, clean); err != nil {
		return err
	}

	if err := os.Mkdir(ws, 0o700); err != nil {
		return err
	}

	tarball := filepath.Join(filepath.Dir(clean), "head.tar")
	defer func() { _ = os.Remove(tarball) }()

	if _, err := git(ctx, clean, "archive", "--format=tar", "-o", tarball, "HEAD"); err != nil {
		return err
	}

	if b, err := exec.CommandContext(ctx, "tar", "-xf", tarball, "-C", ws, "--no-same-owner").CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, b)
	}

	// The excluded paths never count as changes.
	return os.WriteFile(filepath.Join(clean, ".git", "info", "exclude"), []byte(strings.Join(exclude, "\n")+"\n"), 0o600)
}

// diff takes the patch of the workspace against HEAD. Git runs with the clean clone's metadata,
// which the agent never saw, and the workspace only as content, so nothing the agent wrote is run
// or read as configuration: hooks and fsmonitor are off as well. A separate index leaves the clone
// untouched.
func diff(ctx context.Context, clean, ws string) ([]byte, int, error) {
	index := filepath.Join(filepath.Dir(clean), "index.tmp")
	defer func() { _ = os.Remove(index) }()

	base := []string{"--git-dir", filepath.Join(clean, ".git"), "--work-tree", ws,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.symlinks=true"}

	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // built here
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_CONFIG_NOSYSTEM=1")

		var so, se bytes.Buffer

		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(se.String()))
		}

		return so.Bytes(), nil
	}

	if _, err := run("read-tree", "HEAD"); err != nil {
		return nil, 0, err
	}

	if _, err := run("add", "--all", "."); err != nil {
		return nil, 0, err
	}

	patch, err := run("diff", "--cached", "--binary", "HEAD")
	if err != nil {
		return nil, 0, err
	}

	names, err := run("diff", "--cached", "--name-only", "HEAD")
	if err != nil {
		return nil, 0, err
	}

	return patch, len(strings.Fields(string(names))), nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return out, nil
}
