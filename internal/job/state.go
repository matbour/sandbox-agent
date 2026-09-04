package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Stages a job goes through, in order. Done and Failed are terminal.
const (
	StageCloning    = "cloning"
	StageSetup      = "setup"
	StageAgent      = "agent"
	StageValidating = "validating"
	StagePublishing = "publishing"
	StageDone       = "done"
	StageFailed     = "failed"
	// StageInterrupted is never written: Jobs reports it for a job whose agentd process is gone.
	StageInterrupted = "interrupted"
)

// StateFile holds every job, in the state directory. It is the single source of truth for
// `agentd -status`, the overview and /api/jobs; per-job directories keep the logs and the patch.
const StateFile = "state.json"

// State is one job.
type State struct {
	ID       string     `json:"id"`                // the job directory name, e.g. eng-750-20261113-124431
	Kind     string     `json:"kind,omitempty"`    // "" for a new pull request, "update" for a follow-up
	Comment  string     `json:"comment,omitempty"` // Linear comment that asked for the update
	Issue    string     `json:"issue"`
	Title    string     `json:"title"`
	URL      string     `json:"url,omitempty"`
	Model    string     `json:"model"`
	Stage    string     `json:"stage"`
	Sandbox  string     `json:"sandbox,omitempty"`
	PID      int        `json:"pid"`
	Started  time.Time  `json:"started"`
	Updated  time.Time  `json:"updated"`
	Finished *time.Time `json:"finished,omitempty"`
	PR       string     `json:"pr,omitempty"`
	CostUSD  float64    `json:"cost_usd,omitempty"`
	Turns    int        `json:"turns,omitempty"`
	Error    string     `json:"error,omitempty"`
	Dir      string     `json:"dir"`
}

type stateFile struct {
	Version int     `json:"version"`
	Jobs    []State `json:"jobs"`
	// Mentions: comments created before Since are ignored (set on first run, so an old backlog
	// is not replayed); Handled lists the comment IDs already taken, newest last.
	// Sessions are Linear agent sessions received by webhook and not yet handled.
	Sessions []Session `json:"sessions,omitempty"`
	Mentions struct {
		Since   time.Time `json:"since"`
		Handled []string  `json:"handled"`
	} `json:"mentions"`
}

// MentionWatermark returns the time before which mentions are ignored, setting it to now the
// first time.
func MentionWatermark(root string) time.Time {
	var t time.Time
	_ = withState(root, true, func(sf *stateFile) error {
		if sf.Mentions.Since.IsZero() {
			sf.Mentions.Since = time.Now().UTC()
		}
		t = sf.Mentions.Since
		return nil
	})
	return t
}

// ClaimMention records a comment as handled. It returns false if it already was.
func ClaimMention(root, id string) bool {
	claimed := false
	_ = withState(root, true, func(sf *stateFile) error {
		for _, h := range sf.Mentions.Handled {
			if h == id {
				return nil
			}
		}
		sf.Mentions.Handled = append(sf.Mentions.Handled, id)
		if n := len(sf.Mentions.Handled); n > 1000 {
			sf.Mentions.Handled = sf.Mentions.Handled[n-1000:]
		}
		claimed = true
		return nil
	})
	return claimed
}

// LastPR returns the pull request URL and model of the latest successful job for an issue.
func LastPR(root, issue string) (pr, model string) {
	jobs, _ := Jobs(root)
	for _, j := range jobs {
		if strings.EqualFold(j.Issue, issue) && j.PR != "" {
			return j.PR, j.Model
		}
	}
	return "", ""
}

// Terminal reports whether the job has finished, one way or another.
func (s *State) Terminal() bool {
	return s.Stage == StageDone || s.Stage == StageFailed || s.Stage == StageInterrupted
}

// Elapsed is the run time so far, or the total run time once finished.
func (s *State) Elapsed(now time.Time) time.Duration {
	end := now
	if s.Finished != nil {
		end = *s.Finished
	} else if s.Stage == StageInterrupted {
		end = s.Updated
	}
	return end.Sub(s.Started).Round(time.Second)
}

// stateRoot is the state directory a job directory belongs to (<root>/jobs/<id>).
func stateRoot(dir string) string { return filepath.Dir(filepath.Dir(dir)) }

