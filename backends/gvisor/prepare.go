package gvisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Prepare checks the tools and the image, and removes the sandboxes of processes that died
// without closing them.
func (p *Provider) Prepare(ctx context.Context) error {
	if _, err := exec.LookPath(p.c.Podman); err != nil {
		return fmt.Errorf("podman: %w", err)
	}

	if _, err := p.runsc(); err != nil {
		return err
	}

	if _, err := p.podman(ctx, "image", "exists", p.c.Image); err != nil {
		return fmt.Errorf("image %s: not found (scripts/build-rootfs.sh)", p.c.Image)
	}

	p.reapOrphans(ctx)

	return nil
}

// runsc returns runsc's real path: Podman runs it by that path, which is the one the AppArmor
// profile allows user namespaces to.
func (p *Provider) runsc() (string, error) {
	path, err := exec.LookPath(p.c.Runsc)
	if err != nil {
		return "", fmt.Errorf("runsc: %w", err)
	}

	return filepath.EvalSymlinks(path)
}

// podman runs one podman command and returns its output.
func (p *Provider) podman(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.c.Podman, append([]string{"--cgroup-manager=cgroupfs"}, args...)...) //nolint:gosec // args are built here

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("podman %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return out, nil
}

// reapOrphans removes the sandboxes whose owner process is gone.
func (p *Provider) reapOrphans(ctx context.Context) {
	out, err := p.podman(ctx, "ps", "--all", "--filter", "label="+labelProvider,
		"--format", `{{.Names}} {{index .Labels "`+labelOwner+`"}}`)
	if err != nil {
		return
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, owner, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		pid, err := strconv.Atoi(owner)
		if err == nil && pid != os.Getpid() && syscall.Kill(pid, 0) == nil {
			continue
		}

		if pid == os.Getpid() {
			continue
		}

		p.c.Log.Info("gvisor: removing an orphan sandbox", "container", name)
		_, _ = p.podman(ctx, "rm", "--force", "--time", "0", name)
	}
}
