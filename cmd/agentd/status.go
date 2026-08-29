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
	svc := "unknown"
	if out, err := exec.Command("systemctl", "--user", "is-active", "sandbox-agent").Output(); err == nil || len(out) > 0 {
		svc = strings.TrimSpace(string(out))
	}
	fmt.Fprintf(w, "service sandbox-agent: %s\n", svc)
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
		fmt.Fprintf(w, "sandboxes: %s\n", strings.Join(names, ", "))
	}
	jobs, err := job.Jobs(stateDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(w)
	if len(jobs) == 0 {
		fmt.Fprintln(w, "no jobs yet")
		return nil
	}
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ISSUE\tMODEL\tSTAGE\tSTARTED\tTIME\tCOST\tRESULT")
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
		case s.Stage == job.StageFailed:
			result = firstLine(s.Error)
		case s.Stage == job.StageInterrupted:
			result = "agentd stopped during the job"
		case !s.Terminal():
			result = "running: " + s.Title
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Issue, strings.TrimPrefix(s.Model, "claude-"),
			s.Stage, s.Started.Local().Format("Jan 02 15:04"), s.Elapsed(now), cost, cut(result, 70))
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
		fmt.Fprintf(w, "%s  %s\n", s.Issue, s.Title)
		fmt.Fprintf(w, "  stage    %s (%s)\n", s.Stage, s.Elapsed(time.Now()))
		fmt.Fprintf(w, "  model    %s\n", s.Model)
		fmt.Fprintf(w, "  started  %s\n", s.Started.Local().Format(time.DateTime))
		if s.URL != "" {
			fmt.Fprintf(w, "  issue    %s\n", s.URL)
		}
		if s.PR != "" {
			fmt.Fprintf(w, "  pr       %s\n", s.PR)
		}
		if s.CostUSD > 0 {
			fmt.Fprintf(w, "  cost     $%.2f, %d turns\n", s.CostUSD, s.Turns)
		}
		if s.Sandbox != "" && !s.Terminal() {
			fmt.Fprintf(w, "  sandbox  %s (sbx exec -it %s bash)\n", s.Sandbox, s.Sandbox)
		}
		fmt.Fprintf(w, "  dir      %s\n", s.Dir)
		if s.Error != "" {
			fmt.Fprintf(w, "\nerror:\n%s\n", indent(s.Error))
		}
		if b, err := os.ReadFile(filepath.Join(s.Dir, "agent.json")); err == nil {
			var res struct {
				Result string `json:"result"`
			}
			if json.Unmarshal(b, &res) == nil && res.Result != "" {
				fmt.Fprintf(w, "\nagent's last message:\n%s\n", indent(cut(res.Result, 3000)))
			}
		} else if b, err := os.ReadFile(filepath.Join(s.Dir, "setup.log")); err == nil && s.Stage == job.StageSetup {
			fmt.Fprintf(w, "\nsetup log (end):\n%s\n", indent(lastLines(string(b), 10)))
		}
		return nil
	}
	return fmt.Errorf("no job for %s", issue)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
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
