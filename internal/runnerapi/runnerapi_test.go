package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/sandbox"
	"github.com/matbour/sandbox-agent/internal/task"
)

// hostProvider plays the worker on the host directory, for protocol tests only: it reads the
// task, writes out.txt with the runner's name, and emits events. No isolation at all.
type hostProvider struct {
	name    string
	running atomic.Int32
	peak    atomic.Int32
}

func (p *hostProvider) Name() string                  { return "host-test" }
func (p *hostProvider) Prepare(context.Context) error { return nil }
func (p *hostProvider) Start(_ context.Context, s sandbox.Spec) (sandbox.Sandbox, error) {
	return &hostSandbox{p: p, ws: s.Workspace, name: s.Name}, nil
}

type hostSandbox struct {
	p        *hostProvider
	ws, name string
}

func (s *hostSandbox) Name() string       { return s.name }
func (s *hostSandbox) WorkDir() string    { return s.ws }
func (s *hostSandbox) WorkerPath() string { return "/worker" }
func (s *hostSandbox) Close() error       { return nil }

func (s *hostSandbox) Exec(ctx context.Context, o sandbox.ExecOptions, _ ...string) ([]byte, []byte, error) {
	n := s.p.running.Add(1)
	defer s.p.running.Add(-1)

	for {
		peak := s.p.peak.Load()
		if n <= peak || s.p.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	var t task.Task
	if err := json.NewDecoder(o.Stdin).Decode(&t); err != nil {
		return nil, nil, err
	}

	enc := json.NewEncoder(o.Stdout)
	_ = enc.Encode(task.Event{Kind: task.KindThought, Text: "on " + s.p.name})

	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	if err := os.WriteFile(filepath.Join(s.ws, "out.txt"), []byte(s.p.name), 0o644); err != nil {
		return nil, nil, err
	}

	_ = os.Remove(filepath.Join(s.ws, "delete-me.txt"))
	_ = enc.Encode(task.Event{Kind: task.KindDone, Title: t.Prompt, Turns: 1})

	return nil, []byte("worker log"), nil
}

type fixture struct {
	srv    *Server
	url    string
	tokens map[string]string
	ctx    context.Context
}

func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	f := &fixture{tokens: map[string]string{}, ctx: ctx}

	var lines []string
	for _, n := range names {
		f.tokens[n] = "token-of-" + n
		lines = append(lines, n+" "+HashToken(f.tokens[n]))
	}

	tf := filepath.Join(t.TempDir(), "runner-tokens")
	if err := os.WriteFile(tf, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(tf, nil)
	if err != nil {
		t.Fatal(err)
	}

	srv.LeaseTTL, srv.LongPoll, srv.TempDir = 2*time.Second, time.Second, t.TempDir()
	go srv.Expire(ctx)

	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	f.srv, f.url = srv, hs.URL

	return f
}

func (f *fixture) agent(t *testing.T, name string, c Capacity) *hostProvider {
	t.Helper()

	p := &hostProvider{name: name}
	a := &Agent{Manager: f.url, Token: f.tokens[name], Name: name, Provider: p, Capacity: c, WorkDir: t.TempDir()}

	go func() { _ = a.Run(f.ctx) }()

	return p
}

func workspace(t *testing.T) string {
	t.Helper()

	ws := t.TempDir()
	for name, body := range map[string]string{"in.txt": "input", "delete-me.txt": "x"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return ws
}

// Two runners with different capacities share six runs; each stays within its capacity, every
// run comes back with its workspace changed, and the manager reads the events itself.
func TestRunsAcrossRunners(t *testing.T) {
	f := newFixture(t, "big", "small")
	big := f.agent(t, "big", Capacity{CPUs: 2, MemoryMiB: 2048})
	small := f.agent(t, "small", Capacity{CPUs: 1, MemoryMiB: 1024})

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ranOn   = map[string]int{}
		thought atomic.Int32
	)

	for i := range 6 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			ws := workspace(t)
			spec := runner.Spec{ID: fmt.Sprintf("run-%d", i), CPUs: 1, Memory: "1g", Task: task.Task{Prompt: fmt.Sprintf("title %d", i)}}

			res, err := f.srv.Execute(f.ctx, spec, ws, func(task.Event) { thought.Add(1) })
			if err != nil || res.Terminal.Kind != task.KindDone || res.Terminal.Title != spec.Task.Prompt {
				t.Errorf("%s: %+v, %v", spec.ID, res.Terminal, err)

				return
			}

			b, _ := os.ReadFile(filepath.Join(ws, "out.txt"))
			if _, err := os.Stat(filepath.Join(ws, "delete-me.txt")); err == nil {
				t.Errorf("%s: a deletion did not come back", spec.ID)
			}

			mu.Lock()
			ranOn[string(b)]++
			mu.Unlock()
		}()
	}

	wg.Wait()

	if ranOn["big"] == 0 || ranOn["small"] == 0 || ranOn["big"]+ranOn["small"] != 6 {
		t.Errorf("runs per runner: %v", ranOn)
	}

	if big.peak.Load() > 2 || small.peak.Load() > 1 {
		t.Errorf("capacity exceeded: big ran %d at once, small %d", big.peak.Load(), small.peak.Load())
	}

	if thought.Load() != 6 {
		t.Errorf("%d thoughts reached the manager, want 6", thought.Load())
	}
}

