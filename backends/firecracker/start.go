package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// vmConfig is what the API calls need.
type vmConfig struct {
	vcpus, memMiB int
	kernel        string
	bootArgs      string
	drives        []drive
	tap, mac      string
	vsock         string
}

// Start boots a VM for s and waits until its guest agent answers.
func (p *Provider) Start(ctx context.Context, s sandbox.Spec) (sandbox.Sandbox, error) {
	if !validName.MatchString(s.Name) {
		return nil, fmt.Errorf("invalid sandbox name %q", s.Name)
	}

	memMiB, err := parseMemory(s.Memory)
	if err != nil {
		return nil, err
	}

	vcpus := max(s.CPUs, 1)

	dir := filepath.Join(p.c.StateDir, "vms", s.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	sb := &Sandbox{name: s.Name, dir: dir, workspace: s.Workspace, c: p.c}

	ok := false
	defer func() {
		if !ok {
			// Keep the console of a failed boot next to the run directories: it is the only
			// trace of what the guest said.
			if b, err := os.ReadFile(filepath.Join(dir, "console.log")); err == nil {
				_ = os.WriteFile(filepath.Join(p.c.StateDir, "vms", s.Name+".failed.log"), b, 0o600)
			}

			_ = sb.Close()
		}
	}()

	if len(p.c.Taps) > 0 {
		if sb.tap, err = leaseTap(ctx, p.c.TapLockDir, p.c.Taps, p.c.TapWait); err != nil {
			return nil, err
		}
	}

	tools := []string{p.c.GuestAgent}
	if _, err := os.Stat(p.c.Worker); err == nil {
		tools = append(tools, p.c.Worker)
		sb.worker = "/run/agent/worker"
	}

	began := time.Now()

	agent, err := agentDisk(ctx, tools, filepath.Join(p.c.StateDir, "cache"))
	if err != nil {
		return nil, err
	}

	if err := layout(dir, p.c.Rootfs, agent); err != nil {
		return nil, err
	}

	if err := workspaceDisk(ctx, s.Workspace, filepath.Join(dir, workspaceName), p.c.DiskSize); err != nil {
		return nil, err
	}

	sb.timings.Disks = time.Since(began)
	sb.vsock = filepath.Join(dir, vsockName)

	if !p.c.NoSnapshots {
		began = time.Now()

		t, err := p.template(ctx, agent, vcpus, memMiB, sb.tap)
		if err == nil {
			sb.timings.Template = time.Since(began)

			if err := sb.restore(ctx, t); err != nil {
				return nil, err
			}

			ok = true

			return sb, nil
		}

		p.c.Log.Warn("firecracker template unavailable, booting cold", "err", err)
	}

	vm := vmConfig{
		vcpus: vcpus, memMiB: memMiB, kernel: p.c.Kernel, bootArgs: p.bootArgs(s.Name, sb.tap),
		drives: drives(), vsock: vsockName,
	}

	if sb.tap != nil {
		vm.tap, vm.mac = sb.tap.name, guestMAC(sb.tap)
	}

	if err := sb.boot(ctx, vm); err != nil {
		return nil, err
	}

	ok = true

	return sb, nil
}

// bootArgs is the kernel command line. The init is a shell one-liner that mounts the agent's disk
// and hands over to it (cmd/guest-agent).
func (p *Provider) bootArgs(name string, tap *tapLease) string {
	// pci=off and the i8042 options skip probes for hardware a Firecracker guest does not have
	// (its devices are virtio-mmio): they took about 470 ms of a 720 ms start (benchmarks.md).
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "ro", "quiet",
		"pci=off", "i8042.noaux", "i8042.nomux", "i8042.nopnp", "i8042.dumbkbd",
		"sandbox.hostname=" + name, "sandbox.dns=" + p.c.DNS}

	if tap != nil {
		args = append(args, fmt.Sprintf("ip=%s::%s.1:255.255.255.0::eth0:off", guestAddr(p.c.Subnet, tap), p.c.Subnet))
	}

	init := "mount -t tmpfs -o mode=755 tmpfs /run && mkdir /run/agent && " +
		"mount -t ext4 -o ro /dev/vdc /run/agent && exec /run/agent/guest-agent"

	return strings.Join(args, " ") + ` init=/bin/sh -- -c "` + init + `"`
}

// parseMemory reads "1g", "512m" or a number of MiB; empty means 1 GiB.
func parseMemory(m string) (int, error) {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		return 1024, nil
	}

	mult := 1
	switch {
	case strings.HasSuffix(m, "g"):
		mult, m = 1024, strings.TrimSuffix(m, "g")
	case strings.HasSuffix(m, "m"):
		m = strings.TrimSuffix(m, "m")
	}

	n, err := strconv.Atoi(m)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory %q", m)
	}

	return n * mult, nil
}
