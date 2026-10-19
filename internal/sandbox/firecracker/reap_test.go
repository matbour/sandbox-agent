package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReapOrphans(t *testing.T) {
	state := t.TempDir()
	p := NewWith(Defaults(Config{StateDir: state, Taps: []string{}}, t.TempDir()))

	// A dead owner: a process that has exited.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}

	mk := func(name string, owner int) string {
		dir := filepath.Join(state, "vms", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "owner"), []byte(itoa(owner)), 0o600); err != nil {
			t.Fatal(err)
		}

		return dir
	}

	orphan := mk("orphan", dead.Process.Pid)
	mine := mk("mine", os.Getpid())

	p.reapOrphans()

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("the orphan's directory is still there")
	}

	if _, err := os.Stat(mine); err != nil {
		t.Error("a live owner's VM was removed")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