func post(t *testing.T, url, token, lease string, body any) *http.Response {
	t.Helper()

	var r io.Reader
	switch b := body.(type) {
	case []byte:
		r = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		r = bytes.NewReader(j)
	}

	req, _ := http.NewRequest(http.MethodPost, url, r)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(LeaseHeader, lease)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	return resp
}

// A runner that leases and disappears before any output: the run is queued again and another
// runner does it. Unknown tokens and foreign leases are refused (E-RUNNER-1, 2).
func TestLostLeaseAndAuth(t *testing.T) {
	f := newFixture(t, "ghost", "good")

	if resp := post(t, f.url+"/v1/register", "wrong", "", Register{Protocol: Protocol}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", resp.StatusCode)
	}

	if resp := post(t, f.url+"/v1/register", f.tokens["ghost"], "", Register{Protocol: Protocol + 1}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("other protocol: %d (E-RUNNER-7)", resp.StatusCode)
	}

	done := make(chan runner.Result, 1)
	ws := workspace(t)

	go func() {
		res, _ := f.srv.Execute(f.ctx, runner.Spec{ID: "run-lost", CPUs: 1, Memory: "1g", Task: task.Task{Prompt: "p"}}, ws, nil)
		done <- res
	}()

	// The ghost leases by hand, then never sends a heartbeat.
	var l Lease

	req, _ := http.NewRequest(http.MethodPost, f.url+"/v1/lease", strings.NewReader(`{"free":{"cpus":4,"memory_mib":4096}}`))
	req.Header.Set("Authorization", "Bearer "+f.tokens["ghost"])

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("ghost lease: %v %v", resp, err)
	}

	_ = json.NewDecoder(resp.Body).Decode(&l)
	_ = resp.Body.Close()

	if r := post(t, f.url+"/v1/runs/run-lost/output?offset=0", f.tokens["good"], l.ID, []byte("x")); r.StatusCode != http.StatusConflict {
		t.Errorf("another runner wrote to the ghost's lease: %d (E-RUNNER-2)", r.StatusCode)
	}

	f.agent(t, "good", Capacity{CPUs: 1, MemoryMiB: 1024})

	select {
	case res := <-done:
		if res.Terminal.Kind != task.KindDone {
			t.Errorf("after the lost lease: %+v", res.Terminal)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run was not queued again after its lease was lost")
	}
}

// Output resent with an offset the manager already has is counted once (E-RUNNER-6), and a gap
// is refused.
func TestOutputOffsets(t *testing.T) {
	f := newFixture(t, "r")

	got := make(chan runner.Result, 1)

	go func() {
		res, _ := f.srv.Execute(f.ctx, runner.Spec{ID: "run-off", CPUs: 1, Memory: "1g"}, workspace(t), nil)
		got <- res
	}()

	var l Lease

	for l.ID == "" {
		req, _ := http.NewRequest(http.MethodPost, f.url+"/v1/lease", strings.NewReader(`{"free":{"cpus":1,"memory_mib":1024}}`))
		req.Header.Set("Authorization", "Bearer "+f.tokens["r"])

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = json.NewDecoder(resp.Body).Decode(&l)
		_ = resp.Body.Close()
	}

	line := []byte(`{"kind":"done","title":"once"}` + "\n")
	base := f.url + "/v1/runs/run-off/output?offset="

	for _, c := range []struct {
		off  int
		body []byte
		want int
	}{{0, line[:10], 200}, {0, line[:10], 200}, {50, line, 409}, {10, line[10:], 200}} {
		if r := post(t, base+fmt.Sprint(c.off), f.tokens["r"], l.ID, c.body); r.StatusCode != c.want {
			t.Errorf("offset %d: %d, want %d", c.off, r.StatusCode, c.want)
		}
	}

	_ = post(t, f.url+"/v1/runs/run-off/end", f.tokens["r"], l.ID, End{})

	if res := <-got; res.Terminal.Kind != task.KindDone || res.Terminal.Title != "once" {
		t.Errorf("terminal %+v", res.Terminal)
	}
}

