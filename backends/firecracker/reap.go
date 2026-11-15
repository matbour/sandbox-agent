package firecracker

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// reapOrphans kills the microVMs whose owner process is gone (agentd crashed or was killed) and
// removes their run directories. A VM whose owner is alive is left alone, so several owners can
// share the state directory. Tap leases need nothing: the kernel drops a dead owner's flock.
func (p *Provider) reapOrphans() {
	dirs, _ := filepath.Glob(filepath.Join(p.c.StateDir, "vms", "*"))

	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}

		owner := readPID(filepath.Join(dir, "owner"))
		if owner == 0 || alive(owner) {
			continue
		}

		if vm := readPID(filepath.Join(dir, "pid")); vm != 0 && isFirecracker(vm, filepath.Base(dir)) {
			// Its own process group, as started by boot (Setpgid).
			_ = syscall.Kill(-vm, syscall.SIGKILL)
		}

		p.c.Log.Info("removed a microVM left by a dead process", "name", filepath.Base(dir), "owner", owner)

		_ = os.RemoveAll(dir)
	}
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 1 {
		return 0
	}

	return n
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// isFirecracker checks that pid is still our VM and not a recycled pid: its command line names
// firecracker and the VM's id.
func isFirecracker(pid int, name string) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}

	args := bytes.Split(b, []byte{0})

	return bytes.Contains(b, []byte("firecracker")) && containsArg(args, name)
}

func containsArg(args [][]byte, s string) bool {
	for _, a := range args {
		if string(a) == s {
			return true
		}
	}

	return false
}
