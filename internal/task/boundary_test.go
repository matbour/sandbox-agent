package task

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportBoundary keeps what runs inside the sandbox, and the protocol, free of the manager's
// identity: no Linear, GitHub, Git or configuration (D33). Go's internal/ only hides packages from
// other modules; this test is what enforces the boundary inside ours.
func TestImportBoundary(t *testing.T) {
	// The go command that runs the tests is on PATH (make test, CI).
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}

	forbidden := []string{"internal/linear", "internal/github", "internal/gitops", "internal/config", "internal/job"}

	for _, pkg := range []string{"./internal/task", "./cmd/worker", "./cmd/guest-agent", "./cmd/sandbox", "./internal/guestexec"} {
		cmd := exec.Command(gobin, "list", "-deps", pkg)
		cmd.Dir = "../.."

		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}

		for _, dep := range strings.Fields(string(out)) {
			for _, f := range forbidden {
				if strings.HasSuffix(dep, f) || strings.Contains(dep, f+"/") {
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
		}
	}
}
