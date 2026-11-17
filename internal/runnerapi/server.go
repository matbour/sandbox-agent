package runnerapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/task"
)

// Server is the manager's side: a queue of runs that runners lease. It is also a job.Executor,
// so the job hands it runs as it would to an in-process provider.
type Server struct {
	LeaseTTL  time.Duration // a lease not extended for this long is lost; default 30 s
	LongPoll  time.Duration // how long a lease request waits for a run; default 25 s
	MaxUpload int64         // workspace bytes accepted back; default 4 GiB
	MaxOutput int64         // worker output bytes; default task.DefaultReadLimits.MaxBytes
	TempDir   string        // where workspace tars wait for their runner
	Log       *slog.Logger

	tokens map[string]string // hex SHA-256 of a token → runner name

	mu      sync.Mutex
	queue   []*run
	runs    map[string]*run
	runners map[string]Register
	wake    chan struct{} // closed and replaced when a run is queued
}

// run is one run, from Execute to its end.
type run struct {
	spec  runner.Spec
	input string // workspace tar for the runner
	ws    string // the job's workspace, where the result is mirrored

	lease    string
	runner   string
	deadline time.Time
	attempt  int

	offset    int64 // worker output received so far
	output    *io.PipeWriter
	term      chan task.Event
	cancelled bool
	ended     chan End
	lostErr   error
}

// NewServer reads the runners' tokens from a file of "<name> <hex sha256 of the token>" lines.
func NewServer(tokenFile string, log *slog.Logger) (*Server, error) {
	f, err := os.Open(tokenFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	s := &Server{tokens: map[string]string{}, runs: map[string]*run{}, runners: map[string]Register{},
		wake: make(chan struct{}), Log: log}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, sum, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || strings.HasPrefix(name, "#") {
			continue
		}

		s.tokens[strings.ToLower(strings.TrimSpace(sum))] = name
	}

	if len(s.tokens) == 0 {
		return nil, fmt.Errorf("%s: no runner token", tokenFile)
	}

	return s, sc.Err()
}

// HashToken is what the token file holds for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func (s *Server) defaults() {
	if s.LeaseTTL == 0 {
		s.LeaseTTL = 30 * time.Second
	}

	if s.LongPoll == 0 {
		s.LongPoll = 25 * time.Second
	}

	if s.MaxUpload == 0 {
		s.MaxUpload = 4 << 30
	}

	if s.MaxOutput == 0 {
		s.MaxOutput = int64(task.DefaultReadLimits.MaxBytes)
	}

	if s.Log == nil {
		s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

// Name is how the job records where it ran.
func (s *Server) Name() string { return "runners" }

// Execute queues spec, waits for a runner to run it, and mirrors the changed workspace into ws.
func (s *Server) Execute(ctx context.Context, spec runner.Spec, ws string, events func(task.Event)) (runner.Result, error) {
	s.defaults()

	in, err := os.CreateTemp(s.TempDir, "run-input-*.tar")
	if err != nil {
		return runner.Result{}, err
	}
	defer func() { _ = os.Remove(in.Name()) }()

	err = guestexec.WriteTree(in, ws, guestexec.TreeOptions{Exclude: spec.Exclude})
	if cerr := in.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return runner.Result{}, fmt.Errorf("workspace for the runner: %w", err)
	}

	pr, pw := io.Pipe()
	r := &run{spec: spec, input: in.Name(), ws: ws, output: pw, term: make(chan task.Event, 1), ended: make(chan End, 1)}

	// The manager reads the worker's output itself, as hostile input, whatever the runner did.
	go func() {
		term := task.Read(pr, task.ReadLimits{MaxLine: task.DefaultReadLimits.MaxLine, MaxBytes: int(s.MaxOutput),
			MaxEvents: task.DefaultReadLimits.MaxEvents, MaxText: task.DefaultReadLimits.MaxText,
			MaxBody: task.DefaultReadLimits.MaxBody}, events, nil)

		_, _ = io.Copy(io.Discard, pr)

		r.term <- term
	}()

	s.mu.Lock()
	s.runs[spec.ID] = r
	s.queue = append(s.queue, r)
	s.notify()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.runs, spec.ID)
		s.dequeue(r)
		s.mu.Unlock()

		_ = pw.Close()
	}()

	var end End

	select {
	case end = <-r.ended:
	case <-ctx.Done():
		s.mu.Lock()
		r.cancelled = true
		s.dequeue(r)
		s.mu.Unlock()

		return runner.Result{}, ctx.Err()
	}

	_ = pw.Close()
	term := <-r.term

	if r.lostErr != nil {
		return runner.Result{Terminal: term}, r.lostErr
	}

	if end.Error != "" {
		return runner.Result{Terminal: term, Log: []byte(end.Log)}, fmt.Errorf("runner %s: %s", r.runner, end.Error)
	}

	return runner.Result{Terminal: term, Log: []byte(end.Log)}, nil
}

// notify wakes the lease requests waiting for a run. Called with mu held.
func (s *Server) notify() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// dequeue removes r from the queue if it is there. Called with mu held.
func (s *Server) dequeue(r *run) {
	for i, q := range s.queue {
		if q == r {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)

			return
		}
	}
}

// Handler serves the runners' calls.
func (s *Server) Handler() http.Handler {
	s.defaults()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", s.auth(s.register))
	mux.HandleFunc("POST /v1/lease", s.auth(s.leaseRun))
	mux.HandleFunc("POST /v1/heartbeat", s.auth(s.heartbeat))
	mux.HandleFunc("GET /v1/runs/{id}/input", s.auth(s.leased(s.input)))
	mux.HandleFunc("POST /v1/runs/{id}/output", s.auth(s.leased(s.output)))
	mux.HandleFunc("PUT /v1/runs/{id}/workspace", s.auth(s.leased(s.workspace)))
	mux.HandleFunc("POST /v1/runs/{id}/end", s.auth(s.leased(s.end)))

	return mux
}

// Expire runs until ctx ends, marking leases that were not extended in time as lost.
func (s *Server) Expire(ctx context.Context) {
	s.defaults()

	t := time.NewTicker(time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.expire(now)
		}
	}
}

func (s *Server) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range s.runs {
		if r.lease == "" || now.Before(r.deadline) {
			continue
		}

		// Lost before the worker said anything: nothing ran that costs money, so try once more,
		// on any runner. Otherwise a person decides (runners.md, Failures).
		if r.offset == 0 && r.attempt == 0 && !r.cancelled {
			s.Log.Warn("lease lost before any output, queued again", "run", r.spec.ID, "runner", r.runner)

			r.attempt++
			r.lease, r.runner = "", ""
			s.queue = append(s.queue, r)
			s.notify()

			continue
		}

		s.Log.Warn("lease lost", "run", r.spec.ID, "runner", r.runner)

		r.lostErr = fmt.Errorf("run lost: runner %s stopped answering", r.runner)
		r.lease = ""

		select {
		case r.ended <- End{}:
		default:
		}
	}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// ErrNoLease is returned by the client when the manager no longer knows the lease.
var ErrNoLease = errors.New("the manager does not know this lease any more")

// tokenFile is the default file name, under the secrets directory.
const tokenFile = "runner-tokens"

// DefaultTokenFile returns the token file under dir.
func DefaultTokenFile(dir string) string { return filepath.Join(dir, tokenFile) }
