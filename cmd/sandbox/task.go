package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/sandbox/providers"
	"github.com/matbour/sandbox-agent/internal/task"
)

// exclude stays in the sandbox: build output and caches, never part of the patch. Same list as
// agentd's (internal/job).
var exclude = []string{"node_modules/", ".bun/", ".cache/", ".turbo/", ".next/", "dist/", "coverage/",
	"__pycache__/", ".pytest_cache/", "*.pyc", ".DS_Store"}

// Result is result.json: how the task ended, for a person or another program.
type Result struct {
	Status   string  `json:"status"` // done, ask or fail
	Title    string  `json:"title,omitempty"`
	Body     string  `json:"body,omitempty"`
	Question string  `json:"question,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	Turns    int     `json:"turns,omitempty"`
	Provider string  `json:"provider"`
	Seconds  float64 `json:"seconds"`
	Patch    string  `json:"patch,omitempty"` // changes.patch, empty when nothing changed
	Files    int     `json:"files_changed"`
}

// Exit codes: 0 done, 3 the agent asks a question, 1 failed, 2 bad usage.
const (
	exitDone = 0
	exitFail = 1
	exitAsk  = 3
)

func runTask(args []string) int {
	fs := flag.NewFlagSet("sandbox task", flag.ExitOnError)
	repo := fs.String("repo", "", "Git repository to work on (its HEAD; the repository itself is not changed)")
	taskFile := fs.String("task", "", "the task, in Markdown")
	setupFile := fs.String("setup", "", "bash script run in the sandbox before the agent (dependencies)")
	out := fs.String("out", "", "output directory (default ./sandbox-<time>)")
	provider := fs.String("provider", "firecracker", "sandbox: "+strings.Join(providers.Registry.Names(), ", "))
	agent := fs.String("agent", "claude", "agent backend: claude, or fake (scripted by FAKE: lines in the task)")
	model := fs.String("model", "", "model for the agent (default: the agent's)")
	cpus := fs.Int("cpus", 2, "vCPUs")
	memory := fs.String("memory", "4g", "memory")
	cpuset := fs.String("cpuset", "", "host cores for the sandbox, e.g. 4-7")
	maxTurns := fs.Int("max-turns", 150, "agent turns")
	timeout := fs.Duration("timeout", 45*time.Minute, "agent wall clock limit")
	tokenFile := fs.String("token-file", "", "file holding the Claude subscription token (default $CLAUDE_CODE_OAUTH_TOKEN)")
	_ = fs.Parse(args)

	if *repo == "" || *taskFile == "" {
		fs.Usage()

		return 2
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, dir, err := doTask(ctx, taskOptions{
		repo: *repo, task: *taskFile, setup: *setupFile, out: *out, provider: *provider, agent: *agent,
		model: *model, cpus: *cpus, memory: *memory, cpuset: *cpuset, maxTurns: *maxTurns, timeout: *timeout,
		tokenFile: *tokenFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox task: %v\n", err)

		if dir != "" {
			fmt.Fprintf(os.Stderr, "output: %s\n", dir)
		}

		return exitFail
	}

	report(os.Stdout, res, dir)

	switch res.Status {
	case task.KindDone:
		return exitDone
	case task.KindAsk:
		return exitAsk
	default:
		return exitFail
	}
}

type taskOptions struct {
	repo, task, setup, out, provider, agent, model, memory, cpuset, tokenFile string
	cpus, maxTurns                                                            int
	timeout                                                                   time.Duration
}

// doTask runs one task and writes the output directory:
//
//	repo/           clean clone of the repository's HEAD; never enters the sandbox
//	workspace/      the files the agent worked on, as it left them (hostile)
//	changes.patch   the workspace against HEAD, binary-safe, for git apply
//	events.jsonl    the worker's events
//	worker.log      the worker's stderr: setup output, agent log
//	result.json     Result
func doTask(ctx context.Context, o taskOptions) (Result, string, error) {
	res := Result{Provider: o.provider}

	text, err := os.ReadFile(o.task)
	if err != nil {
		return res, "", err
	}

	setup := ""
	if o.setup != "" {
		b, err := os.ReadFile(o.setup)
		if err != nil {
			return res, "", err
		}

		setup = string(b)
	}

	token, err := subscriptionToken(o.tokenFile, o.agent)
	if err != nil {
		return res, "", err
	}

	p, err := providers.Registry.New(o.provider, sandbox.Options{CPUSet: o.cpuset})
	if err != nil {
		return res, "", err
	}

	dir := o.out
	if dir == "" {
		dir = "sandbox-" + time.Now().Format("20060102-150405")
	}

	if dir, err = filepath.Abs(dir); err != nil {
		return res, "", err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, "", err
	}

	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		return res, "", fmt.Errorf("output directory %s is not empty", dir)
	}

	clean, ws := filepath.Join(dir, "repo"), filepath.Join(dir, "workspace")

	if err := cloneAndExport(ctx, o.repo, clean, ws); err != nil {
		return res, dir, err
	}

	var env []string
	if token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}

	spec := runner.Spec{
		ID:        "task-" + time.Now().Format("150405") + fmt.Sprintf("-%d", os.Getpid()%10000),
		CPUs:      o.cpus,
		Memory:    o.memory,
		Exclude:   exclude,
		SecretEnv: env,
		Task: task.Task{Prompt: prompt(string(text)), Model: o.model, Agent: o.agent, Setup: setup,
			Limits: task.Limits{MaxTurns: o.maxTurns, AgentTimeoutSec: int(o.timeout.Seconds())}},
	}

	events, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return res, dir, err
	}
	defer func() { _ = events.Close() }()

	enc := json.NewEncoder(events)
	leaked := false

	began := time.Now()

	run, err := runner.Execute(ctx, p, spec, ws, runner.Hooks{Events: func(e task.Event) {
		leaked = leaked || leaks(token, e)
		_ = enc.Encode(e)
	}})
	res.Seconds = time.Since(began).Seconds()

	if leaked || leaks(token, run.Terminal) || (token != "" && bytes.Contains(run.Log, []byte(token))) {
		return res, dir, errors.New("the subscription token came back in the agent's output: nothing kept")
	}

	_ = enc.Encode(run.Terminal)
	_ = os.WriteFile(filepath.Join(dir, "worker.log"), run.Log, 0o600)

	if err != nil {
		return res, dir, err
	}

	t := run.Terminal
	res.Status, res.Title, res.Body, res.Question, res.Reason = t.Kind, t.Title, t.Body, t.Question, t.Reason
	res.CostUSD, res.Turns = t.CostUSD, t.Turns

	patch, files, err := diff(ctx, clean, ws)
	if err != nil {
		return res, dir, err
	}

	if len(patch) > 0 {
		res.Patch, res.Files = filepath.Join(dir, "changes.patch"), files
		if err := os.WriteFile(res.Patch, patch, 0o600); err != nil {
			return res, dir, err
		}
	}

	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(b, '\n'), 0o600); err != nil {
		return res, dir, err
	}

	return res, dir, nil
}

// subscriptionToken reads the Claude subscription token (D43) for the claude agent: from a file,
// else from the environment. The fake agent needs none.
func subscriptionToken(file, agent string) (string, error) {
	if agent != "claude" {
		return "", nil
	}

	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(string(b)), nil
	}

	if t := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); t != "" {
		return t, nil
	}

	return "", errors.New("the claude agent needs a subscription token: -token-file or $CLAUDE_CODE_OAUTH_TOKEN")
}

// cloneAndExport makes the clean clone of repo's HEAD, and exports its tracked files, without Git
// metadata, to the workspace the sandbox gets.
func cloneAndExport(ctx context.Context, repo, clean, ws string) error {
	if _, err := git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--", repo, clean); err != nil {
		return err
	}

	if err := os.Mkdir(ws, 0o700); err != nil {
		return err
	}

	tarball := filepath.Join(filepath.Dir(clean), "head.tar")
	defer func() { _ = os.Remove(tarball) }()

	if _, err := git(ctx, clean, "archive", "--format=tar", "-o", tarball, "HEAD"); err != nil {
		return err
	}

	if b, err := exec.CommandContext(ctx, "tar", "-xf", tarball, "-C", ws, "--no-same-owner").CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, b)
	}

	// The excluded paths never count as changes.
	return os.WriteFile(filepath.Join(clean, ".git", "info", "exclude"), []byte(strings.Join(exclude, "\n")+"\n"), 0o600)
}

// diff takes the patch of the workspace against HEAD. Git runs with the clean clone's metadata,
// which the agent never saw, and the workspace only as content, so nothing the agent wrote is run
// or read as configuration: hooks and fsmonitor are off as well. A separate index leaves the clone
// untouched.
func diff(ctx context.Context, clean, ws string) ([]byte, int, error) {
	index := filepath.Join(filepath.Dir(clean), "index.tmp")
	defer func() { _ = os.Remove(index) }()

	base := []string{"--git-dir", filepath.Join(clean, ".git"), "--work-tree", ws,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.symlinks=true"}

	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // built here
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_CONFIG_NOSYSTEM=1")

		var so, se bytes.Buffer

		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(se.String()))
		}

		return so.Bytes(), nil
	}

	if _, err := run("read-tree", "HEAD"); err != nil {
		return nil, 0, err
	}

	if _, err := run("add", "--all", "."); err != nil {
		return nil, 0, err
	}

	patch, err := run("diff", "--cached", "--binary", "HEAD")
	if err != nil {
		return nil, 0, err
	}

	names, err := run("diff", "--cached", "--name-only", "HEAD")
	if err != nil {
		return nil, 0, err
	}

	return patch, len(strings.Fields(string(names))), nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return out, nil
}

// leaks reports whether an event carries the token.
func leaks(token string, e task.Event) bool {
	if token == "" {
		return false
	}

	b, _ := json.Marshal(e)

	return bytes.Contains(b, []byte(token))
}

// report prints the outcome for a person.
func report(w io.Writer, r Result, dir string) {
	var b strings.Builder

	fmt.Fprintf(&b, "status: %s  (%s, %.1f s, %d turns, $%.2f)\n", r.Status, r.Provider, r.Seconds, r.Turns, r.CostUSD)

	switch {
	case r.Title != "":
		fmt.Fprintf(&b, "title: %s\n", r.Title)
	case r.Reason != "":
		fmt.Fprintf(&b, "reason: %s\n", r.Reason)
	}

	if r.Question != "" {
		fmt.Fprintf(&b, "question: %s\n", r.Question)
	}

	if r.Patch != "" {
		fmt.Fprintf(&b, "patch: %s (%d files)\n", r.Patch, r.Files)
	} else {
		fmt.Fprintln(&b, "patch: none, nothing changed")
	}

	fmt.Fprintf(&b, "output: %s\n", dir)

	_, _ = io.WriteString(w, b.String())
}
