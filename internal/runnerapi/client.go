package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
	"github.com/matbour/sandbox-agent/internal/runner"
	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Agent is the runner's side: it registers, leases runs that fit its free capacity, runs them on
// its provider and reports back. One Agent per machine (runnerd).
type Agent struct {
	Manager  string // base URL; plain HTTP only to a loopback address
	Token    string
	Name     string
	Version  string
	Provider sandbox.Provider
	Capacity Capacity
	WorkDir  string // workspaces of the runs in progress
	Log      *slog.Logger
	HTTP     *http.Client

	mu     sync.Mutex
	used   Capacity
	active map[string]context.CancelFunc // lease ID → cancel
	beat   time.Duration                 // heartbeat interval: a third of the shortest lease TTL
}

// maxBeat is the longest interval between heartbeats.
const maxBeat = 5 * time.Second

// CheckURL refuses plain HTTP to anything but loopback: the token and the code travel on it.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
			return nil
		}

		return fmt.Errorf("%s: plain HTTP is only allowed to a loopback address; use https", raw)
	default:
		return fmt.Errorf("%s: scheme must be https", raw)
	}
}

// Run serves leases until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := CheckURL(a.Manager); err != nil {
		return err
	}

	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 60 * time.Second}
	}

	if a.Log == nil {
		a.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	a.active = map[string]context.CancelFunc{}
	a.beat = maxBeat

	if err := os.MkdirAll(a.WorkDir, 0o700); err != nil {
		return err
	}

	if err := a.register(ctx); err != nil {
		return err
	}

	go a.heartbeats(ctx)

	var wg sync.WaitGroup
	defer wg.Wait()

	for ctx.Err() == nil {
		free := a.free()
		if free.CPUs <= 0 || free.MemoryMiB <= 0 {
			sleep(ctx, time.Second)

			continue
		}

		l, err := a.lease(ctx, free)
		if err != nil {
			if ctx.Err() == nil {
				a.Log.Warn("lease", "err", err)
				sleep(ctx, 5*time.Second)
			}

			continue
		}

		if l == nil {
			continue
		}

		a.take(l)
		wg.Add(1)

		go func() {
			defer wg.Done()

			a.handle(ctx, l)
		}()
	}

	return ctx.Err()
}

func (a *Agent) register(ctx context.Context) error {
	reg := Register{Name: a.Name, Version: a.Version, Protocol: Protocol, Arch: runtime.GOARCH,
		Providers: []string{a.Provider.Name()}, Capacity: a.Capacity}

	for attempt := 0; ; attempt++ {
		err := a.call(ctx, http.MethodPost, "/v1/register", "", reg, nil)
		if err == nil {
			a.Log.Info("registered", "manager", a.Manager, "cpus", a.Capacity.CPUs, "memory_mib", a.Capacity.MemoryMiB)

			return nil
		}

		var he *httpError
		if errors.As(err, &he) && he.code < 500 {
			return err // wrong token or protocol: retrying will not help
		}

		a.Log.Warn("register", "err", err)

		if !sleep(ctx, backoff(attempt)) {
			return ctx.Err()
		}
	}
}

func (a *Agent) free() Capacity {
	a.mu.Lock()
	defer a.mu.Unlock()

	return Capacity{CPUs: a.Capacity.CPUs - a.used.CPUs, MemoryMiB: a.Capacity.MemoryMiB - a.used.MemoryMiB}
}

func (a *Agent) take(l *Lease) {
	mem, _ := MemoryMiB(l.Spec.Memory)

	a.mu.Lock()
	if b := time.Duration(l.TTLSeconds) * time.Second / 3; b > 0 && b < a.beat {
		a.beat = max(b, 200*time.Millisecond)
	}

	a.used.CPUs += max(l.Spec.CPUs, 1)
	a.used.MemoryMiB += mem
	a.mu.Unlock()
}

func (a *Agent) release(l *Lease) {
	mem, _ := MemoryMiB(l.Spec.Memory)

	a.mu.Lock()
	a.used.CPUs -= max(l.Spec.CPUs, 1)
	a.used.MemoryMiB -= mem
	delete(a.active, l.ID)
	a.mu.Unlock()
}

