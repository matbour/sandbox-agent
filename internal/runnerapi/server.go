package runnerapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

type ctxKey struct{}

// auth checks the bearer token and puts the runner's name in the request's context.
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		sum := HashToken(tok)

		name := ""

		for known, n := range s.tokens {
			if subtle.ConstantTimeCompare([]byte(known), []byte(sum)) == 1 {
				name = n
			}
		}

		if !ok || name == "" {
			http.Error(w, "unknown runner", http.StatusUnauthorized)

			return
		}

		h(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, name)))
	}
}

func runnerName(req *http.Request) string {
	name, _ := req.Context().Value(ctxKey{}).(string)
	return name
}

// leased finds the run and checks that the caller holds its current lease.
func (s *Server) leased(h func(http.ResponseWriter, *http.Request, *run)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		r, ok := s.runs[req.PathValue("id")]
		held := ok && r.lease != "" && r.lease == req.Header.Get(LeaseHeader) && r.runner == runnerName(req)
		s.mu.Unlock()

		if !held {
			http.Error(w, "no such lease", http.StatusConflict)

			return
		}

		h(w, req, r)
	}
}

func readJSON(w http.ResponseWriter, req *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)

		return false
	}

	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) register(w http.ResponseWriter, req *http.Request) {
	var r Register
	if !readJSON(w, req, &r) {
		return
	}

	if r.Protocol != Protocol {
		http.Error(w, fmt.Sprintf("protocol %d, this manager speaks %d", r.Protocol, Protocol), http.StatusBadRequest)

		return
	}

	// The name is the token's, not what the runner says.
	r.Name = runnerName(req)

	s.mu.Lock()
	s.runners[r.Name] = r
	s.mu.Unlock()

	s.Log.Info("runner registered", "runner", r.Name, "arch", r.Arch, "cpus", r.Capacity.CPUs, "memory_mib", r.Capacity.MemoryMiB)
	writeJSON(w, map[string]int{"protocol": Protocol})
}

// leaseRun gives the caller the first queued run that fits its free capacity, waiting up to
// LongPoll for one.
func (s *Server) leaseRun(w http.ResponseWriter, req *http.Request) {
	var lr LeaseRequest
	if !readJSON(w, req, &lr) {
		return
	}

	name := runnerName(req)
	deadline := time.After(s.LongPoll)

	for {
		s.mu.Lock()

		for _, r := range s.queue {
			if !lr.Free.Fits(r.spec) {
				continue
			}

			s.dequeue(r)
			r.lease, r.runner, r.deadline = newID(), name, time.Now().Add(s.LeaseTTL)
			l := Lease{ID: r.lease, Spec: r.spec, TTLSeconds: int(s.LeaseTTL.Seconds()), MaxUpload: s.MaxUpload, MaxOutput: s.MaxOutput}
			s.mu.Unlock()

			s.Log.Info("run leased", "run", r.spec.ID, "runner", name, "attempt", r.attempt+1)
			writeJSON(w, l)

			return
		}

		wake := s.wake
		s.mu.Unlock()

		select {
		case <-wake:
		case <-deadline:
			w.WriteHeader(http.StatusNoContent)

			return
		case <-req.Context().Done():
			return
		}
	}
}

func (s *Server) heartbeat(w http.ResponseWriter, req *http.Request) {
	var hb Heartbeat
	if !readJSON(w, req, &hb) {
		return
	}

	name := runnerName(req)

	var reply HeartbeatReply

	s.mu.Lock()

	for _, id := range hb.Leases {
		r := s.runByLease(id)
		if r == nil || r.runner != name || r.cancelled {
			reply.Cancel = append(reply.Cancel, id)

			continue
		}

		r.deadline = time.Now().Add(s.LeaseTTL)
	}

	s.mu.Unlock()

	writeJSON(w, reply)
}

// runByLease finds a run by its lease ID. Called with mu held.
func (s *Server) runByLease(id string) *run {
	for _, r := range s.runs {
		if r.lease == id {
			return r
		}
	}

	return nil
}

func (s *Server) input(w http.ResponseWriter, req *http.Request, r *run) {
	w.Header().Set("Content-Type", "application/x-tar")
	http.ServeFile(w, req, r.input)
}

// output appends the worker's raw output. A retry resends bytes the manager already has: they are
// skipped by offset. A gap is refused.
func (s *Server) output(w http.ResponseWriter, req *http.Request, r *run) {
	from, err := strconv.ParseInt(req.URL.Query().Get("offset"), 10, 64)
	if err != nil || from < 0 {
		http.Error(w, "bad offset", http.StatusBadRequest)

		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, guestexec.MaxFrame+1))
	if err != nil || len(body) > guestexec.MaxFrame {
		http.Error(w, "bad body", http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	have := r.offset

	switch {
	case from > have:
		s.mu.Unlock()
		http.Error(w, fmt.Sprintf("gap: have %d bytes", have), http.StatusConflict)

		return
	case from+int64(len(body)) <= have:
		s.mu.Unlock()
		writeJSON(w, map[string]int64{"offset": have})

		return
	}

	body = body[have-from:]
	r.offset += int64(len(body))
	s.mu.Unlock()

	// task.Read is on the other end and never stops reading before the pipe closes.
	_, _ = r.output.Write(body)

	writeJSON(w, map[string]int64{"offset": have + int64(len(body))})
}

func (s *Server) workspace(w http.ResponseWriter, req *http.Request, r *run) {
	err := guestexec.Mirror(r.ws, http.MaxBytesReader(w, req.Body, s.MaxUpload+(64<<20)),
		guestexec.MirrorOptions{Exclude: r.spec.Exclude, MaxBytes: s.MaxUpload})
	if err != nil {
		s.Log.Warn("workspace from runner refused", "run", r.spec.ID, "err", err)
		http.Error(w, "workspace refused", http.StatusBadRequest)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) end(w http.ResponseWriter, req *http.Request, r *run) {
	var e End
	if !readJSON(w, req, &e) {
		return
	}

	s.mu.Lock()
	r.lease = ""
	s.mu.Unlock()

	select {
	case r.ended <- e:
	default:
	}

	w.WriteHeader(http.StatusNoContent)
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
