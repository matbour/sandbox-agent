package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

// StateFile is the per-job status file, rewritten at each stage.
const StateFile = "status.json"

// State is what `agentd -status` shows about a job.
type State struct {
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
	Dir      string     `json:"-"`
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

// update applies fn to dir's state and writes it back atomically. Errors are ignored: the status
// file is for humans and must never fail a job.
func update(dir string, fn func(*State)) {
	path := filepath.Join(dir, StateFile)
	var s State
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	fn(&s)
	s.Updated = time.Now().UTC()
	if s.Stage == StageDone || s.Stage == StageFailed {
		t := s.Updated
		s.Finished = &t
	}
	b, _ := json.MarshalIndent(&s, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func setStage(dir, stage string) { update(dir, func(s *State) { s.Stage = stage }) }

// Jobs returns the jobs under stateDir, newest first. A running job whose process is gone is
// reported as interrupted.
func Jobs(stateDir string) ([]State, error) {
	dirs, err := filepath.Glob(filepath.Join(stateDir, "jobs", "*", StateFile))
	if err != nil {
		return nil, err
	}
	var out []State
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s State
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		s.Dir = filepath.Dir(p)
		if !s.Terminal() && !alive(s.PID) {
			s.Stage = StageInterrupted
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
