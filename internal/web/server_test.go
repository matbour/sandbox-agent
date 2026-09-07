package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func newServer(t *testing.T) (*Server, *int) {
	triggers := 0
	return &Server{
		StateDir: t.TempDir(), LinearWebhookSecrets: [][]byte{[]byte("other"), []byte("s3cret")},
		Labels:  func() []string { return []string{"agent", "agent-opus"} },
		Trigger: func() { triggers++ },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, &triggers
}

func TestLoopbackOnly(t *testing.T) {
	s, _ := newServer(t)
	if err := s.ListenAndServe(context.Background(), "0.0.0.0:0"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback refusal, got %v", err)
	}
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := r.base + req.URL.Path
	out, _ := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
	return http.DefaultTransport.RoundTrip(out)
}
