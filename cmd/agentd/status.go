package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/matbour/sandbox-agent/internal/job"
)

// printStatus shows the service, the running sandboxes and the last jobs.
func printStatus(w io.Writer, stateDir string, limit int) error {
	p := &printer{w: w}
	svc := "unknown"
	if out, err := exec.Command("systemctl", "--user", "is-active", "sandbox-agent").Output(); err == nil || len(out) > 0 {
		svc = strings.TrimSpace(string(out))
	}
	p.printf("service sandbox-agent: %s\n", svc)
	if out, err := exec.Command("sbx", "ls").Output(); err == nil {
		var names []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n")[1:] {
			if f := strings.Fields(line); len(f) > 0 {
				names = append(names, f[0])
			}
		}
		if len(names) == 0 {
			names = []string{"none"}
		}
		p.printf("sandboxes: %s\n", strings.Join(names, ", "))
	}
	if q := job.Queued(stateDir); len(q) > 0 {
		p.printf("queued: %d\n", len(q))
		for _, s := range q {
			p.printf("  %-8s %-9s since %s  %s\n", s.Issue, s.Kind(), s.Received.Local().Format("15:04"), cut(firstLine(s.Request), 60))
		}
	}
	jobs, err := job.Jobs(stateDir)
	if err != nil {
		return err
	}
	p.printf("\n")
	if len(jobs) == 0 {
		p.printf("no jobs yet\n")
		return p.err
	}
	if p.err != nil {
		return p.err
	}
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	tp := &printer{w: tw}
	tp.printf("ISSUE\tMODEL\tSTAGE\tSTARTED\tTIME\tCOST\tRESULT\n")
	for i, s := range jobs {
		if i == limit {
			break
		}
		cost := "-"
		if s.CostUSD > 0 {
			cost = fmt.Sprintf("$%.2f", s.CostUSD)
		}
		result := s.PR
		switch {
		case s.Result != "" && s.PR != "":
			result = s.Result + ": " + s.PR
		case s.Result != "":
			result = s.Result
		}
		switch {
		case s.Stage == job.StageFailed:
			result = firstLine(s.Error)
		case s.Stage == job.StageInterrupted:
			result = "agentd stopped during the job"
		case !s.Terminal():
			result = "running: " + s.Title
		}
		tp.printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Issue, strings.TrimPrefix(s.Model, "claude-"),
			s.Stage, s.Started.Local().Format("Jan 02 15:04"), s.Elapsed(now), cost, cut(result, 70))
	}
	if tp.err != nil {
		return tp.err
	}
	return tw.Flush()
}

// printJob shows the latest job for an issue: state, files, and the end of the logs.
func printJob(w io.Writer, stateDir, issue string) error {
	jobs, err := job.Jobs(stateDir)
	if err != nil {
		return err
	}
	for _, s := range jobs {
		if !strings.EqualFold(s.Issue, issue) {
			continue
		}
		p := &printer{w: w}
		p.printf("%s  %s\n", s.Issue, s.Title)
		p.printf("  stage    %s (%s)\n", s.Stage, s.Elapsed(time.Now()))
		p.printf("  model    %s\n", s.Model)
		p.printf("  started  %s\n", s.Started.Local().Format(time.DateTime))
		if s.URL != "" {
			p.printf("  issue    %s\n", s.URL)
		}
		if s.PR != "" {
			p.printf("  pr       %s\n", s.PR)
		}
		if s.Result != "" {
			p.printf("  result   %s\n", s.Result)
		}
		if s.CostUSD > 0 {
			p.printf("  cost     $%.2f, %d turns\n", s.CostUSD, s.Turns)
		}
		if s.Sandbox != "" && !s.Terminal() {
			p.printf("  sandbox  %s (sbx exec -it %s bash)\n", s.Sandbox, s.Sandbox)
		}
		p.printf("  dir      %s\n", s.Dir)
		if s.Error != "" {
			p.printf("\nerror:\n%s\n", indent(s.Error))
		}
		if b, err := os.ReadFile(filepath.Join(s.Dir, "agent.json")); err == nil {
			var res struct {
				Result string `json:"result"`
			}
			if json.Unmarshal(b, &res) == nil && res.Result != "" {
				p.printf("\nagent's last message:\n%s\n", indent(cut(res.Result, 3000)))
			}
		} else if b, err := os.ReadFile(filepath.Join(s.Dir, "setup.log")); err == nil && s.Stage == job.StageSetup {
			p.printf("\nsetup log (end):\n%s\n", indent(lastLines(string(b), 10)))
		}
		return p.err
	}
	return fmt.Errorf("no job for %s", issue)
}

// printer writes formatted output, stops at the first write error and keeps it for the caller to
// return. (A closed stdout never gets here: Go exits on SIGPIPE, as `agentd -status | head` shows.)
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

// firstLine flattens an error to one line ("patch rejected: - bunfig.toml: protected file").
func firstLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
