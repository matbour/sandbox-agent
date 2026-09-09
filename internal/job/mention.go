package job

import (
	"context"
	"regexp"
	"strings"

	"github.com/matbour/sandbox-agent/internal/linear"
)

// MentionPattern matches a mention of the agent's Linear name in a comment ("@Sandbox Agent",
// "@sandbox-agent", or a Markdown mention link), with flexible spacing and case.
func MentionPattern(name string) *regexp.Regexp {
	words := strings.Fields(name)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)@\s*` + strings.Join(words, `[\s_-]*`) + `\b`)
}

// HandleMention updates the pull request of the comment's issue, replying in the comment's thread.
func (r *Runner) HandleMention(ctx context.Context, c linear.Comment) {
	requester := c.AuthorName()
	rep := commentReporter{lc: r.Linear, log: r.Log, Issue: c.Issue.ID, Parent: c.ID}
	owner := ""
	if c.Author != nil {
		owner = c.Author.ID
	}
	if !r.UpdatePR(ctx, *c.Issue, requester, c.Body, c.ID, rep, owner) {
		r.Research(ctx, *c.Issue, requester, c.Body, rep)
	}
}
