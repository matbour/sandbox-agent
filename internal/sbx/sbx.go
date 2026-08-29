// Package sbx drives Docker Sandboxes (the sbx CLI). See docs/docker-sandboxes.md.
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
)

// Sandbox is one microVM created for one job.
type Sandbox struct {
	Name      string
	Workspace string
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
	return &Sandbox{Name: name, Workspace: workspace}, nil
}

// Exec runs argv in the sandbox, in the workspace, and returns stdout and stderr.
func (s *Sandbox) Exec(ctx context.Context, user string, argv ...string) (stdout, stderr []byte, err error) {
	args := []string{"exec", "-w", s.Workspace}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, s.Name)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "sbx", args...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	return o.Bytes(), e.Bytes(), err
}

// Remove destroys the sandbox. It uses a fresh context so it runs even after a timeout.
func (s *Sandbox) Remove() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := run(ctx, nil, "rm", "--force", s.Name)
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
