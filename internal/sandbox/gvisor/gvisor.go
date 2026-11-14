// Package gvisor runs sandboxes in gVisor (runsc), through rootless Podman (D51).
//
// One sandbox is one Podman container whose OCI runtime is runsc: the agent's system calls reach
// gVisor's user-space kernel, not the host's. Podman provides what rootless runsc alone cannot: a
// full user namespace from /etc/subuid, so --userns=keep-id makes the agent (UID 1000 in the
// image) the host account, and the workspace a plain bind mount, shared both ways. Its rootless
// network (pasta) carries gVisor's own network stack (runsc --network=sandbox).
//
// Podman cannot create cgroups for runsc here (the systemd manager needs an interactive
// authorization, and runsc ignores cgroups), so each sandbox runs in a transient systemd scope
// of the account's delegated tree, which carries the memory and CPU limits: every process of the
// sandbox is born in it.
//
// runsc must be allowed user namespaces by AppArmor (docs/host.md).
package gvisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Config locates the tools and the image. Zero values get the defaults of Defaults.
type Config struct {
	Podman    string // the podman binary
	Runsc     string // runsc; resolved to its real path, which the AppArmor profile names
	Image     string // the agent image (scripts/build-rootfs.sh)
	Worker    string // cmd/worker for the image's architecture; optional
	Network   string // podman network: "pasta", or "none"
	CPUSet    string // host cores for the sandboxes (taskset)
	MaxOutput int    // per stream of one Exec
	Log       *slog.Logger
}

// Defaults fills Config from the repository layout: binaries in <root>/bin (or
// <root>/bin/linux-<arch>).
func Defaults(c Config, root string) Config {
	arch := runtime.GOARCH

	bin := filepath.Join(root, "bin")
	if _, err := os.Stat(filepath.Join(bin, "worker")); err != nil {
		bin = filepath.Join(root, "bin", "linux-"+arch)
	}

	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}

	def(&c.Podman, "podman")
	def(&c.Runsc, "runsc")
	def(&c.Image, "localhost/sandbox-agent-rootfs:"+arch)
	def(&c.Worker, filepath.Join(bin, "worker"))
	def(&c.Network, "pasta")

	if c.MaxOutput == 0 {
		c.MaxOutput = 256 << 20
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

	return &Provider{c: Defaults(Config{CPUSet: o.CPUSet, Log: slog.Default()}, root)}, nil
}

// NewWith builds a provider from an explicit configuration (tests).
func NewWith(c Config) *Provider { return &Provider{c: c} }

// Provider starts gVisor sandboxes.
type Provider struct {
	c Config
}

// Name returns "gvisor".
func (p *Provider) Name() string { return "gvisor" }

// CarriesWorker reports whether the worker binary is there to mount in the sandbox.
func (p *Provider) CarriesWorker() bool {
	_, err := os.Stat(p.c.Worker)

	return err == nil
}

// Prepare checks the tools and the image, and removes the sandboxes of processes that died
// without closing them.
func (p *Provider) Prepare(ctx context.Context) error {
	if _, err := exec.LookPath(p.c.Podman); err != nil {
		return fmt.Errorf("podman: %w", err)
	}

	if _, err := p.runsc(); err != nil {
		return err
	}

	if _, err := p.podman(ctx, "image", "exists", p.c.Image); err != nil {
		return fmt.Errorf("image %s: not found (scripts/build-rootfs.sh)", p.c.Image)
	}

	p.reapOrphans(ctx)

	return nil
}

// runsc returns runsc's real path: Podman runs it by that path, which is the one the AppArmor
// profile allows user namespaces to.
func (p *Provider) runsc() (string, error) {
	path, err := exec.LookPath(p.c.Runsc)
	if err != nil {
		return "", fmt.Errorf("runsc: %w", err)
	}

	return filepath.EvalSymlinks(path)
}

