package job

import (
	"context"
	"log/slog"

	"github.com/matbour/sandbox-agent/internal/linear"
)

// Reporter tells people in Linear how a job is going. Errors are logged, never fatal.
type Reporter interface {
	Start(ctx context.Context, text string)
	Progress(ctx context.Context, text string) // intermediate steps; may be dropped
	Done(ctx context.Context, text string)
	Ask(ctx context.Context, text string) // the work stops on a question for a person
	Fail(ctx context.Context, text string)
}

// commentReporter posts comments on the issue, or replies in a thread when Parent is set.
// Intermediate steps are not posted: comments would be noise.
type commentReporter struct {
	lc            *linear.Client
	log           *slog.Logger
	Issue, Parent string
}

func (c commentReporter) post(ctx context.Context, text string) {
	var err error
	if c.Parent != "" {
		err = c.lc.Reply(ctx, c.Issue, c.Parent, Marker+": "+text)
	} else {
		err = c.lc.Comment(ctx, c.Issue, Marker+": "+text)
	}
	if err != nil {
		c.log.Warn("cannot comment on issue", "err", err)
	}
}

func (c commentReporter) Start(ctx context.Context, t string) { c.post(ctx, t) }
func (c commentReporter) Progress(context.Context, string)    {}
func (c commentReporter) Done(ctx context.Context, t string)  { c.post(ctx, t) }
func (c commentReporter) Fail(ctx context.Context, t string)  { c.post(ctx, t) }
func (c commentReporter) Ask(ctx context.Context, t string)   { c.post(ctx, t) }

// sessionReporter writes activities in a Linear agent session, shown live in the issue.
type sessionReporter struct {
	lc      *linear.Client
	log     *slog.Logger
	Session string
}

func (s sessionReporter) emit(ctx context.Context, kind, text string) {
	if err := s.lc.AgentActivity(ctx, s.Session, kind, text); err != nil {
		s.log.Warn("cannot post agent activity", "kind", kind, "err", err)
	}
}

func (s sessionReporter) Start(ctx context.Context, t string)    { s.emit(ctx, "thought", t) }
func (s sessionReporter) Progress(ctx context.Context, t string) { s.emit(ctx, "thought", t) }
func (s sessionReporter) Done(ctx context.Context, t string)     { s.emit(ctx, "response", t) }
func (s sessionReporter) Fail(ctx context.Context, t string)     { s.emit(ctx, "error", t) }
func (s sessionReporter) Ask(ctx context.Context, t string)      { s.emit(ctx, "elicitation", t) }

type reporterKey struct{}

func withReporter(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

var stageText = map[string]string{
	StageCloning:    "Cloning the repository.",
	StageSetup:      "Starting a sandbox and installing the dependencies.",
	StageAgent:      "Working on the code in the sandbox.",
	StageValidating: "Checking the changes.",
	StagePublishing: "Pushing to GitHub.",
	StageChecks:     "Waiting for the checks on the pull request (CI, preview deployment).",
}

// stage records the job's stage and, for agent sessions, shows it in Linear.
func stage(ctx context.Context, dir, s string) {
	setStage(dir, s)
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok && stageText[s] != "" {
		r.Progress(ctx, stageText[s])
	}
}
