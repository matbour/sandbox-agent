package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	basePath  = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	workspace = "/workspace"
	agentUID  = 1000
	agentGID  = 1000
	agentHome = "/home/agent"
)

// setup turns the bare guest the kernel started into one commands can run in. The root file
// system is read-only: everything writable is a tmpfs, except the workspace disk.
func setup() error {
	mounts := []struct {
		src, dst, fstype string
		flags            uintptr
		data             string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"},
		{"tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"tmpfs", agentHome, "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, fmt.Sprintf("mode=0755,uid=%d,gid=%d", agentUID, agentGID)},
		{"/dev/vdb", workspace, "ext4", unix.MS_NOSUID | unix.MS_NODEV, ""},
	}

	for _, m := range mounts {
		// /dev is a devtmpfs, so its subdirectories can be created; the others exist in the image.
		if strings.HasPrefix(m.dst, "/dev/") {
			if err := os.MkdirAll(m.dst, 0o755); err != nil {
				return err
			}
		}

		if err := unix.Mount(m.src, m.dst, m.fstype, m.flags, m.data); err != nil {
			return fmt.Errorf("mount %s on %s: %w", m.src, m.dst, err)
		}
	}

	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}

	params := kernelParams(string(cmdline))

	if err := writableEtc(params); err != nil {
		return fmt.Errorf("/etc: %w", err)
	}

	if h := params["sandbox.hostname"]; h != "" {
		if err := unix.Sethostname([]byte(h)); err != nil {
			return fmt.Errorf("hostname: %w", err)
		}
	}

	return loopbackUp()
}

// kernelParams parses the key=value words of the kernel command line.
func kernelParams(cmdline string) map[string]string {
	out := map[string]string{}

	for _, w := range strings.Fields(cmdline) {
		if k, v, ok := strings.Cut(w, "="); ok {
			out[k] = v
		}
	}

	return out
}

// writableEtc copies the image's /etc to a tmpfs, adds resolv.conf and hostname, and mounts the
// copy over /etc. The image has no resolv.conf, and the host's resolver (127.0.0.53) is not
// reachable from the guest (D44).
func writableEtc(params map[string]string) error {
	const etc = "/run/etc"

	if err := copyTree("/etc", etc); err != nil {
		return err
	}

	dns := params["sandbox.dns"]
	if dns == "" {
		dns = "1.1.1.1"
	}

	var resolv strings.Builder
	for _, ip := range strings.Split(dns, ",") {
		fmt.Fprintf(&resolv, "nameserver %s\n", ip)
	}

	if err := os.WriteFile(filepath.Join(etc, "resolv.conf"), []byte(resolv.String()), 0o644); err != nil {
		return err
	}

	if h := params["sandbox.hostname"]; h != "" {
		if err := os.WriteFile(filepath.Join(etc, "hostname"), []byte(h+"\n"), 0o644); err != nil {
			return err
		}
	}

	return unix.Mount(etc, "/etc", "", unix.MS_BIND, "")
}

// copyTree copies directories, regular files and symbolic links, keeping modes and owners.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		st, _ := info.Sys().(*unix.Stat_t)

		switch {
		case info.IsDir():
			err = os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&fs.ModeSymlink != 0:
			var link string

			if link, err = os.Readlink(p); err == nil {
				err = os.Symlink(link, target)
			}
		case info.Mode().IsRegular():
			err = copyFile(p, target, info.Mode().Perm())
		default:
			return nil
		}

		if err != nil {
			return err
		}

		if st != nil {
			_ = os.Lchown(target, int(st.Uid), int(st.Gid))
		}

		if info.IsDir() {
			return os.Chmod(target, info.Mode().Perm())
		}

		return nil
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}

	return err
}

// loopbackUp brings lo up. eth0, when there is one, is configured by the kernel (ip=, D44).
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()

	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}

	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("lo flags: %w", err)
	}

	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)

	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("lo up: %w", err)
	}

	return nil
}
