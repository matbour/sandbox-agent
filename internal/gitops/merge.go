package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
