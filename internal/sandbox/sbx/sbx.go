// Package sbx is the Docker Sandboxes provider (the sbx CLI): one KVM microVM per sandbox, egress
// through a host proxy that injects the API key. See docs/docker-sandboxes.md.
package sbx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Sandbox is one microVM created for one job.
type Sandbox struct {
	name      string
	workspace string
	cpus      string // host cores the daemon is pinned to, for RemoveHard
}

// Name returns the sandbox name.
func (s *Sandbox) Name() string { return s.name }

// WorkDir is the workspace path inside the VM: sbx mounts it at the same path as on the host.
func (s *Sandbox) WorkDir() string { return s.workspace }

// Close removes the sandbox, restarting the daemon if sbx left it half-deleted.
func (s *Sandbox) Close() error { return s.RemoveHard(s.cpus) }

// Provider starts Docker Sandboxes.
type Provider struct{ CPUSet string }

// New builds the provider.
func New(o sandbox.Options) (sandbox.Provider, error) {
	if o.CPUSet == "" {
		return nil, fmt.Errorf("sbx: a CPU set is required (CPUS)")
	}
	return &Provider{CPUSet: o.CPUSet}, nil
}

// Name returns "sbx".
func (p *Provider) Name() string { return "sbx" }

// Prepare makes sure the sbx daemon runs pinned to the CPU set, so the VMs inherit it.
func (p *Provider) Prepare(ctx context.Context) error { return EnsureDaemon(ctx, p.CPUSet) }

// Start creates a Claude sandbox on the workspace.
func (p *Provider) Start(ctx context.Context, sp sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := Create(ctx, sp.Name, sp.Workspace, Options{CPUs: sp.CPUs, Memory: sp.Memory, DenyNetwork: sp.DenyNetwork})
	if err != nil {
		return nil, err
	}
	sb.cpus = p.CPUSet
	return sb, nil
}

// Options for Create.
type Options struct {
	CPUs        int
	Memory      string
	DenyNetwork []string
}

// Create starts a Claude sandbox on workspace (mounted read-write at the same path).
func Create(ctx context.Context, name, workspace string, o Options) (*Sandbox, error) {
	args := []string{"create", "--name", name, "--skills", "off", "--static-mcp", "none",
		"--no-progress", "--no-input"}
	if o.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(o.CPUs))
	}
	if o.Memory != "" {
		args = append(args, "-m", o.Memory)
	}
	for _, h := range o.DenyNetwork {
		args = append(args, "--deny-network", h)
	}
	args = append(args, "claude", workspace)
	if out, err := run(ctx, nil, args...); err != nil {
		return nil, fmt.Errorf("sbx create: %w: %s", err, tail(out))
	}
	return &Sandbox{name: name, workspace: workspace}, nil
}

// Exec runs argv in the sandbox and returns stdout and stderr.
func (s *Sandbox) Exec(ctx context.Context, o sandbox.ExecOptions, argv ...string) (stdout, stderr []byte, err error) {
	dir := o.Dir
	if dir == "" {
		dir = s.workspace
	}
	args := []string{"exec", "-w", dir}
	if o.User != "" {
		args = append(args, "-u", o.User)
	}
	for _, e := range o.Env {
		args = append(args, "-e", e)
	}
	args = append(args, s.name)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "sbx", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// TrimCache starts a loop, as root in the VM, that drops the guest's page cache every interval.
// A VM does not give its file cache back to the host, so a job that reads or writes gigabytes
// (node_modules) would otherwise keep its whole memory cap; dropped pages are returned to the host
// through free page reporting within a second. Measured: 2.3 GB down to 0.8 GB after 1 GB of I/O.
func (s *Sandbox) TrimCache(ctx context.Context, interval time.Duration) error {
	loop := fmt.Sprintf("while :; do sleep %d; sync; echo 1 > /proc/sys/vm/drop_caches; done", int(interval.Seconds()))
	_, se, err := s.Exec(ctx, sandbox.ExecOptions{User: "root"}, "sh", "-c", "nohup sh -c '"+loop+"' </dev/null >/dev/null 2>&1 &")
	if err != nil {
		return fmt.Errorf("start cache trimmer: %w: %s", err, tail(se))
	}
	return nil
}

// DropCache drops the guest's page cache once (after an install, for example).
func (s *Sandbox) DropCache(ctx context.Context) {
	_, _, _ = s.Exec(ctx, sandbox.ExecOptions{User: "root"}, "sh", "-c", "sync; echo 1 > /proc/sys/vm/drop_caches")
}

// Remove destroys the sandbox. It uses a fresh context so it runs even after a timeout.
func (s *Sandbox) Remove() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := run(ctx, nil, "rm", "--force", s.name)
	if err != nil {
		return fmt.Errorf("sbx rm: %w: %s", err, tail(out))
	}
	return nil
}

// RemoveHard removes the sandbox and, if sbx reports a partial delete (seen after a VM crash:
// "volume is still referenced"), restarts the daemon pinned to cpus and tries again.
func (s *Sandbox) RemoveHard(cpus string) error {
	err := s.Remove()
	if err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, serr := run(ctx, nil, "daemon", "stop"); serr != nil {
		return fmt.Errorf("%w; daemon stop: %v: %s", err, serr, tail(out))
	}
	if derr := EnsureDaemon(ctx, cpus); derr != nil {
		return fmt.Errorf("%w; daemon restart: %v", err, derr)
	}
	return s.Remove()
}

// EnsureDaemon makes sure the sbx daemon runs pinned to cpus, so every VM it starts inherits that
// affinity. A daemon with another affinity is restarted, which stops running sandboxes: call it
// only when no job is running.
func EnsureDaemon(ctx context.Context, cpus string) error {
	pid, allowed := findDaemon()
	if pid > 0 && allowed == cpus {
		return nil
	}
	if pid > 0 {
		if out, err := run(ctx, nil, "daemon", "stop"); err != nil {
			return fmt.Errorf("sbx daemon stop: %w: %s", err, tail(out))
		}
	}
	cmd := exec.Command("taskset", "-c", cpus, "sbx", "daemon", "start")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sbx daemon: %w", err)
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if pid, allowed = findDaemon(); pid > 0 && allowed == cpus {
			if _, err := run(ctx, nil, "daemon", "status"); err == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("sbx daemon not running on cpus %s (found pid %d, cpus %q)", cpus, pid, allowed)
}

// findDaemon scans /proc for "sbx daemon start" owned by us and returns its pid and allowed CPUs.
func findDaemon() (int, string) {
	entries, _ := os.ReadDir("/proc")
	uid := os.Getuid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(argv) < 3 || filepath.Base(argv[0]) != "sbx" || argv[1] != "daemon" || argv[2] != "start" {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(dir, &st) != nil || int(st.Uid) != uid {
			continue
		}
		status, _ := os.ReadFile(filepath.Join(dir, "status"))
		for _, line := range strings.Split(string(status), "\n") {
			if v, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
				return pid, strings.TrimSpace(v)
			}
		}
		return pid, ""
	}
	return 0, ""
}

func run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sbx", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

var (
	_ sandbox.Provider     = (*Provider)(nil)
	_ sandbox.Sandbox      = (*Sandbox)(nil)
	_ sandbox.CacheTrimmer = (*Sandbox)(nil)
)