const (
	labelProvider = "sandbox-agent.provider=gvisor"
	labelOwner    = "sandbox-agent.owner"
	workerPath    = "/run/agent/worker"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Start creates the container and starts it, idle: commands run in it with podman exec.
func (p *Provider) Start(ctx context.Context, s sandbox.Spec) (sandbox.Sandbox, error) {
	if !validName.MatchString(s.Name) {
		return nil, fmt.Errorf("invalid sandbox name %q", s.Name)
	}

	runsc, err := p.runsc()
	if err != nil {
		return nil, err
	}

	if len(s.DenyNetwork) > 0 {
		p.c.Log.Warn("gvisor: per-sandbox network denials are not implemented", "hosts", s.DenyNetwork)
	}

	sb := &Sandbox{name: s.Name, container: "sandbox-agent-" + s.Name, c: p.c}

	args := []string{"--cgroup-manager=cgroupfs", "run", "--detach", "--name", sb.container,
		"--label", labelProvider, "--label", fmt.Sprintf("%s=%d", labelOwner, os.Getpid()),
		"--hostname", s.Name,
		"--runtime", runsc, "--runtime-flag", "ignore-cgroups",
		"--userns", "keep-id",
		"--volume", s.Workspace + ":/workspace:rw", "--workdir", "/workspace",
		"--entrypoint", "sleep",
	}

	if p.c.Network == "none" {
		args = append(args, "--network", "none", "--runtime-flag", "network=none")
	} else {
		args = append(args, "--network", p.c.Network, "--runtime-flag", "network=sandbox")
	}

	if p.CarriesWorker() {
		args = append(args, "--volume", p.c.Worker+":"+workerPath+":ro")
		sb.worker = workerPath
	}

	args = append(args, p.c.Image, "infinity")

	argv, err := p.scoped(s, sb.container, args)
	if err != nil {
		return nil, err
	}

	if s.CPUs > 0 || s.Memory != "" {
		sb.scope = sb.container + ".scope"
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is built here
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = sb.Close()

		return nil, fmt.Errorf("podman run: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return sb, nil
}

// scoped wraps the podman command in a transient systemd scope that carries the sandbox's limits,
// and in taskset for the CPU set. The scope outlives the podman client: conmon and the sandbox's
// processes stay in it.
func (p *Provider) scoped(s sandbox.Spec, unit string, args []string) ([]string, error) {
	argv := append([]string{p.c.Podman}, args...)

	if p.c.CPUSet != "" {
		argv = append([]string{"taskset", "-c", p.c.CPUSet}, argv...)
	}

	var props []string

	if s.Memory != "" {
		b, err := parseBytes(s.Memory)
		if err != nil {
			return nil, err
		}

		props = append(props, "-p", fmt.Sprintf("MemoryMax=%d", b), "-p", "MemorySwapMax=0")
	}

	if s.CPUs > 0 {
		props = append(props, "-p", fmt.Sprintf("CPUQuota=%d%%", 100*s.CPUs))
	}

	if len(props) == 0 {
		return argv, nil
	}

	pre := append([]string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit", unit}, props...)

	return append(append(pre, "--"), argv...), nil
}

// podman runs one podman command and returns its output.
func (p *Provider) podman(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.c.Podman, append([]string{"--cgroup-manager=cgroupfs"}, args...)...) //nolint:gosec // args are built here

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("podman %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return out, nil
}

// reapOrphans removes the sandboxes whose owner process is gone.
func (p *Provider) reapOrphans(ctx context.Context) {
	out, err := p.podman(ctx, "ps", "--all", "--filter", "label="+labelProvider,
		"--format", `{{.Names}} {{index .Labels "`+labelOwner+`"}}`)
	if err != nil {
		return
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, owner, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		pid, err := strconv.Atoi(owner)
		if err == nil && pid != os.Getpid() && syscall.Kill(pid, 0) == nil {
			continue
		}

		if pid == os.Getpid() {
			continue
		}

		p.c.Log.Info("gvisor: removing an orphan sandbox", "container", name)
		_, _ = p.podman(ctx, "rm", "--force", "--time", "0", name)
	}
}

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

// parseBytes reads "1g", "512m" or a number of MiB.
func parseBytes(m string) (int64, error) {
	m = strings.ToLower(strings.TrimSpace(m))

	mult := int64(1 << 20)

	switch {
	case strings.HasSuffix(m, "g"):
		mult, m = 1<<30, strings.TrimSuffix(m, "g")
	case strings.HasSuffix(m, "m"):
		m = strings.TrimSuffix(m, "m")
	}

	n, err := strconv.ParseInt(m, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory %q", m)
	}

	return n * mult, nil
}

var (
	_ sandbox.Provider      = (*Provider)(nil)
	_ sandbox.WorkerCarrier = (*Provider)(nil)
	_ sandbox.Sandbox       = (*Sandbox)(nil)
	_ sandbox.WorkerHost    = (*Sandbox)(nil)
)
