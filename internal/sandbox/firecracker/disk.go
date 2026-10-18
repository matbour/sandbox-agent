package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

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

	// Built under a temporary name and renamed, so a concurrent start never sees half an image.
	tmp := img + ".tmp" + fmt.Sprint(os.Getpid())
	if err := mkfs(ctx, "-L", "agent", "-m", "0", "-O", "^has_journal", "-d", stage, tmp, "64M"); err != nil {
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

// tapLease is a tap held by one sandbox: an exclusive flock on a file named after it, so two
// sandboxes, in this process or another, never share a tap. The kernel drops the lock if the
// process dies.
type tapLease struct {
	name  string
	index int
	lock  *os.File
}

// leaseTap takes the first free tap among taps.
func leaseTap(lockDir string, taps []string) (*tapLease, error) {
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}

	for i, t := range taps {
		f, err := os.OpenFile(filepath.Join(lockDir, t+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}

		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &tapLease{name: t, index: tapIndex(t, i), lock: f}, nil
		}

		_ = f.Close()

		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("all %d taps are in use", len(taps))
}

func (l *tapLease) release() { _ = l.lock.Close() }

// tapIndex reads N from fc-tapN, which gives the guest its address (deploy/network/fc-net.sh);
// other names fall back to their position in the list.
func tapIndex(name string, pos int) int {
	var n int
	if _, err := fmt.Sscanf(name, "fc-tap%d", &n); err == nil {
		return n
	}

	return pos
}

// hostTaps lists the fc-tap* devices on this host, in order.
func hostTaps() []string {
	var out []string

	for i := range 64 {
		name := fmt.Sprintf("fc-tap%d", i)
		if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err == nil {
			out = append(out, name)
		}
	}

	return out
}
