// Package firecracker runs sandboxes as Firecracker microVMs (R1, docs/decision/runners.md).
//
// One sandbox is one VM with three disks: the agent rootfs, read-only (vda); the workspace, an
// ext4 image built from the host directory (vdb); and the guest agent binary, read-only (vdc).
// The host drives the guest through the guest agent over vsock (D46): commands, their output,
// and the workspace back as a tar stream (sandbox.Syncer), since Firecracker has no shared
// directory. The network, when the host has taps, is a tap on the bridge of deploy/network/
// (D44), with the guest's address on the kernel command line.
//
// A VM normally starts from a snapshot (D49): the first start of a machine size boots a guest,
// snapshots it before any job, and keeps it as a template; every start then restores the template
// in a new firecracker process and the guest agent makes the guest its own (OpConfigure). The
// memory file is shared, copy-on-write, by every VM restored from it. With a tap, the load swaps
// the template's tap for the VM's, and OpConfigure gives the guest that tap's address and MAC.
//
// It runs rootless, without Firecracker's jailer, which needs root (D47); the layer 1 runtime
// will wrap the VMM instead (M3). Not done yet: converting an OCI image to the rootfs.
package firecracker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/backends/firecracker/fcapi"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Config locates what a VM is made of. Zero values get the defaults of Defaults.
type Config struct {
	Firecracker string        // the firecracker binary
	Kernel      string        // uncompressed guest kernel (vmlinux)
	Rootfs      string        // agent rootfs, ext4
	GuestAgent  string        // cmd/guest-agent, built for the guest's architecture
	Worker      string        // cmd/worker, same architecture; optional
	StateDir    string        // run directories, caches
	TapLockDir  string        // tap leases; host-wide, shared by every process of the account
	TapWait     time.Duration // how long a start waits for a free tap
	Taps        []string      // taps to use; nil: every fc-tapN on the host; empty: no network
	Subnet      string        // first three octets of the bridge's /24 (deploy/network/fc-net.sh)
	DNS         string        // resolvers for the guest, comma-separated
	BaseEnv     []string      // added to every command's environment (the image's ENV)
	DiskSize    string        // workspace disk ceiling, sparse
	CPUSet      string        // host cores for the VMs (taskset)
	BootTimeout time.Duration
	NoSnapshots bool  // boot every VM cold instead of restoring a template (D49)
	MaxOutput   int   // per stream of one Exec
	MaxSync     int64 // bytes of file content one SyncWorkspace may bring back
	Log         *slog.Logger
}

// Defaults fills Config from the repository layout: binaries in <root>/bin (or
// <root>/bin/linux-<arch>), images in <root>/build/<arch>.
func Defaults(c Config, root string) Config {
	arch := runtime.GOARCH

	bin := filepath.Join(root, "bin")
	if _, err := os.Stat(filepath.Join(bin, "guest-agent")); err != nil {
		bin = filepath.Join(root, "bin", "linux-"+arch)
	}

	home, _ := os.UserHomeDir()

	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}

	def(&c.Firecracker, "firecracker")
	def(&c.Kernel, filepath.Join(root, "build", arch, "vmlinux"))
	def(&c.Rootfs, filepath.Join(root, "build", arch, "rootfs.ext4"))
	def(&c.GuestAgent, filepath.Join(bin, "guest-agent"))
	def(&c.Worker, filepath.Join(bin, "worker"))
	def(&c.StateDir, filepath.Join(home, ".local/state/sandbox-agent/firecracker"))
	def(&c.TapLockDir, tapLockDir())
	def(&c.Subnet, "10.230.0")
	def(&c.DNS, "1.1.1.1")
	def(&c.DiskSize, "20G")

	if c.Taps == nil {
		c.Taps = hostTaps()
	}

	// The caches go on the workspace disk, not in the home directory: home is a tmpfs, capped at
	// half the guest's memory, and a large repository's Bun cache does not fit (ENOSPC). On the
	// disk they cost no memory, Bun can hard-link from its cache into node_modules, and .bun/ and
	// .cache/ are never synced back (internal/job's exclude list).
	if c.BaseEnv == nil {
		c.BaseEnv = []string{"DISABLE_AUTOUPDATER=1", "USE_BUILTIN_RIPGREP=0",
			"BUN_INSTALL_CACHE_DIR=/workspace/.bun/install/cache", "XDG_CACHE_HOME=/workspace/.cache"}
	}

	if c.TapWait == 0 {
		c.TapWait = 2 * time.Minute
	}

	if c.BootTimeout == 0 {
		c.BootTimeout = 30 * time.Second
	}

	if c.MaxOutput == 0 {
		c.MaxOutput = 256 << 20
	}

	if c.MaxSync == 0 {
		c.MaxSync = 4 << 30
	}

	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return c
}

// New is the registry factory: defaults relative to the running binary (<root>/bin/agentd).
func New(o sandbox.Options) (sandbox.Provider, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	root := filepath.Dir(filepath.Dir(exe))
	if strings.HasPrefix(filepath.Base(filepath.Dir(exe)), "linux-") {
		root = filepath.Dir(root)
	}

	// slog.Default is agentd's or runnerd's logger: a template that cannot be built must show.
	return &Provider{c: Defaults(Config{CPUSet: o.CPUSet, Log: slog.Default()}, root)}, nil
}

// NewWith builds a provider from an explicit configuration (tests, runnerd).
func NewWith(c Config) *Provider { return &Provider{c: c} }

// Provider starts Firecracker microVMs.
type Provider struct {
	c Config

	versionChecked sync.Once
}

// Name returns "firecracker".
func (p *Provider) Name() string { return "firecracker" }

// CarriesWorker reports whether the worker binary is there to put in the guest.
func (p *Provider) CarriesWorker() bool {
	_, err := os.Stat(p.c.Worker)

	return err == nil
}

// Prepare checks that the binaries, images and /dev/kvm are there, and removes the microVMs of
// processes that died without closing them.
func (p *Provider) Prepare(context.Context) error {
	p.reapOrphans()

	if _, err := lookPath(p.c.Firecracker); err != nil {
		return fmt.Errorf("firecracker binary: %w", err)
	}

	p.versionChecked.Do(p.checkVersion)

	for _, f := range []string{p.c.Kernel, p.c.Rootfs, p.c.GuestAgent} {
		if _, err := os.Stat(f); err != nil {
			return err
		}
	}

	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("/dev/kvm: %w (the account must be in the kvm group)", err)
	}

	_ = kvm.Close()

	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return err
	}

	return nil
}

// lookPath finds the firecracker binary, by path or on PATH.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

// checkVersion warns when the installed firecracker is not the release the API types come from
// (fcapi.Version): a field may have changed between them. Firecracker prints "Firecracker v1.18.0".
func (p *Provider) checkVersion() {
	out, err := exec.Command(p.c.Firecracker, "--version").Output()
	if err != nil {
		p.c.Log.Warn("firecracker --version", "err", err)

		return
	}

	fields := strings.Fields(string(out))
	if len(fields) < 2 || strings.TrimPrefix(fields[1], "v") != fcapi.Version {
		p.c.Log.Warn("firecracker is not the version of the API types: update backends/firecracker/fcapi (make firecracker-api)",
			"installed", strings.TrimSpace(firstLine(string(out))), "api", fcapi.Version)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}
