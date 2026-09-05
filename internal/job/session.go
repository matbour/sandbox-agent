package job

import (
	"context"
	"strings"
	"sync"
)

// running maps a session ID to the cancel function of its job, for the Stop signal.
var running sync.Map

// StopSession cancels the job running for a session, if any. Cancelling stops the agent and
// removes the sandbox; nothing is pushed.
func StopSession(id string) bool {
	if c, ok := running.LoadAndDelete(id); ok {
		c.(context.CancelFunc)()
		return true
	}
	return false
}

// HandleSession answers a Linear agent session. With an agent pull request on the issue, it
// updates it. Otherwise, when the issue is delegated to the agent, it opens one; when the agent
// was only mentioned (or messaged in the session), it researches the question and answers with
// notes. Progress shows in the session.
func (r *Runner) HandleSession(ctx context.Context, s Session, overview, appUserID string) {
	ctx, cancel := context.WithCancel(ctx)
	running.Store(s.ID, cancel)
	defer func() {
		running.Delete(s.ID)
		cancel()
	}()
	rep := sessionReporter{lc: r.Linear, log: r.Log, Session: s.ID}
	if overview != "" {
		_ = r.Linear.AgentSessionLink(ctx, s.ID, "sandbox-agent jobs", overview)
	}
	is, err := r.Linear.IssueByID(ctx, s.IssueID)
	if err != nil {
		rep.Fail(ctx, "cannot read the issue: "+err.Error())
		return
	}
	requester := s.Requester
	if requester == "" {
		requester = "someone"
	}
	if r.UpdatePR(ctx, *is, requester, s.Request, s.ID, rep, s.RequesterID) {
		return
	}
	if is.Delegate == nil || is.Delegate.ID != appUserID {
		r.Research(ctx, *is, requester, s.Request, rep)
		return
	}
	var words []string
	for _, w := range strings.Fields(s.Request) {
		words = append(words, strings.Trim(w, "`.,;:!?()[]"))
	}
	model, ok := r.Cfg.ModelFor(words)
	if !ok {
		model = r.Cfg.AgentModel
	}
	if out := r.NewPR(ctx, *is, model, rep, false, s.RequesterID); out != nil && out.PR != nil {
		_ = r.Linear.AgentSessionLink(ctx, s.ID, "Pull request", out.PR.HTMLURL)
	}
}
