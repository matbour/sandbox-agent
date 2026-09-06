package job

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/matbour/sandbox-agent/internal/linear"
)

// Try runs a task on a local Git repository, without Linear or GitHub, and leaves the result in
// the job directory (changes.patch, agent.json). Nothing is pushed.
func (r *Runner) Try(ctx context.Context, localRepo, task, model string) (*Execution, string, error) {
	w, err := r.newWorkdir("try", "")
	if err != nil {
		return nil, "", err
	}
	dir, repoDir, ws, stamp := w.dir, w.repo, w.ws, w.stamp
	// Plain local clone: the source is the operator's own repository, not agent output.
	if b, err := exec.CommandContext(ctx, "git", "clone", "-q", "--no-hardlinks", localRepo, repoDir).CombinedOutput(); err != nil {
		return nil, dir, fmt.Errorf("git clone: %w: %s", err, b)
	}
	if err := prepare(ctx, repoDir, ws); err != nil {
		return nil, dir, err
	}
	var is linear.Issue
	is.Identifier, is.Title, is.Description = "TRY-1", firstLine(task), task
	update(dir, func(st *State) {
		st.Issue, st.Title, st.Model = "try", is.Title, model
		st.Stage, st.PID, st.Started = StageSetup, os.Getpid(), time.Now().UTC()
	})
	ex, err := r.Execute(ctx, is, model, dir, repoDir, ws, "try", stamp)
	update(dir, func(st *State) {
		if ex != nil {
			st.CostUSD, st.Turns = ex.CostUSD, ex.Turns
		}
		if err != nil {
			st.Stage, st.Error = StageFailed, truncate(err.Error(), 2000)
		} else {
			st.Stage = StageDone
		}
	})
	return ex, dir, err
}
