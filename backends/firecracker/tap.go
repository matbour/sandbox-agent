package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// tapLease is a tap held by one sandbox: an exclusive flock on a file named after it, so two
// sandboxes, in this process or another, never share a tap. The kernel drops the lock if the
// process dies.
type tapLease struct {
	name  string
	index int
	lock  *os.File
}

// tapLockDir is where tap leases live. Taps are the host's, not one provider's: every process of
// the account must see the same locks, whatever its state directory, or two of them take the same
// tap. The user's runtime directory when there is one, else a directory of the account's in /tmp.
func tapLockDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "sandbox-agent", "taps")
	}

	return filepath.Join(os.TempDir(), fmt.Sprintf("sandbox-agent-%d", os.Getuid()), "taps")
}

// leaseTap takes the first free tap among taps, waiting up to wait for one: a tap is a slot of
// the host, shared by every process of the account, and one frees up when a VM closes.
func leaseTap(ctx context.Context, lockDir string, taps []string, wait time.Duration) (*tapLease, error) {
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(wait)

	for {
		l, err := tryTaps(lockDir, taps)
		if l != nil || err != nil {
			return l, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("all %d taps stayed in use for %s", len(taps), wait)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tryTaps takes a free tap, or returns nil, nil when all are in use.
func tryTaps(lockDir string, taps []string) (*tapLease, error) {
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

	return nil, nil
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

// guestAddr is the guest's address behind tap N on the bridge's /24 (deploy/network/fc-net.sh).
func guestAddr(subnet string, tap *tapLease) string {
	return fmt.Sprintf("%s.%d", subnet, 10+tap.index)
}

// guestMAC is the guest's MAC behind tap N: one per tap, so no two guests on the bridge share one.
func guestMAC(tap *tapLease) string { return fmt.Sprintf("06:00:0a:e6:00:%02x", 10+tap.index) }
