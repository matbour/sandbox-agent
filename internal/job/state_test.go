package job

import (
	"context"
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	update(dir, func(s *State) { s.Issue, s.Stage, s.PID, s.Started = "ENG-2", StageAgent, 1<<30, time.Now() })
	jobs, _ := Jobs(root)
	if jobs[0].Stage != StageInterrupted {
		t.Fatalf("expected interrupted, got %s", jobs[0].Stage)
	}
}

func TestImport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "jobs", "eng-3-x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"issue":"ENG-3","stage":"done","started":"2026-11-13T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
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

func TestSessionQueue(t *testing.T) {
	root := t.TempDir()
	EnqueueSession(root, Session{ID: "s1", Action: "created", CommentID: "c1"})
	EnqueueSession(root, Session{ID: "s1", Action: "created", CommentID: "c1"}) // duplicate delivery
	EnqueueSession(root, Session{ID: "s1", Action: "prompted", Request: "more"})
	if !MentionHandled(root, "c1") {
		t.Fatal("session comment not marked as handled")
	}
	got := TakeSessions(root)
	if len(got) != 2 || got[0].Action != "created" || got[1].Request != "more" {
		t.Fatalf("got %+v", got)
	}
	if len(TakeSessions(root)) != 0 {
		t.Fatal("sessions not removed")
	}
}

func TestStopSessionAndDrop(t *testing.T) {
	root := t.TempDir()
	EnqueueSession(root, Session{ID: "s1", Action: "prompted", Request: "a"})
	EnqueueSession(root, Session{ID: "s2", Action: "created"})
	if n := DropSessions(root, "s1"); n != 1 {
		t.Fatalf("dropped %d", n)
	}
	if got := TakeSessions(root); len(got) != 1 || got[0].ID != "s2" {
		t.Fatalf("left %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running.Store("s3", cancel)
	if !StopSession("s3") || ctx.Err() == nil || StopSession("s3") {
		t.Fatal("StopSession did not cancel exactly once")
	}
}

func TestTakeSessionOneByOne(t *testing.T) {
	root := t.TempDir()
	EnqueueSession(root, Session{ID: "a", Action: "created"})
	EnqueueSession(root, Session{ID: "b", Action: "created"})
	s, ok := TakeSession(root)
	if !ok || s.ID != "a" || len(Queued(root)) != 1 || Queued(root)[0].ID != "b" {
		t.Fatalf("took %+v, left %+v", s, Queued(root))
	}
}