func (a *Agent) lease(ctx context.Context, free Capacity) (*Lease, error) {
	var l Lease

	err := a.call(ctx, http.MethodPost, "/v1/lease", "", LeaseRequest{Free: free}, &l)
	if errors.Is(err, errNoContent) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &l, nil
}

// heartbeats extends the leases held, at a third of their TTL, and stops the runs the manager
// cancelled.
func (a *Agent) heartbeats(ctx context.Context) {
	last := time.Now()

	// Checked often, sent when due: a lease with a short TTL shortens the interval at once.
	for sleep(ctx, 200*time.Millisecond) {
		a.mu.Lock()
		due := time.Since(last) >= a.beat
		a.mu.Unlock()

		if !due {
			continue
		}

		last = time.Now()

		a.mu.Lock()

		hb := Heartbeat{Leases: []string{}}
		for id := range a.active {
			hb.Leases = append(hb.Leases, id)
		}

		a.mu.Unlock()

		if len(hb.Leases) == 0 {
			continue
		}

		var reply HeartbeatReply
		if err := a.call(ctx, http.MethodPost, "/v1/heartbeat", "", hb, &reply); err != nil {
			a.Log.Warn("heartbeat", "err", err)

			continue
		}

		a.mu.Lock()

		for _, id := range reply.Cancel {
			if cancel, ok := a.active[id]; ok {
				a.Log.Info("run cancelled by the manager", "lease", id)
				cancel()
			}
		}

		a.mu.Unlock()
	}
}

// handle runs one lease from download to report.
func (a *Agent) handle(ctx context.Context, l *Lease) {
	defer a.release(l)

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.mu.Lock()
	a.active[l.ID] = cancel
	a.mu.Unlock()

	log := a.Log.With("run", l.Spec.ID, "lease", l.ID)
	log.Info("run started")

	ws := filepath.Join(a.WorkDir, l.Spec.ID)
	defer func() { _ = os.RemoveAll(ws) }()

	end := End{}

	res, err := a.execute(rctx, l, ws, log)
	if err != nil {
		end.Error = err.Error()
	}

	end.Log = string(tail(res.Log, 1<<20))

	if err := a.call(ctx, http.MethodPost, "/v1/runs/"+l.Spec.ID+"/end", l.ID, end, nil); err != nil {
		log.Warn("end", "err", err)
	}

	log.Info("run ended", "terminal", res.Terminal.Kind, "err", end.Error)
}

func (a *Agent) execute(ctx context.Context, l *Lease, ws string, log *slog.Logger) (runner.Result, error) {
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return runner.Result{}, err
	}

	if err := a.download(ctx, l, ws); err != nil {
		return runner.Result{}, fmt.Errorf("workspace from the manager: %w", err)
	}

	up := &uploader{a: a, l: l, log: log, max: l.MaxOutput}
	up.start(ctx)

	res, err := runner.Execute(ctx, a.Provider, l.Spec, ws, runner.Hooks{Raw: up})

	if uerr := up.close(); err == nil && uerr != nil {
		err = fmt.Errorf("worker output to the manager: %w", uerr)
	}

	if err != nil {
		return res, err
	}

	pr, pw := io.Pipe()

	go func() {
		_ = pw.CloseWithError(guestexec.WriteTree(pw, ws, guestexec.TreeOptions{Exclude: l.Spec.Exclude}))
	}()

	if err := a.stream(ctx, http.MethodPut, "/v1/runs/"+l.Spec.ID+"/workspace", l.ID, pr); err != nil {
		_ = pr.CloseWithError(err)

		return res, fmt.Errorf("workspace to the manager: %w", err)
	}

	return res, nil
}

// download writes the manager's workspace tar into ws. The manager is trusted, but the tar goes
// through Mirror all the same: one way to write a workspace.
func (a *Agent) download(ctx context.Context, l *Lease, ws string) error {
	req, err := a.request(ctx, http.MethodGet, "/v1/runs/"+l.Spec.ID+"/input", l.ID, nil)
	if err != nil {
		return err
	}

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}

	return guestexec.Mirror(ws, resp.Body, guestexec.MirrorOptions{})
}

