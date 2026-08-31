package job

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateLifecycle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "jobs", "eng-1-x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	update(dir, func(s *State) {
		s.Issue, s.Model, s.Stage, s.PID, s.Started = "ENG-1", "m", StageCloning, os.Getpid(), time.Now()
	})
	setStage(dir, StageAgent)
	jobs, err := Jobs(root)
	if err != nil || len(jobs) != 1 || jobs[0].Stage != StageAgent || jobs[0].Terminal() {
		t.Fatalf("running job: %+v %v", jobs, err)
	}
	update(dir, func(s *State) { s.Stage, s.PR = StageDone, "https://example/pr/1" })
	jobs, _ = Jobs(root)
	if jobs[0].Stage != StageDone || jobs[0].Finished == nil || jobs[0].PR == "" {
		t.Fatalf("done job: %+v", jobs[0])
	}
}

func TestInterruptedJob(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "jobs", "eng-2-x")
	os.MkdirAll(dir, 0o700)
	update(dir, func(s *State) { s.Issue, s.Stage, s.PID, s.Started = "ENG-2", StageAgent, 1<<30, time.Now() })
	jobs, _ := Jobs(root)
	if jobs[0].Stage != StageInterrupted {
		t.Fatalf("expected interrupted, got %s", jobs[0].Stage)
	}
}

func TestImport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "jobs", "eng-3-x")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"issue":"ENG-3","stage":"done","started":"2026-11-13T12:00:00Z"}`), 0o600)
	n, err := Import(root)
	if err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "status.json")); !os.IsNotExist(err) {
		t.Fatal("old status file not removed")
	}
	jobs, _ := Jobs(root)
	if len(jobs) != 1 || jobs[0].Issue != "ENG-3" || jobs[0].ID != "eng-3-x" || jobs[0].Dir != dir {
		t.Fatalf("imported: %+v", jobs)
	}
	if n, _ := Import(root); n != 0 {
		t.Fatal("second import should be a no-op")
	}
}

func TestConcurrentUpdates(t *testing.T) {
	root := t.TempDir()
	done := make(chan bool)
	for i := 0; i < 20; i++ {
		go func(i int) {
			dir := filepath.Join(root, "jobs", fmt.Sprintf("job-%d", i))
			update(dir, func(s *State) { s.Issue, s.Stage = fmt.Sprint(i), StageDone })
			done <- true
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	jobs, _ := Jobs(root)
	if len(jobs) != 20 {
		t.Fatalf("lost updates: %d jobs", len(jobs))
	}
}
