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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server is the HTTP side of agentd.
type Server struct {
	StateDir string
	// LinearWebhookSecrets are the signing secrets accepted (the workspace webhook and the OAuth
	// application's webhook have one each). Empty: the Linear webhook route answers 404.
	LinearWebhookSecrets [][]byte
	Access               *Access // nil: only requests from the host itself reach the overview
	Labels               func() []string
	Mention              func(body string) bool // whether a comment mentions the agent
	// AgentSession receives Linear agent session events (mention, delegation, follow-up message).
	// It must return quickly: Linear expects the webhook answer within 5 s.
	AgentSession func(ev AgentSessionEvent)
	Trigger      func() // asks the worker to poll Linear now
	Log          *slog.Logger

	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/linear", s.linearWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
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
