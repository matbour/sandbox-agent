package gvisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Sandbox is one running gVisor container.
type Sandbox struct {
	name, container, scope string
	worker                 string

	c Config

	closeOnce sync.Once
	closeErr  error
}

// Name returns the sandbox name.
func (s *Sandbox) Name() string { return s.name }

// WorkDir is the workspace mount in the container.
func (s *Sandbox) WorkDir() string { return "/workspace" }

// WorkerPath is the worker binary in the container, mounted read-only.
func (s *Sandbox) WorkerPath() string { return s.worker }

// ExitError is a command that ran and exited non-zero.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Exec runs argv in the container, through a shell that writes its pid to a file and execs the
// command: podman exec does not stop the command when its client is killed, so a cancelled
// command is killed in the container by that pid. (busybox setsid would fork and return at once
// here, taking the output with it.)
func (s *Sandbox) Exec(ctx context.Context, o sandbox.ExecOptions, argv ...string) (stdout, stderr []byte, err error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("empty argv")
	}

	pidFile := fmt.Sprintf("/tmp/.sandbox-exec-%d", time.Now().UnixNano())

	args := []string{"--cgroup-manager=cgroupfs", "exec"}
	if o.Stdin != nil {
		args = append(args, "--interactive")
	}

	switch o.User {
	case "", "agent":
	case "root":
		args = append(args, "--user", "root")
	default:
		return nil, nil, fmt.Errorf("unknown user %q (agent or root)", o.User)
	}

	if o.Dir != "" {
		args = append(args, "--workdir", o.Dir)
	}

	for _, e := range o.Env {
		args = append(args, "--env", e)
	}

	args = append(args, s.container, "sh", "-c", `echo $$ > `+pidFile+` && exec "$@"`, "sh")
	args = append(args, argv...)

	cmd := exec.Command(s.c.Podman, args...) //nolint:gosec // args are built here

	so := &capped{max: s.c.MaxOutput}
	se := &capped{max: s.c.MaxOutput}

	cmd.Stdout, cmd.Stderr = io.Writer(so), se
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	}

	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err = <-done:
	case <-ctx.Done():
		s.kill(pidFile)
		_ = cmd.Process.Kill()

		// The client was killed: its exit error says nothing about the command.
		if werr := <-done; werr != nil {
			s.c.Log.Debug("gvisor: cancelled exec", "container", s.container, "err", werr)
		}

		return so.Bytes(), se.Bytes(), ctx.Err()
	}

	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return so.Bytes(), se.Bytes(), &ExitError{Code: ee.ExitCode()}
	}

	return so.Bytes(), se.Bytes(), err
}

// kill stops a cancelled command and its children, as root in the container. Deeper descendants
// die with the container at Close.
func (s *Sandbox) kill(pidFile string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	script := `p=$(cat ` + pidFile + ` 2>/dev/null) && { pkill -KILL -P $p; kill -KILL $p; }; rm -f ` + pidFile
	_ = exec.CommandContext(ctx, s.c.Podman, "--cgroup-manager=cgroupfs", "exec", "--user", "root", //nolint:gosec // built here
		s.container, "sh", "-c", script).Run()
}

// Close removes the container and its scope. The host workspace stays.
func (s *Sandbox) Close() error {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		out, err := exec.CommandContext(ctx, s.c.Podman, "--cgroup-manager=cgroupfs", //nolint:gosec // built here
			"rm", "--force", "--time", "0", s.container).CombinedOutput()
		if err != nil && !bytes.Contains(out, []byte("no such container")) {
			s.closeErr = fmt.Errorf("podman rm: %w: %s", err, strings.TrimSpace(string(out)))
		}

		if s.scope != "" {
			_ = exec.CommandContext(ctx, "systemctl", "--user", "stop", s.scope).Run() //nolint:gosec // built here
		}
	})

	return s.closeErr
}

// capped is a buffer that stops growing at max bytes.
type capped struct {
	bytes.Buffer

	max int
}

// Write keeps what fits and reports the whole of p as written, so the stream goes on.
func (c *capped) Write(p []byte) (int, error) {
	keep := p
	if room := c.max - c.Len(); len(keep) > room {
		keep = keep[:max(room, 0)]
	}

	_, _ = c.Buffer.Write(keep)

	return len(p), nil
}
