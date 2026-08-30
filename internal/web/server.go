// Package web serves the Linear webhook receiver and a read-only overview of the jobs.
//
// It listens on loopback only and is exposed through a Cloudflare Tunnel:
//
//   - /webhooks/* bypass Cloudflare Access; each request is authenticated by its HMAC signature.
//   - Everything else is for people: behind Access, and the Access JWT is verified here too.
//     Requests without Cloudflare headers come from the host itself and are allowed, because
//     Cloudflare's edge always adds Cf-Connecting-Ip and a client cannot remove it.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server is the HTTP side of agentd.
type Server struct {
	StateDir            string
	LinearWebhookSecret []byte  // empty: the Linear webhook route answers 404
	Access              *Access // nil: only requests from the host itself reach the overview
	Labels              func() []string
	Trigger             func() // asks the worker to poll Linear now
	Log                 *slog.Logger

	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/linear", s.linearWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.Handle("GET /api/jobs", s.people(http.HandlerFunc(s.apiJobs)))
	mux.Handle("GET /{$}", s.people(http.HandlerFunc(s.overview)))
	return securityHeaders(mux)
}

// ListenAndServe serves on addr, which must be a loopback address, until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing to listen on %s: use a loopback address, the tunnel connects locally", addr)
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// people guards the routes meant for humans.
func (s *Server) people(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cf-Connecting-Ip") == "" {
			next.ServeHTTP(w, r) // from the host itself, not through the tunnel
			return
		}
		if s.Access == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		claims, err := s.Access.Verify(r.Context(), r.Header.Get("Cf-Access-Jwt-Assertion"))
		if err != nil {
			s.Log.Warn("access denied", "path", r.URL.Path, "err", err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Header.Set("X-Agentd-User", claims.Email)
		next.ServeHTTP(w, r)
	})
}

// linearWebhook verifies a Linear delivery and wakes the worker when an issue may need it.
// See https://linear.app/developers/webhooks.
func (s *Server) linearWebhook(w http.ResponseWriter, r *http.Request) {
	if len(s.LinearWebhookSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.LinearWebhookSecret, body, r.Header.Get("Linear-Signature")) {
		s.Log.Warn("linear webhook: bad signature", "remote", r.Header.Get("Cf-Connecting-Ip"))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p struct {
		Action    string          `json:"action"`
		Type      string          `json:"type"`
		Timestamp int64           `json:"webhookTimestamp"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	now := s.clock()
	if d := now.Sub(time.UnixMilli(p.Timestamp)); d > time.Minute || d < -time.Minute {
		s.Log.Warn("linear webhook: stale timestamp", "age", d)
		http.Error(w, "stale", http.StatusUnauthorized)
		return
	}
	if s.duplicate(r.Header.Get("Linear-Delivery"), now) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if p.Type == "Issue" && p.Action != "remove" && s.mentionsLabel(p.Data) && s.Trigger != nil {
		s.Log.Info("linear webhook: issue event, polling now", "action", p.Action)
		s.Trigger()
	}
	w.WriteHeader(http.StatusOK)
}

func validSignature(secret, body []byte, header string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// mentionsLabel reports whether the issue in an event carries one of the trigger labels. Events
// without label information also wake the worker: polling is cheap and decides for real.
func (s *Server) mentionsLabel(data json.RawMessage) bool {
	var d struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if json.Unmarshal(data, &d) != nil || d.Labels == nil || s.Labels == nil {
		return true
	}
	for _, l := range d.Labels {
		for _, t := range s.Labels() {
			if strings.EqualFold(l.Name, t) {
				return true
			}
		}
	}
	return false
}

// duplicate remembers delivery IDs for a day; Linear retries a failed delivery up to three times.
func (s *Server) duplicate(id string, now time.Time) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	for k, t := range s.seen {
		if now.Sub(t) > 24*time.Hour {
			delete(s.seen, k)
		}
	}
	if _, ok := s.seen[id]; ok {
		return true
	}
	s.seen[id] = now
	return false
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
