// Package firecracker runs sandboxes as Firecracker microVMs (R1, docs/decision/runners.md).
//
// One sandbox is one VM with three disks: the agent rootfs, read-only (vda); the workspace, an
// ext4 image built from the host directory (vdb); and the guest agent binary, read-only (vdc).
// The host drives the guest through the guest agent over vsock (D46): commands, their output,
// and the workspace back as a tar stream (sandbox.Syncer), since Firecracker has no shared
// directory. The network, when the host has taps, is a tap on the bridge of deploy/network/
// (D44), with the guest's address on the kernel command line.
//
// It runs rootless, without Firecracker's jailer, which needs root (D47); the layer 1 runtime
// will wrap the VMM instead (M3). Not done yet: snapshots, converting an OCI image to the rootfs.
package firecracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Config locates what a VM is made of. Zero values get the defaults of Defaults.
type Config struct {
	Firecracker string   // the firecracker binary
	Kernel      string   // uncompressed guest kernel (vmlinux)
	Rootfs      string   // agent rootfs, ext4
	GuestAgent  string   // cmd/guest-agent, built for the guest's architecture
	Worker      string   // cmd/worker, same architecture; optional
	StateDir    string   // run directories, caches, tap locks
	Taps        []string // taps to use; nil: every fc-tapN on the host; empty: no network
	Subnet      string   // first three octets of the bridge's /24 (deploy/network/fc-net.sh)
	DNS         string   // resolvers for the guest, comma-separated
	BaseEnv     []string // added to every command's environment (the image's ENV)
	DiskSize    string   // workspace disk ceiling, sparse
	CPUSet      string   // host cores for the VMs (taskset)
	BootTimeout time.Duration
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
	def(&c.Subnet, "10.230.0")
	def(&c.DNS, "1.1.1.1")
	def(&c.DiskSize, "20G")

	if c.Taps == nil {
		c.Taps = hostTaps()
	}

	if c.BaseEnv == nil {
		c.BaseEnv = []string{"DISABLE_AUTOUPDATER=1", "USE_BUILTIN_RIPGREP=0"}
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

	return &Provider{c: Defaults(Config{CPUSet: o.CPUSet}, root)}, nil
}

// NewWith builds a provider from an explicit configuration (tests, runnerd).
func NewWith(c Config) *Provider { return &Provider{c: c} }

// Provider starts Firecracker microVMs.
type Provider struct {
	c Config
}

// Name returns "firecracker".
func (p *Provider) Name() string { return "firecracker" }

// Prepare checks that the binaries, images and /dev/kvm are there.
func (p *Provider) Prepare(context.Context) error {
	if _, err := exec.LookPath(p.c.Firecracker); err != nil {
		return fmt.Errorf("firecracker binary: %w", err)
	}

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
		if sb.tap, err = leaseTap(filepath.Join(p.c.StateDir, "taps"), p.c.Taps); err != nil {
			return nil, err
		}
	}

	tools := []string{p.c.GuestAgent}
	if _, err := os.Stat(p.c.Worker); err == nil {
		tools = append(tools, p.c.Worker)
		sb.worker = "/run/agent/worker"
	}

	agent, err := agentDisk(ctx, tools, filepath.Join(p.c.StateDir, "cache"))
	if err != nil {
		return nil, err
	}

	wsImg := filepath.Join(dir, "workspace.ext4")
	if err := workspaceDisk(ctx, s.Workspace, wsImg, p.c.DiskSize); err != nil {
		return nil, err
	}

	vm := vmConfig{
		vcpus: vcpus, memMiB: memMiB, kernel: p.c.Kernel, bootArgs: p.bootArgs(s.Name, sb.tap),
		drives: []drive{
			{DriveID: "rootfs", PathOnHost: p.c.Rootfs, IsRootDevice: true, IsReadOnly: true},
			{DriveID: "workspace", PathOnHost: wsImg},
			{DriveID: "agent", PathOnHost: agent, IsReadOnly: true},
		},
		vsock: filepath.Join(dir, "v.sock"),
	}

	if sb.tap != nil {
		vm.tap = sb.tap.name
		vm.mac = fmt.Sprintf("06:00:0a:e6:00:%02x", 10+sb.tap.index)
	}

	sb.vsock = vm.vsock

	if err := sb.boot(ctx, vm); err != nil {
		return nil, err
	}

	ok = true

	return sb, nil
}

