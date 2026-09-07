package sbx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/sandboxtest"
)

func TestConformance(t *testing.T) {
	if os.Getenv("SANDBOX_IT") == "" {
		t.Skip("starts real sandboxes; set SANDBOX_IT=1")
	}
	p, err := New(sandbox.Options{CPUSet: "4-7"})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".local/state/sandbox-agent/conformance")
	os.MkdirAll(root, 0o700)
	sandboxtest.Run(t, p, root)
}