// uploader sends the worker's raw output to the manager in order, by byte offset, in batches.
// A failed post is retried with the same offset; the manager skips what it already has.
type uploader struct {
	a   *Agent
	l   *Lease
	log *slog.Logger
	max int64

	mu      sync.Mutex
	buf     []byte
	sent    int64
	closing bool
	err     error
	wake    chan struct{}
	done    chan struct{}
}

func (u *uploader) start(ctx context.Context) {
	u.wake, u.done = make(chan struct{}, 1), make(chan struct{})

	go u.loop(ctx)
}

func (u *uploader) Write(p []byte) (int, error) {
	u.mu.Lock()
	if u.max > 0 && u.sent+int64(len(u.buf))+int64(len(p)) > u.max {
		u.mu.Unlock()

		return len(p), nil // over the manager's limit: it fails the run anyway
	}

	u.buf = append(u.buf, p...)
	u.mu.Unlock()

	select {
	case u.wake <- struct{}{}:
	default:
	}

	return len(p), nil
}

func (u *uploader) loop(ctx context.Context) {
	defer close(u.done)

	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-u.wake:
		case <-t.C:
		case <-ctx.Done():
			return
		}

		u.mu.Lock()
		batch := u.buf[:min(len(u.buf), guestexec.MaxFrame)]
		closing := u.closing
		u.mu.Unlock()

		if len(batch) > 0 {
			if err := u.post(ctx, batch); err != nil {
				u.mu.Lock()
				u.err = err
				u.mu.Unlock()

				return
			}

			u.mu.Lock()
			u.buf = u.buf[len(batch):]
			u.sent += int64(len(batch))
			more := len(u.buf) > 0
			u.mu.Unlock()

			if more {
				select {
				case u.wake <- struct{}{}:
				default:
				}
			}

			continue
		}

		if closing {
			return
		}
	}
}

func (u *uploader) post(ctx context.Context, batch []byte) error {
	path := fmt.Sprintf("/v1/runs/%s/output?offset=%d", u.l.Spec.ID, u.sent)

	for attempt := 0; ; attempt++ {
		err := u.a.stream(ctx, http.MethodPost, path, u.l.ID, bytes.NewReader(batch))
		if err == nil || attempt == 4 {
			return err
		}

		u.log.Warn("output to the manager, retrying", "err", err)

		if !sleep(ctx, backoff(attempt)) {
			return ctx.Err()
		}
	}
}

// close flushes what is left and stops.
func (u *uploader) close() error {
	u.mu.Lock()
	u.closing = true
	u.mu.Unlock()

	select {
	case u.wake <- struct{}{}:
	default:
	}

	<-u.done

	u.mu.Lock()
	defer u.mu.Unlock()

	return u.err
}

var errNoContent = errors.New("no content")

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("%d: %s", e.code, e.msg) }

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	return &httpError{code: resp.StatusCode, msg: string(bytes.TrimSpace(b))}
}

func (a *Agent) request(ctx context.Context, method, path, lease string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.Manager+path, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+a.Token)

	if lease != "" {
		req.Header.Set(LeaseHeader, lease)
	}

	return req, nil
}

// call sends in as JSON and decodes the answer into out (if not nil).
func (a *Agent) call(ctx context.Context, method, path, lease string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := a.request(ctx, method, path, lease, bytes.NewReader(b))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		if out != nil {
			return errNoContent
		}

		return nil
	case resp.StatusCode/100 != 2:
		return statusError(resp)
	case out != nil:
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	}

	return nil
}

// stream sends body as it is, for tar streams and raw output. Uploads have no client timeout:
// a workspace can be large.
func (a *Agent) stream(ctx context.Context, method, path, lease string, body io.Reader) error {
	req, err := a.request(ctx, method, path, lease, body)
	if err != nil {
		return err
	}

	c := *a.HTTP
	c.Timeout = 0

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return statusError(resp)
	}

	return nil
}

func backoff(attempt int) time.Duration {
	return min(time.Duration(1<<attempt)*time.Second, 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}

	return b
}