// bootArgs is the kernel command line. The init is a shell one-liner that mounts the agent's disk
// and hands over to it (cmd/guest-agent).
func (p *Provider) bootArgs(name string, tap *tapLease) string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "ro", "quiet",
		"sandbox.hostname=" + name, "sandbox.dns=" + p.c.DNS}

	if tap != nil {
		addr := fmt.Sprintf("%s.%d", p.c.Subnet, 10+tap.index)
		args = append(args, fmt.Sprintf("ip=%s::%s.1:255.255.255.0::eth0:off", addr, p.c.Subnet))
	}

	init := "mount -t tmpfs -o mode=755 tmpfs /run && mkdir /run/agent && " +
		"mount -t ext4 -o ro /dev/vdc /run/agent && exec /run/agent/guest-agent"

	return strings.Join(args, " ") + ` init=/bin/sh -- -c "` + init + `"`
}

// boot starts the firecracker process, configures the VM through the API and waits for the
// guest agent.
func (s *Sandbox) boot(ctx context.Context, vm vmConfig) error {
	apiSock := filepath.Join(s.dir, "api.sock")

	argv := []string{s.c.Firecracker, "--api-sock", apiSock, "--id", s.name}
	if s.c.CPUSet != "" {
		argv = append([]string{"taskset", "-c", s.c.CPUSet}, argv...)
	}

	console, err := os.Create(filepath.Join(s.dir, "console.log"))
	if err != nil {
		return err
	}

	// Not tied to ctx: the VM outlives the call that started it, until Close.
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is built here, not from input
	cmd.Stdout, cmd.Stderr = console, console
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = console.Close()

		return fmt.Errorf("start firecracker: %w", err)
	}

	s.cmd = cmd
	s.exited = make(chan struct{})

	go func() {
		_ = cmd.Wait()
		_ = console.Close()

		close(s.exited)
	}()

	_ = os.WriteFile(filepath.Join(s.dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)

	if err := s.waitFor(ctx, 5*time.Second, func() error { _, err := os.Stat(apiSock); return err }); err != nil {
		return fmt.Errorf("firecracker API socket: %w%s", err, s.consoleTail())
	}

	if err := newAPI(apiSock).configure(ctx, vm); err != nil {
		return fmt.Errorf("configure VM: %w%s", err, s.consoleTail())
	}

	err = s.waitFor(ctx, s.c.BootTimeout, func() error {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		return s.call(pctx, guestexec.Request{Op: guestexec.OpPing}, nil)
	})
	if err != nil {
		return fmt.Errorf("guest agent: %w%s", err, s.consoleTail())
	}

	return nil
}

// waitFor retries check every 50 ms until it succeeds, the VM exits or timeout passes.
func (s *Sandbox) waitFor(ctx context.Context, timeout time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)

	for {
		err := check()
		if err == nil {
			return nil
		}

		select {
		case <-s.exited:
			return errors.New("the VM stopped")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %w", timeout, err)
		}
	}
}

func (s *Sandbox) consoleTail() string {
	b, err := os.ReadFile(filepath.Join(s.dir, "console.log"))
	if err != nil || len(b) == 0 {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(b)), "\n")

	// A guest whose init failed ends with a kernel panic and its stack dump: show what came
	// before the panic, which says why.
	end := len(lines)
	for i, l := range lines {
		if strings.Contains(l, "Kernel panic") {
			end = i + 1

			break
		}
	}

	return "\nconsole (" + filepath.Join(s.dir, "console.log") + "):\n  " +
		strings.Join(lines[max(0, end-15):end], "\n  ")
}

