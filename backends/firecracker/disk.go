package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// guestOwner is the agent user of the rootfs (images/agent/Dockerfile): the workspace's files
// belong to it in the guest, whatever the host's UID is.
var guestOwner = [2]int{1000, 1000}

// workspaceDisk writes the host workspace into a new ext4 image at img. The files go through a
// tar stream with their owner replaced, which mkfs.ext4 (1.47.1 or later) reads with -d. The
// image is sparse: its size is a ceiling, not space used on the host.
func workspaceDisk(ctx context.Context, ws, img, size string) error {
	tarPath := img + ".tar"

	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarPath) }()

	err = guestexec.WriteTree(f, ws, guestexec.TreeOptions{Owner: &guestOwner})
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return fmt.Errorf("workspace tar: %w", err)
	}

	owner := fmt.Sprintf("root_owner=%d:%d", guestOwner[0], guestOwner[1])

	return mkfs(ctx, "-L", "workspace", "-m", "0", "-E", owner, "-d", tarPath, img, size)
}

// agentDisk returns a read-only ext4 image holding the guest agent and the other tools (the
// worker), built once per content and cached under cacheDir.
func agentDisk(ctx context.Context, tools []string, cacheDir string) (string, error) {
	h := sha256.New()

	for _, t := range tools {
		sum, err := fileSHA256(t)
		if err != nil {
			return "", fmt.Errorf("guest tools: %w", err)
		}

		_, _ = fmt.Fprintf(h, "%s %s\n", filepath.Base(t), sum)
	}

	img := filepath.Join(cacheDir, "agent-"+hex.EncodeToString(h.Sum(nil))[:16]+".ext4")
	if _, err := os.Stat(img); err == nil {
		return img, nil
	}

	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}

	stage, err := os.MkdirTemp(cacheDir, "stage-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()

	for _, t := range tools {
		if err := copyExecutable(t, filepath.Join(stage, filepath.Base(t))); err != nil {
			return "", err
		}
	}

	// Built under a name of its own and renamed, so a concurrent start never sees half an image.
	// Starts in one process share the pid: the name must not come from it. Two builds give the
	// same content, so whichever rename lands last is fine.
	f, err := os.CreateTemp(cacheDir, "agent-*.ext4.tmp")
	if err != nil {
		return "", err
	}

	tmp := f.Name()
	_ = f.Close()

	if err := mkfs(ctx, "-L", "agent", "-m", "0", "-O", "^has_journal", "-d", stage, tmp, "64M"); err != nil {
		_ = os.Remove(tmp)

		return "", err
	}

	return img, os.Rename(tmp, img)
}

func mkfs(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "mkfs.ext4", append([]string{"-q", "-F"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}

	return err
}