func TestCheckURL(t *testing.T) {
	for u, ok := range map[string]bool{"https://m.example": true, "http://127.0.0.1:8788": true, "http://localhost:1": true,
		"http://192.168.1.10:8788": false, "ftp://x": false} {
		if err := CheckURL(u); (err == nil) != ok {
			t.Errorf("%s: %v", u, err)
		}
	}
}

// Cancelling a run on the manager (a Stop from Linear, a timeout) reaches the runner through its
// next heartbeat, and the runner stops the sandbox (R7).
func TestCancelReachesTheRunner(t *testing.T) {
	f := newFixture(t, "r")
	p := &slowProvider{started: make(chan struct{}), stopped: make(chan struct{})}

	a := &Agent{Manager: f.url, Token: f.tokens["r"], Name: "r", Provider: p, Capacity: Capacity{CPUs: 1, MemoryMiB: 1024}, WorkDir: t.TempDir()}
	go func() { _ = a.Run(f.ctx) }()

	ctx, cancel := context.WithCancel(f.ctx)
	errc := make(chan error, 1)

	go func() {
		_, err := f.srv.Execute(ctx, runner.Spec{ID: "run-stop", CPUs: 1, Memory: "1g"}, workspace(t), nil)
		errc <- err
	}()

	<-p.started
	cancel()

	if err := <-errc; err == nil {
		t.Error("Execute returned no error after cancel")
	}

	select {
	case <-p.stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("the runner did not stop the sandbox after the cancel")
	}
}

// slowProvider's worker runs until it is cancelled.
type slowProvider struct {
	started chan struct{}
	stopped chan struct{}
}

func (p *slowProvider) Name() string                  { return "slow-test" }
func (p *slowProvider) Prepare(context.Context) error { return nil }
func (p *slowProvider) Start(_ context.Context, s sandbox.Spec) (sandbox.Sandbox, error) {
	return &slowSandbox{p: p, name: s.Name}, nil
}

type slowSandbox struct {
	p    *slowProvider
	name string
}

func (s *slowSandbox) Name() string       { return s.name }
func (s *slowSandbox) WorkDir() string    { return "/" }
func (s *slowSandbox) WorkerPath() string { return "/worker" }
func (s *slowSandbox) Close() error       { return nil }

func (s *slowSandbox) Exec(ctx context.Context, o sandbox.ExecOptions, _ ...string) ([]byte, []byte, error) {
	_, _ = io.Copy(io.Discard, o.Stdin)
	_, _ = fmt.Fprintln(o.Stdout, `{"kind":"thought","text":"working for ever"}`)

	close(s.p.started)
	<-ctx.Done()
	close(s.p.stopped)

	return nil, nil, ctx.Err()
}

// A runner that disappears after the worker produced output: the run is not tried again (the
// agent may already have spent money), the job gets an error (runners.md, Failures).
func TestLostAfterOutput(t *testing.T) {
	f := newFixture(t, "ghost")

	errc := make(chan error, 1)

	go func() {
		_, err := f.srv.Execute(f.ctx, runner.Spec{ID: "run-gone", CPUs: 1, Memory: "1g"}, workspace(t), nil)
		errc <- err
	}()

	var l Lease

	for l.ID == "" {
		req, _ := http.NewRequest(http.MethodPost, f.url+"/v1/lease", strings.NewReader(`{"free":{"cpus":1,"memory_mib":1024}}`))
		req.Header.Set("Authorization", "Bearer "+f.tokens["ghost"])

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = json.NewDecoder(resp.Body).Decode(&l)
		_ = resp.Body.Close()
	}

	_ = post(t, f.url+"/v1/runs/run-gone/output?offset=0", f.tokens["ghost"], l.ID, []byte(`{"kind":"thought","text":"started"}`+"\n"))

	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "lost") {
			t.Errorf("err %v, want a lost run", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the lost run never ended")
	}

	// E-RUNNER-3: the lease is gone, late output is refused.
	if r := post(t, f.url+"/v1/runs/run-gone/output?offset=36", f.tokens["ghost"], l.ID, []byte("late")); r.StatusCode != http.StatusConflict {
		t.Errorf("late output after the lease was lost: %d", r.StatusCode)
	}
}