// Sandbox is one running microVM.
type Sandbox struct {
	name, dir, workspace, vsock string
	worker                      string // path in the guest, empty without one

	c      Config
	tap    *tapLease
	cmd    *exec.Cmd
	exited chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// Name returns the sandbox name.
func (s *Sandbox) Name() string { return s.name }

// WorkDir is where the workspace disk is mounted in the guest.
func (s *Sandbox) WorkDir() string { return "/workspace" }

// WorkerPath is the worker binary in the guest, on the agent's read-only disk.
func (s *Sandbox) WorkerPath() string { return s.worker }

// dial opens a stream to the guest agent. Firecracker maps guest vsock ports to the device's Unix
// socket: the host writes "CONNECT <port>" and reads "OK <port>" before the stream starts.
func (s *Sandbox) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer

	c, err := d.DialContext(ctx, "unix", s.vsock)
	if err != nil {
		return nil, err
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := fmt.Fprintf(c, "CONNECT %d\n", guestexec.Port); err != nil {
		_ = c.Close()

		return nil, err
	}

	// Read the answer one byte at a time: nothing past the newline may be consumed.
	line := make([]byte, 0, 32)
	b := make([]byte, 1)

	for len(line) < 64 {
		if _, err := c.Read(b); err != nil {
			_ = c.Close()

			return nil, fmt.Errorf("vsock handshake: %w", err)
		}

		if b[0] == '\n' {
			break
		}

		line = append(line, b[0])
	}

	if !bytes.HasPrefix(line, []byte("OK ")) {
		_ = c.Close()

		return nil, fmt.Errorf("vsock handshake: %q", line)
	}

	_ = c.SetDeadline(time.Time{})

	return c, nil
}

// call runs one request and fails if the guest reports an error.
func (s *Sandbox) call(ctx context.Context, req guestexec.Request, sinks map[byte]io.Writer) error {
	res, err := s.request(ctx, req, sinks)
	if err != nil {
		return err
	}

	if res.Error != "" {
		return errors.New(res.Error)
	}

	return nil
}

func (s *Sandbox) request(ctx context.Context, req guestexec.Request, sinks map[byte]io.Writer) (guestexec.Result, error) {
	return s.requestStdin(ctx, req, nil, sinks)
}

func (s *Sandbox) requestStdin(ctx context.Context, req guestexec.Request, stdin io.Reader, sinks map[byte]io.Writer) (guestexec.Result, error) {
	select {
	case <-s.exited:
		return guestexec.Result{}, errors.New("the VM is not running")
	default:
	}

	c, err := s.dial(ctx)
	if err != nil {
		return guestexec.Result{}, err
	}

	return guestexec.CallStdin(ctx, c, req, stdin, sinks)
}

// ExitError is a command that ran and exited non-zero.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("exit status %d: %s", e.Code, e.Msg)
	}

	return fmt.Sprintf("exit status %d", e.Code)
}

// Exec runs argv in the guest as the agent user (or root) and returns its output.
func (s *Sandbox) Exec(ctx context.Context, o sandbox.ExecOptions, argv ...string) (stdout, stderr []byte, err error) {
	req := guestexec.Request{Op: guestexec.OpExec, Argv: argv, Dir: o.Dir, Env: append(append([]string{}, s.c.BaseEnv...), o.Env...)}

	switch o.User {
	case "", "agent":
	case "root":
		req.Root = true
	default:
		return nil, nil, fmt.Errorf("unknown user %q (agent or root)", o.User)
	}

	so := &capped{max: s.c.MaxOutput}
	se := &capped{max: s.c.MaxOutput}

	var outSink io.Writer = so
	if o.Stdout != nil {
		outSink = o.Stdout
	}

	res, err := s.requestStdin(ctx, req, o.Stdin, map[byte]io.Writer{guestexec.Stdout: outSink, guestexec.Stderr: se})
	if err != nil {
		return so.Bytes(), se.Bytes(), err
	}

	if res.ExitCode != 0 || res.Error != "" {
		return so.Bytes(), se.Bytes(), &ExitError{Code: res.ExitCode, Msg: res.Error}
	}

	return so.Bytes(), se.Bytes(), nil
}

// SyncWorkspace makes the host workspace match the guest's /workspace, except excluded paths.
func (s *Sandbox) SyncWorkspace(ctx context.Context, exclude []string) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)

	go func() {
		err := guestexec.Mirror(s.workspace, pr, guestexec.MirrorOptions{Exclude: exclude, MaxBytes: s.c.MaxSync})

		// Drain, so the guest's writes do not block once Mirror stopped early.
		_, _ = io.Copy(io.Discard, pr)

		done <- err
	}()

	err := s.call(ctx, guestexec.Request{Op: guestexec.OpTar, Exclude: exclude},
		map[byte]io.Writer{guestexec.Data: pw})
	_ = pw.CloseWithError(err)

	if merr := <-done; err == nil {
		err = merr
	}

	if err != nil {
		return fmt.Errorf("sync workspace: %w", err)
	}

	return nil
}

// Close stops the VM and removes its run directory. The host workspace stays.
func (s *Sandbox) Close() error {
	s.closeOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)

			select {
			case <-s.exited:
			case <-time.After(10 * time.Second):
				s.closeErr = fmt.Errorf("firecracker %d did not exit", s.cmd.Process.Pid)
			}
		}

		if s.tap != nil {
			s.tap.release()
		}

		if err := os.RemoveAll(s.dir); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})

	return s.closeErr
}

// capped is a buffer that stops growing at max bytes and says so.
type capped struct {
	bytes.Buffer

	max       int
	truncated bool
}

// Write keeps what fits and reports the whole of p as written, so the stream goes on.
func (c *capped) Write(p []byte) (int, error) {
	keep := p
	if room := c.max - c.Len(); len(keep) > room {
		keep = keep[:max(room, 0)]
		c.truncated = true
	}

	_, _ = c.Buffer.Write(keep)

	return len(p), nil
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
