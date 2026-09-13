package job

import (
	"strings"
	"testing"

	"github.com/matbour/sandbox-agent/internal/github"
)

func TestParseDecision(t *testing.T) {
	d, rest := ParseDecision("Done.\nSTATUS: ready\nTITLE: Add x\n\nBody")
	if !d.Ready || d.Question != "" || strings.Contains(rest, "STATUS") || !strings.Contains(rest, "TITLE: Add x") {
		t.Fatalf("ready: %+v %q", d, rest)
	}

	d, rest = ParseDecision("STATUS: needs-decision\nQUESTION: Keep the old API or break it?\nTITLE: Draft")
	if d.Ready || d.Question != "Keep the old API or break it?" || strings.Contains(rest, "QUESTION") {
		t.Fatalf("needs decision: %+v %q", d, rest)
	}

	if d, _ := ParseDecision("TITLE: no status"); d.Ready || d.Question == "" {
		t.Fatalf("missing status must not be ready: %+v", d)
	}
}

func TestClassifyChecks(t *testing.T) {
	checks := []github.Check{
		{Name: "Type-check", Conclusion: "success"},
		{Name: "Vercel – example.com", Conclusion: "failure", Summary: "Deployment has failed"},
		{Name: "claude-review", Conclusion: "failure"},
		{Name: "Tests", Conclusion: "in_progress"},
		{Name: "Vercel – docs", Conclusion: "success", Summary: "Skipped - Not affected"},
	}

	failed, pending, seen := classify(checks, []string{"claude-review"})
	if len(failed) != 1 || !strings.HasPrefix(failed[0], "Vercel – example.com: Deployment") || pending != 1 || seen != 4 {
		t.Fatalf("failed=%v pending=%d seen=%d", failed, pending, seen)
	}
}
