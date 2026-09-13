package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds origin with main, a feature branch, and returns a clone of the feature branch
// with origin/main fetched.
func setup(t *testing.T, mainEdit, featureEdit string) (*Git, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	run(origin, "init", "-q")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(origin, "add", "-A")
	run(origin, "commit", "-qm", "base")
	run(origin, "checkout", "-qb", "agent/x")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte(featureEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	run(origin, "commit", "-qam", "feature")
	run(origin, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte(mainEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "b.txt"), []byte("new on main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(origin, "add", "-A")
	run(origin, "commit", "-qm", "main moves")
	clone := filepath.Join(root, "clone")
	run(root, "clone", "-q", "--single-branch", "--branch", "agent/x", origin, clone)
	run(clone, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	return &Git{Dir: clone}, clone
}

func TestMergeBaseClean(t *testing.T) {
	g, clone := setup(t, "one\ntwo\nthree\n", "one\ntwo\nthree\nfour\n")
	merged, conflicts, err := MergeBase(context.Background(), g, "main")
	if err != nil || !merged || len(conflicts) != 0 {
		t.Fatalf("merged=%v conflicts=%v err=%v", merged, conflicts, err)
	}
	if _, err := os.Stat(filepath.Join(clone, "b.txt")); err != nil {
		t.Fatal("main's file missing after merge")
	}
	merged, _, err = MergeBase(context.Background(), g, "main")
	if err != nil || merged {
		t.Fatalf("second merge should be a no-op: %v %v", merged, err)
	}
}

func TestMergeBaseConflict(t *testing.T) {
	g, clone := setup(t, "one\nTWO main\nthree\n", "one\nTWO feature\nthree\n")
	merged, conflicts, err := MergeBase(context.Background(), g, "main")
	if err != nil || !merged || len(conflicts) != 1 || conflicts[0] != "a.txt" {
		t.Fatalf("merged=%v conflicts=%v err=%v", merged, conflicts, err)
	}
	if out, _ := g.Run(context.Background(), "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("conflicts not committed locally: %q", out)
	}
	if err := CheckNoMarkers(clone, conflicts); err == nil {
		t.Fatal("markers not detected")
	}
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("one\nTWO both\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoMarkers(clone, conflicts); err != nil {
		t.Fatal(err)
	}
}
