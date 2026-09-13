package job

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/gitops"
)

// Excluded from the change set whatever the repository's .gitignore says.
const exclude = `node_modules/
.bun/
.cache/
.turbo/
.next/
dist/
coverage/
__pycache__/
.pytest_cache/
*.pyc
.DS_Store
`

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// prepare exports the clean clone into the workspace and sets the host-side excludes.
func prepare(ctx context.Context, repoDir, ws string) error {
	if err := exportTree(ctx, repoDir, ws); err != nil {
		return fmt.Errorf("export working tree: %w", err)
	}

	return os.WriteFile(filepath.Join(repoDir, ".git/info/exclude"), []byte(exclude), 0o600)
}

// exportTree copies HEAD's tracked files (no .git) from repoDir into dst.
func exportTree(ctx context.Context, repoDir, dst string) error {
	tarPath := filepath.Join(filepath.Dir(dst), "tree.tar")

	g := &gitops.Git{Dir: repoDir}
	if _, err := g.Run(ctx, "archive", "--format=tar", "-o", tarPath, "HEAD"); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarPath) }()

	cmd := exec.CommandContext(ctx, "tar", "-xf", tarPath, "-C", dst, "--no-same-owner")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, b)
	}

	return nil
}

// workdir is a job's directory under <state>/jobs: the host clone in repo/, the sandbox workspace
// in workspace/, and the logs, prompt and patch at the top.
type workdir struct {
	dir, repo, ws string
	slug, stamp   string // issue slug ("eng-750") and UTC timestamp, for names
}

// newWorkdir creates <state>/jobs/<slug>[-<kind>]-<stamp>/workspace.
func (r *Runner) newWorkdir(issue, kind string) (*workdir, error) {
	w := &workdir{stamp: time.Now().UTC().Format("20060102-150405"),
		slug: strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(issue), "-"), "-")}

	name := w.slug
	if kind != "" {
		name += "-" + kind
	}

	w.dir = filepath.Join(r.Cfg.StateDir, "jobs", name+"-"+w.stamp)
	w.repo, w.ws = filepath.Join(w.dir, "repo"), filepath.Join(w.dir, "workspace")

	return w, os.MkdirAll(w.ws, 0o700)
}

// cleanWorkdir deletes the clone and the workspace (gigabytes with node_modules), keeping the
// logs, the prompt and the patch.
func cleanWorkdir(dir string) {
	_ = os.RemoveAll(filepath.Join(dir, "repo"))
	_ = os.RemoveAll(filepath.Join(dir, "workspace"))
}