// withState runs fn on the state file under an exclusive lock, so agentd and a concurrent
// `agentd -try` never lose each other's updates. fn's changes are written atomically.
func withState(root string, write bool, fn func(*stateFile) error) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, StateFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	how := syscall.LOCK_SH
	if write {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(lock.Fd()), how); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	path := filepath.Join(root, StateFile)
	var sf stateFile
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &sf); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := fn(&sf); err != nil || !write {
		return err
	}
	sf.Version = 1
	out, err := json.MarshalIndent(&sf, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// update applies fn to the job stored for dir, creating it if needed. Errors are ignored: the
// state is for humans and must never fail a job.
func update(dir string, fn func(*State)) {
	_ = withState(stateRoot(dir), true, func(sf *stateFile) error {
		id := filepath.Base(dir)
		i := -1
		for k := range sf.Jobs {
			if sf.Jobs[k].ID == id {
				i = k
				break
			}
		}
		if i < 0 {
			sf.Jobs = append(sf.Jobs, State{ID: id, Dir: dir})
			i = len(sf.Jobs) - 1
		}
		s := &sf.Jobs[i]
		fn(s)
		s.Updated = time.Now().UTC()
		if (s.Stage == StageDone || s.Stage == StageFailed) && s.Finished == nil {
			t := s.Updated
			s.Finished = &t
		}
		return nil
	})
}

func setStage(dir, stage string) { update(dir, func(s *State) { s.Stage = stage }) }

// Jobs returns the jobs, newest first. A running job whose process is gone is reported as
// interrupted.
func Jobs(stateDir string) ([]State, error) {
	var out []State
	err := withState(stateDir, false, func(sf *stateFile) error {
		out = append(out, sf.Jobs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		if !out[i].Terminal() && !alive(out[i].PID) {
			out[i].Stage = StageInterrupted
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Import adds jobs from the per-job status.json files written by earlier versions, and removes
// those files. Jobs already in the state file are kept as they are.
func Import(stateDir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(stateDir, "jobs", "*", "status.json"))
	if err != nil || len(files) == 0 {
		return 0, err
	}
	n := 0
	err = withState(stateDir, true, func(sf *stateFile) error {
		have := map[string]bool{}
		for _, j := range sf.Jobs {
			have[j.ID] = true
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var s State
			if json.Unmarshal(b, &s) != nil {
				continue
			}
			s.Dir = filepath.Dir(f)
			s.ID = filepath.Base(s.Dir)
			if !have[s.ID] {
				sf.Jobs = append(sf.Jobs, s)
				n++
			}
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	return n, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// Session is a Linear agent session event waiting for the worker: the agent was mentioned,
// delegated an issue, or sent a follow-up message in a session.
type Session struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"` // created or prompted
	IssueID   string    `json:"issue_id"`
	Issue     string    `json:"issue"` // identifier, for logs
	CommentID string    `json:"comment_id,omitempty"`
	Request   string    `json:"request,omitempty"` // the comment or message text
	Requester string    `json:"requester,omitempty"`
	Received  time.Time `json:"received"`
}

// EnqueueSession stores a session event for the worker. A comment that opened a session is also
// marked as a handled mention, so it is not taken a second time by the comment poller.
func EnqueueSession(root string, s Session) {
	_ = withState(root, true, func(sf *stateFile) error {
		for _, p := range sf.Sessions {
			if p.ID == s.ID && p.Action == s.Action && p.Request == s.Request {
				return nil
			}
		}
		sf.Sessions = append(sf.Sessions, s)
		if s.CommentID != "" {
			sf.Mentions.Handled = append(sf.Mentions.Handled, s.CommentID)
		}
		return nil
	})
}

// TakeSessions removes and returns the waiting sessions, oldest first.
func TakeSessions(root string) []Session {
	var out []Session
	_ = withState(root, true, func(sf *stateFile) error {
		out, sf.Sessions = sf.Sessions, nil
		return nil
	})
	return out
}

// MentionHandled reports whether a comment was already taken (by the poller or a session).
func MentionHandled(root, id string) bool {
	found := false
	_ = withState(root, false, func(sf *stateFile) error {
		for _, h := range sf.Mentions.Handled {
			if h == id {
				found = true
			}
		}
		return nil
	})
	return found
}
