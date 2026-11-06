package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// configured guards OpConfigure: it runs once per guest. A second call would mount the workspace
// twice, or let something in the guest reconfigure it later.
var configured sync.Once

// configure makes a guest restored from a template its own (D49). The order matters: the clock
// first, so that nothing after it runs with the template's time.
func configure(c *guestexec.Configure) guestexec.Result {
	if c == nil {
		return guestexec.Result{Error: "configure: no settings"}
	}

	err := errors.New("configure: already done")

	configured.Do(func() { err = apply(c) })

	if err != nil {
		return guestexec.Result{Error: err.Error()}
	}

	return guestexec.Result{}
}

func apply(c *guestexec.Configure) error {
	if c.UnixNano != 0 {
		tv := unix.NsecToTimeval(c.UnixNano)
		if err := unix.Settimeofday(&tv); err != nil {
			return fmt.Errorf("clock: %w", err)
		}
	}

	if len(c.Entropy) > 0 {
		if err := addEntropy(c.Entropy); err != nil {
			return fmt.Errorf("entropy: %w", err)
		}
	}

	if c.Hostname != "" {
		if err := setHostname(c.Hostname); err != nil {
			return err
		}
	}

	if c.Workspace {
		if err := flushBlockCache("/dev/vdb"); err != nil {
			return err
		}

		if err := mountWorkspace(); err != nil {
			return err
		}
	}

	return nil
}

// addEntropy mixes b into the kernel's random pool and forces a reseed, so this guest's random
// numbers stop being those of every other guest restored from the same snapshot. RNDADDENTROPY
// takes a struct rand_pool_info: the entropy credited in bits, the buffer size, the buffer.
func addEntropy(b []byte) error {
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	info := make([]byte, 8+len(b))
	binary.NativeEndian.PutUint32(info[0:], uint32(len(b)*8))
	binary.NativeEndian.PutUint32(info[4:], uint32(len(b)))
	copy(info[8:], b)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDADDENTROPY,
		uintptr(unsafe.Pointer(&info[0]))); errno != 0 {
		return fmt.Errorf("RNDADDENTROPY: %w", errno)
	}

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDRESEEDCRNG, 0); errno != 0 {
		return fmt.Errorf("RNDRESEEDCRNG: %w", errno)
	}

	return nil
}

// flushBlockCache drops the guest's cached blocks of dev. The template's kernel read the disk it
// was built with (its partition table, at least): the job's disk is another file behind the same
// device, and those blocks would be stale.
func flushBlockCache(dev string) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := unix.IoctlSetInt(int(f.Fd()), unix.BLKFLSBUF, 0); err != nil {
		return fmt.Errorf("BLKFLSBUF %s: %w", dev, err)
	}

	return nil
}
