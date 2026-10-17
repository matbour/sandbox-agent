package task

import "strings"

// Decision is what the agent says about its work: ready for review, or a question for a person.
type Decision struct {
	Ready    bool
	Question string
}

// ParseDecision reads the STATUS and QUESTION lines of the agent's final message and returns the
// message without them. A missing or unknown status means the agent did not say it was ready.
func ParseDecision(result string) (Decision, string) {
	var (
		d    Decision
		keep []string
	)

	for _, l := range strings.Split(strings.ReplaceAll(result, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(t, "STATUS:"); ok {
			d.Ready = strings.EqualFold(strings.TrimSpace(v), "ready")
			continue
		}

		if v, ok := strings.CutPrefix(t, "QUESTION:"); ok {
			d.Question = strings.TrimSpace(v)
			continue
		}

		keep = append(keep, l)
	}

	if d.Ready {
		d.Question = ""
	} else if d.Question == "" {
		d.Question = "The agent did not mark this as ready for review; see its summary on the pull request."
	}

	return d, strings.Join(keep, "\n")
}

// ParseSummary extracts the PR title and body from the agent's final message.
func ParseSummary(result, fallbackTitle string) (title, body string) {
	lines := strings.Split(strings.ReplaceAll(result, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "TITLE:"); ok {
			title = strings.TrimSpace(t)
			body = strings.TrimSpace(strings.Join(lines[i+1:], "\n"))

			break
		}
	}

	if title == "" {
		title, body = fallbackTitle, strings.TrimSpace(result)
	}

	title = strings.Join(strings.Fields(title), " ")
	if r := []rune(title); len(r) > 100 {
		title = string(r[:100])
	}

	if len(body) > 20000 {
		body = body[:20000] + "\n\n…(truncated)"
	}

	return title, body
}
