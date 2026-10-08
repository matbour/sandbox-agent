package egress

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testKey = "sk-test-secret"

// upstream records the last request it received and answers 200 with "ok".
type upstream struct {
	last *http.Request
	body string
}

func newProxy(t *testing.T) (*httptest.Server, *upstream) {
	t.Helper()

	up := &upstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		up.last, up.body = r, string(b)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)

	p, err := New([]Route{Anthropic(u, testKey)}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	front := httptest.NewServer(p)
	t.Cleanup(front.Close)

	return front, up
}

func TestForwardsWithKey(t *testing.T) {
	front, up := newProxy(t)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/anthropic/v1/messages?beta=true", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("X-Api-Key", "placeholder")
	req.Header.Set("Authorization", "Bearer from-the-sandbox")
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}

	if up.last == nil {
		t.Fatal("upstream not called")
	}

	if got := up.last.URL.RequestURI(); got != "/v1/messages?beta=true" {
		t.Errorf("upstream path %q", got)
	}

	if got := up.last.Header.Get("X-Api-Key"); got != testKey {
		t.Errorf("key %q, want the route's key", got)
	}

	if got := up.last.Header.Get("Authorization"); got != "" {
		t.Errorf("sandbox Authorization header reached the upstream: %q", got)
	}

	if got := up.last.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("other headers must pass: Anthropic-Version %q", got)
	}

	if up.body != `{"model":"m"}` {
		t.Errorf("body %q", up.body)
	}
}

// E-NET-6: the proxy is not an open proxy and forwards only the paths it knows.
func TestRejects(t *testing.T) {
	front, up := newProxy(t)
	addr := strings.TrimPrefix(front.URL, "http://")

	cases := map[string]string{
		"connect":         "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
		"absolute URL":    "POST http://example.com/anthropic/v1/messages HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n",
		"unknown route":   "POST /other/v1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
		"path not listed": "POST /anthropic/v1/files HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
		"wrong method":    "GET /anthropic/v1/messages HTTP/1.1\r\nHost: x\r\n\r\n",
		"dot dot":         "POST /anthropic/v1/messages/../files HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
		"encoded slash":   "POST /anthropic/v1%2Fmessages HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
		"double slash":    "POST /anthropic//v1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
		"prefix only":     "POST /anthropicv1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			up.last = nil

			status := rawRequest(t, addr, raw)
			if status < 400 {
				t.Errorf("status %d, want an error", status)
			}

			if up.last != nil {
				t.Errorf("upstream called with %s", up.last.URL)
			}
		})
	}
}

// E-SEC-2: the key never comes back, not even when the upstream is down.
func TestUpstreamDown(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")

	p, err := New([]Route{Anthropic(u, testKey)}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader("{}"))
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d", rec.Code)
	}

	if strings.Contains(rec.Body.String(), testKey) || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Errorf("error leaks details: %q", rec.Body.String())
	}
}

func TestBodyLimit(t *testing.T) {
	front, _ := newProxy(t)

	resp, err := http.Post(front.URL+"/anthropic/v1/messages", "application/json", io.LimitReader(zeros{}, MaxBody+1))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 400 {
		t.Errorf("status %d for a body over the limit", resp.StatusCode)
	}
}

func TestNewChecksRoutes(t *testing.T) {
	u, _ := url.Parse(AnthropicUpstream)

	if _, err := New([]Route{Anthropic(u, "")}, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("a route without a key must be refused")
	}
}

func rawRequest(t *testing.T, addr, raw string) int {
	t.Helper()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	return resp.StatusCode
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) {
	clear(b)

	return len(b), nil
}
