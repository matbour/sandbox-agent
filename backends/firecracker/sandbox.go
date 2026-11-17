package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

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

	timings Timings
}

// Timings is where a Start spent its time. A restored VM (Restored) spends Configure loading the
// snapshot and resuming, and Guest in OpConfigure; a cold one spends Configure in the API calls up
// to InstanceStart, and Guest booting the kernel until the guest agent answers.
type Timings struct {
	Disks     time.Duration // workspace image (and the tools disk, once per binary)
	Template  time.Duration // finding the template, or building it on the first start
	Process   time.Duration // firecracker started until its API socket exists
	Configure time.Duration
	Guest     time.Duration
	Restored  bool
}

// Total is the whole Start.
func (t Timings) Total() time.Duration {
	return t.Disks + t.Template + t.Process + t.Configure + t.Guest
}

// Timings returns where Start spent its time.
func (s *Sandbox) Timings() Timings { return s.timings }

// VMMPid is the firecracker process, for measurements.
func (s *Sandbox) VMMPid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}

	return s.cmd.Process.Pid
}

// Name returns the sandbox name.
func (s *Sandbox) Name() string { return s.name }

// WorkDir is where the workspace disk is mounted in the guest.
func (s *Sandbox) WorkDir() string { return "/workspace" }

// WorkerPath is the worker binary in the guest, on the agent's read-only disk.
func (s *Sandbox) WorkerPath() string { return s.worker }

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
		s.closeErr = s.stop()

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
