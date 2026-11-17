package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// startVMM starts a firecracker process with no VM yet, in the VM's run directory.
func (s *Sandbox) startVMM() error {
	argv := []string{s.c.Firecracker, "--api-sock", apiName, "--id", s.name}
	if s.c.CPUSet != "" {
		argv = append([]string{"taskset", "-c", s.c.CPUSet}, argv...)
	}

	console, err := os.Create(filepath.Join(s.dir, "console.log"))
	if err != nil {
		return err
	}

	// Not tied to ctx: the VM outlives the call that started it, until Close.
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is built here, not from input
	cmd.Dir = s.dir
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

	// The VM's pid and its owner's, for reapOrphans after a crash.
	_ = os.WriteFile(filepath.Join(s.dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	_ = os.WriteFile(filepath.Join(s.dir, "owner"), []byte(strconv.Itoa(os.Getpid())), 0o600)

	return nil
}

// boot starts the firecracker process, configures the VM through the API and waits for the
// guest agent: a cold start.
func (s *Sandbox) boot(ctx context.Context, vm vmConfig) error {
	apiSock := filepath.Join(s.dir, apiName)

	if err := s.startVMM(); err != nil {
		return err
	}

	began := time.Now()

	if err := s.waitFor(ctx, 5*time.Second, func() error { _, err := os.Stat(apiSock); return err }); err != nil {
		return fmt.Errorf("firecracker API socket: %w%s", err, s.consoleTail())
	}

	s.timings.Process = time.Since(began)
	began = time.Now()

	if err := newAPI(apiSock).configure(ctx, vm); err != nil {
		return fmt.Errorf("configure VM: %w%s", err, s.consoleTail())
	}

	s.timings.Configure = time.Since(began)
	began = time.Now()

	defer func() { s.timings.Guest = time.Since(began) }()

	err := s.waitFor(ctx, s.c.BootTimeout, func() error {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		return s.call(pctx, guestexec.Request{Op: guestexec.OpPing}, nil)
	})
	if err != nil {
		return fmt.Errorf("guest agent: %w%s", err, s.consoleTail())
	}

	return nil
}

// waitFor retries check every 5 ms until it succeeds, the VM exits or timeout passes. The checks
// are a stat and a Unix socket connect: cheap, and the interval shows in the start time.
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
		case <-time.After(5 * time.Millisecond):
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

// stop kills the VM's process group and waits for firecracker to exit.
func (s *Sandbox) stop() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)

	select {
	case <-s.exited:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("firecracker %d did not exit", s.cmd.Process.Pid)
	}
}
