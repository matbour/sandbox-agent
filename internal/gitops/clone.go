package gitops

import "context"

// Clone makes a shallow single-branch clone of url at branch into dir.
func Clone(ctx context.Context, url, branch, dir, token string) error {
	g := &Git{Dir: ".", Token: token}
	_, err := g.Run(ctx, "clone", "--depth=1", "--single-branch", "--branch", branch,
		"--no-recurse-submodules", url, dir)

	return err
}
