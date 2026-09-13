package sbx

import (
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

	defer func() { _ = devnull.Close() }()

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
