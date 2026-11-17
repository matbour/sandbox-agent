package runnerapi

import (
	"context"
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
