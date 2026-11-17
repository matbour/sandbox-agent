package sbx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
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
	// Whether sbx exec streams stdin and stdout without a TTY is to verify on the board.
	if o.Stdin != nil || o.Stdout != nil {
		return nil, nil, sandbox.ErrNoStreaming
	}

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
