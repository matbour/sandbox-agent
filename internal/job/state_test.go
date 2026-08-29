package job

import (
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
