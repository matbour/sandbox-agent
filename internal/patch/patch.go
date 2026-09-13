// Package patch validates the changes produced in a sandbox before the host commits them.
//
// The input is the output of `git diff --cached --raw -z --no-renames` on the host, after staging
// the sandbox workspace against a clean clone. Everything in it is attacker-controlled.
package patch

import (
	"fmt"
	"path"
	"strings"
)

// Change is one entry of a raw diff.
type Change struct {
	Status  byte // A, M, D, T
	Path    string
	OldMode string
	NewMode string
}

// Limits bound the size of a change set.
type Limits struct {
	MaxFiles int
	MaxBytes int
	// DenyPaths are extra, repository-specific path prefixes ("contracts/") or exact paths
	// ("bun.lock") the change set must not touch.
	DenyPaths []string
}

// Result lists why a change set is rejected, and what a reviewer should look at.
type Result struct {
	Rejections []string
	Warnings   []string
}

// OK reports whether the change set can be committed.
func (r Result) OK() bool { return len(r.Rejections) == 0 }

// ParseRaw parses `git diff --raw -z --no-renames` output.
func ParseRaw(out string) ([]Change, error) {
	var changes []Change

	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		head := fields[i]
		if head == "" {
			continue
		}

		if !strings.HasPrefix(head, ":") || i+1 >= len(fields) {
			return nil, fmt.Errorf("unexpected raw diff entry %q", head)
		}

		parts := strings.Fields(head[1:])
		if len(parts) != 5 || len(parts[4]) == 0 {
			return nil, fmt.Errorf("unexpected raw diff header %q", head)
		}

		i++
		changes = append(changes, Change{
			OldMode: parts[0], NewMode: parts[1], Status: parts[4][0], Path: fields[i],
		})
	}

	return changes, nil
}

// Paths the agent must not touch: CI, repository automation, and files that run code on the
// machines of people who open the repository.
var deniedPrefixes = []string{
	".github/", ".gitlab/", ".circleci/", ".buildkite/", ".husky/", ".vscode/", ".idea/",
	".devcontainer/", ".claude/", ".cursor/", ".git/",
}

var deniedNames = map[string]bool{
	".gitlab-ci.yml": true, "CODEOWNERS": true, ".gitmodules": true, ".envrc": true,
	".npmrc": true, ".yarnrc": true, ".yarnrc.yml": true, "bunfig.toml": true,
	".pre-commit-config.yaml": true, "lefthook.yml": true, ".mcp.json": true,
}

var lockfiles = map[string]bool{
	"bun.lock": true, "bun.lockb": true, "package-lock.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "go.sum": true, "Cargo.lock": true, "poetry.lock": true, "uv.lock": true,
}

// Validate applies the rules to changes; patchBytes is the size of the full binary diff.
func Validate(changes []Change, patchBytes int, l Limits) Result {
	var r Result
	if len(changes) == 0 {
		r.Rejections = append(r.Rejections, "no changes")
	}

	if l.MaxFiles > 0 && len(changes) > l.MaxFiles {
		r.Rejections = append(r.Rejections, fmt.Sprintf("%d files changed (limit %d)", len(changes), l.MaxFiles))
	}

	if l.MaxBytes > 0 && patchBytes > l.MaxBytes {
		r.Rejections = append(r.Rejections, fmt.Sprintf("patch is %d bytes (limit %d)", patchBytes, l.MaxBytes))
	}

	for _, c := range changes {
		p := c.Path
		if reason := badPath(p); reason != "" {
			r.Rejections = append(r.Rejections, fmt.Sprintf("%s: %s", p, reason))
			continue
		}

		if denied(p, l.DenyPaths) {
			r.Rejections = append(r.Rejections, fmt.Sprintf("%s: denied for this repository (DENY_PATHS)", p))
			continue
		}

		if c.Status != 'D' {
			switch c.NewMode {
			case "100644":
			case "100755":
				if c.OldMode != "100755" {
					r.Warnings = append(r.Warnings, fmt.Sprintf("%s: made executable", p))
				}
			case "120000":
				r.Rejections = append(r.Rejections, fmt.Sprintf("%s: symlink", p))
			case "160000":
				r.Rejections = append(r.Rejections, fmt.Sprintf("%s: submodule or nested repository", p))
			default:
				r.Rejections = append(r.Rejections, fmt.Sprintf("%s: unexpected mode %s", p, c.NewMode))
			}
		}

		base := path.Base(p)
		switch {
		case lockfiles[base]:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: lockfile changed, check resolved URLs", p))
		case base == "package.json":
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: check scripts and dependencies", p))
		}
	}

	return r
}

func badPath(p string) string {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "non-canonical path"
	}

	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") {
			return "forbidden path component"
		}
	}

	for _, pre := range deniedPrefixes {
		if strings.HasPrefix(p+"/", pre) || strings.Contains("/"+p, "/"+pre) {
			return "protected path"
		}
	}

	base := path.Base(p)
	if deniedNames[base] {
		return "protected file"
	}

	if (base == ".env" || strings.HasPrefix(base, ".env.")) &&
		!strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") {
		return "environment file"
	}

	return ""
}

func denied(p string, prefixes []string) bool {
	for _, d := range prefixes {
		if d == p || (strings.HasSuffix(d, "/") && strings.HasPrefix(p, d)) {
			return true
		}
	}

	return false
}
