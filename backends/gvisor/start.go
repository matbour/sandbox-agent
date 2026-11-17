package gvisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

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
