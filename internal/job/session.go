package job

import (
	"context"
	"strings"
)

// HandleSession answers a Linear agent session: it updates the issue's agent pull request if
// there is an open one, and otherwise opens a new one. Progress shows in the session.
func (r *Runner) HandleSession(ctx context.Context, s Session, overview string) {
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
