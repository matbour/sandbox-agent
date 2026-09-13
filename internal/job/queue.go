package job

import (
	"strings"
	"time"
)

// Session is a Linear agent session event waiting for the worker: the agent was mentioned,
// delegated an issue, or sent a follow-up message in a session.
type Session struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"` // created or prompted
	IssueID     string    `json:"issue_id"`
	Issue       string    `json:"issue"` // identifier, for logs
	CommentID   string    `json:"comment_id,omitempty"`
	Request     string    `json:"request,omitempty"` // the comment or message text
	Requester   string    `json:"requester,omitempty"`
	RequesterID string    `json:"requester_id,omitempty"` // Linear user to hand the issue back to
	Received    time.Time `json:"received"`
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

// TakeSession removes and returns the oldest waiting session. The others stay in the state file,
// visible in the overview and safe across restarts.
func TakeSession(root string) (Session, bool) {
	var out Session

	ok := false
	_ = withState(root, true, func(sf *stateFile) error {
		if len(sf.Sessions) > 0 {
			out, sf.Sessions, ok = sf.Sessions[0], sf.Sessions[1:], true
		}

		return nil
	})

	return out, ok
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

// DropSessions removes waiting events of a session, e.g. after a stop signal.
func DropSessions(root, id string) int {
	n := 0
	_ = withState(root, true, func(sf *stateFile) error {
		keep := sf.Sessions[:0]
		for _, s := range sf.Sessions {
			if s.ID == id {
				n++
				continue
			}

			keep = append(keep, s)
		}

		sf.Sessions = keep

		return nil
	})

	return n
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

// Queued returns the agent sessions waiting for the worker, oldest first.
func Queued(root string) []Session {
	var out []Session

	_ = withState(root, false, func(sf *stateFile) error {
		out = append(out, sf.Sessions...)
		return nil
	})

	return out
}

// Kind describes what a queued session will do, as far as can be told before running it.
func (s Session) Kind() string {
	if strings.HasPrefix(s.Request, "This thread is for an agent session") {
		return "delegated"
	}

	return "mentioned"
}
