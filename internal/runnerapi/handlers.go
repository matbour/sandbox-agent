package runnerapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

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
