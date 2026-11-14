package firecracker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// A VM's run directory holds every file the VM uses under a fixed name, and firecracker runs in
// it. Its configuration names them relatively, so a snapshot taken in one directory restores in
// another and opens that directory's files (D49): the shared disks are links, the workspace disk
// is the job's own.
const (
	rootfsName    = "rootfs.ext4"    // link to Config.Rootfs
	agentName     = "agent.ext4"     // link to the cached tools disk
	workspaceName = "workspace.ext4" // this VM's workspace disk
	vsockName     = "v.sock"
	apiName       = "api.sock"
)

// layout fills dir with the links to the shared disks. The workspace disk is written by the
// caller.
func layout(dir, rootfs, agent string) error {
	for name, target := range map[string]string{rootfsName: rootfs, agentName: agent} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			return err
		}
	}

	return nil
}

// drives are the VM's disks, by their names in the run directory. Drives get /dev/vda, vdb, … in
// the order they are added; the root device is always vda.
func drives() []drive {
	return []drive{
		{DriveID: "rootfs", PathOnHost: rootfsName, IsRootDevice: true, IsReadOnly: true},
		{DriveID: "workspace", PathOnHost: workspaceName},
		{DriveID: "agent", PathOnHost: agentName, IsReadOnly: true},
	}
}

// template is a snapshot of a booted guest that no job has touched: the guest agent answers, the
// workspace disk is attached but not mounted (sandbox.template=1).
type template struct {
	state, mem string
}

// templateKey names a template after everything the snapshot depends on: the VMM (a snapshot
// loads in the firecracker version that made it), the kernel, the rootfs, the tools disk (named
// after its content), the machine size, the boot arguments and the workspace disk's size.
func (p *Provider) templateKey(agent string, vcpus, memMiB int, bootArgs string) (string, error) {
	h := sha256.New()

	fc, err := lookPath(p.c.Firecracker)
	if err != nil {
		return "", err
	}

	for _, f := range []string{fc, p.c.Kernel, p.c.Rootfs} {
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}

		_, _ = fmt.Fprintf(h, "%s %d %d\n", f, st.Size(), st.ModTime().UnixNano())
	}

	_, _ = fmt.Fprintf(h, "%s\n%d %d %s\n%s\n", filepath.Base(agent), vcpus, memMiB, p.c.DiskSize, bootArgs)

	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// template returns the template for this machine size, building it the first time. Builds are
// serialized per key with a lock file, so the first starts of a fleet build it once. With a tap,
// the template has a network interface, and the build borrows the caller's tap: the guest's
// address it boots with is replaced at each restore, so it is not part of the key.
func (p *Provider) template(ctx context.Context, agent string, vcpus, memMiB int, tap *tapLease) (template, error) {
	keyArgs := p.bootArgs("template", nil) + " sandbox.template=1"
	if tap != nil {
		keyArgs += " net"
	}

	key, err := p.templateKey(agent, vcpus, memMiB, keyArgs)
	if err != nil {
		return template{}, err
	}

	dir := filepath.Join(p.c.StateDir, "templates", key)
	t := template{state: filepath.Join(dir, "vm.state"), mem: filepath.Join(dir, "vm.mem")}

	if _, err := os.Stat(t.state); err == nil {
		return t, nil
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return template{}, err
	}

	unlock, err := lockFile(ctx, dir+".lock")
	if err != nil {
		return template{}, err
	}
	defer unlock()

	// Another start may have built it while this one waited for the lock.
	if _, err := os.Stat(t.state); err == nil {
		return t, nil
	}

	began := time.Now()

	args := p.bootArgs("template", tap) + " sandbox.template=1"

	if err := p.buildTemplate(ctx, dir, agent, vcpus, memMiB, args, tap); err != nil {
		return template{}, fmt.Errorf("build template: %w", err)
	}

	p.c.Log.Info("firecracker template built", "key", key, "vcpus", vcpus, "mem_mib", memMiB,
		"took", time.Since(began).Round(time.Millisecond))

	return t, nil
}

