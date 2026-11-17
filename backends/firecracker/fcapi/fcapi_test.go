package fcapi

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedUpToDate: types.go is what the generator makes from firecracker.yaml, so neither
// is edited without the other.
func TestGeneratedUpToDate(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}

	out := filepath.Join(t.TempDir(), "types.go")
	if b, err := exec.Command(gobin, "run", "./gen", "firecracker.yaml", out).CombinedOutput(); err != nil {
		t.Fatalf("gen: %v: %s", err, b)
	}

	want, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Error("types.go is out of date: run go generate ./backends/firecracker/fcapi")
	}
}
