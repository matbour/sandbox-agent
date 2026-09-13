package job

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