// buildTemplate boots a guest in a fresh directory, snapshots it once its agent answers, and
// moves the snapshot files to dir.
func (p *Provider) buildTemplate(ctx context.Context, dir, agent string, vcpus, memMiB int, args string, tap *tapLease) error {
	run, err := os.MkdirTemp(filepath.Dir(dir), "build-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(run) }()

	if err := layout(run, p.c.Rootfs, agent); err != nil {
		return err
	}

	// An empty disk of the same size as the jobs' disks: the guest sees the size it will keep.
	empty, err := os.MkdirTemp(run, "empty-")
	if err != nil {
		return err
	}

	if err := workspaceDisk(ctx, empty, filepath.Join(run, workspaceName), p.c.DiskSize); err != nil {
		return err
	}

	sb := &Sandbox{name: "template", dir: run, c: p.c, vsock: filepath.Join(run, vsockName)}
	defer func() { _ = sb.stop() }()

	vm := vmConfig{vcpus: vcpus, memMiB: memMiB, kernel: p.c.Kernel, bootArgs: args, drives: drives(), vsock: vsockName}
	if tap != nil {
		vm.tap, vm.mac = tap.name, guestMAC(tap)
	}

	if err := sb.boot(ctx, vm); err != nil {
		return err
	}

	snap := filepath.Join(run, "snapshot")
	if err := os.Mkdir(snap, 0o700); err != nil {
		return err
	}

	a := newAPI(filepath.Join(run, apiName))
	if err := a.snapshot(ctx, filepath.Join(snap, "vm.state"), filepath.Join(snap, "vm.mem")); err != nil {
		return err
	}

	// Renamed whole, so a start never sees half a template.
	return os.Rename(snap, dir)
}

// restore starts the VM from a template: a new firecracker process in the VM's directory loads
// the snapshot, the guest resumes where the template stopped, and the guest agent makes it this
// VM (OpConfigure).
func (s *Sandbox) restore(ctx context.Context, t template) error {
	if err := s.startVMM(); err != nil {
		return err
	}

	began := time.Now()

	if err := s.waitFor(ctx, 5*time.Second, func() error { _, err := os.Stat(filepath.Join(s.dir, apiName)); return err }); err != nil {
		return fmt.Errorf("firecracker API socket: %w%s", err, s.consoleTail())
	}

	s.timings.Process = time.Since(began)
	began = time.Now()

	tap := ""
	if s.tap != nil {
		tap = s.tap.name
	}

	a := newAPI(filepath.Join(s.dir, apiName))
	if err := a.restore(ctx, t.state, t.mem, tap); err != nil {
		return fmt.Errorf("load snapshot: %w%s", err, s.consoleTail())
	}

	if err := a.resume(ctx); err != nil {
		return fmt.Errorf("resume: %w%s", err, s.consoleTail())
	}

	s.timings.Configure = time.Since(began)
	began = time.Now()

	defer func() { s.timings.Guest = time.Since(began) }()

	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		return err
	}

	req := guestexec.Request{Op: guestexec.OpConfigure, Configure: &guestexec.Configure{
		Hostname: s.name, UnixNano: time.Now().UnixNano(), Entropy: entropy, Workspace: true,
	}}

	if s.tap != nil {
		req.Configure.Net = &guestexec.Net{
			Address: guestAddr(s.c.Subnet, s.tap) + "/24", Gateway: s.c.Subnet + ".1", MAC: guestMAC(s.tap),
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := s.call(cctx, req, nil); err != nil {
		return fmt.Errorf("configure guest: %w%s", err, s.consoleTail())
	}

	s.timings.Restored = true

	return nil
}

// lockFile takes an exclusive lock on path, waiting for it until ctx is done, and returns the
// function that releases it. The kernel drops the lock if the process dies.
func lockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}

	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}

		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = f.Close()

			return nil, err
		}

		select {
		case <-ctx.Done():
			_ = f.Close()

			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
