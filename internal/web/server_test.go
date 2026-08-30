package web

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func newServer(t *testing.T) (*Server, *int) {
	triggers := 0
	return &Server{
		StateDir: t.TempDir(), LinearWebhookSecret: []byte("s3cret"),
		Labels:  func() []string { return []string{"agent", "agent-opus"} },
		Trigger: func() { triggers++ },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, &triggers
}

func post(h http.Handler, body, sig, delivery string) int {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/linear", strings.NewReader(body))
	r.Header.Set("Linear-Signature", sig)
	r.Header.Set("Linear-Delivery", delivery)
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestLinearWebhook(t *testing.T) {
	s, triggers := newServer(t)
	h := s.Handler()
	now := time.Now().UnixMilli()
	body := fmt.Sprintf(`{"action":"update","type":"Issue","webhookTimestamp":%d,"data":{"labels":[{"name":"Agent"}]}}`, now)

	if c := post(h, body, sign("wrong", body), "d1"); c != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", c)
	}
	if c := post(h, body, "", "d1"); c != http.StatusUnauthorized {
		t.Fatalf("missing signature: %d", c)
	}
	if c := post(h, body, sign("s3cret", body), "d1"); c != http.StatusOK || *triggers != 1 {
		t.Fatalf("valid: %d, triggers %d", c, *triggers)
	}
	if c := post(h, body, sign("s3cret", body), "d1"); c != http.StatusOK || *triggers != 1 {
		t.Fatalf("duplicate delivery must not trigger again: %d, triggers %d", c, *triggers)
	}
	old := fmt.Sprintf(`{"action":"update","type":"Issue","webhookTimestamp":%d,"data":{}}`, now-5*60*1000)
	if c := post(h, old, sign("s3cret", old), "d2"); c != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: %d", c)
	}
	other := fmt.Sprintf(`{"action":"update","type":"Issue","webhookTimestamp":%d,"data":{"labels":[{"name":"bug"}]}}`, now)
	if c := post(h, other, sign("s3cret", other), "d3"); c != http.StatusOK || *triggers != 1 {
		t.Fatalf("unrelated label must not trigger: %d, triggers %d", c, *triggers)
	}
	s.LinearWebhookSecret = nil
	if c := post(s.Handler(), body, sign("s3cret", body), "d4"); c != http.StatusNotFound {
		t.Fatalf("disabled webhook: %d", c)
	}
}

func get(h http.Handler, path string, hdr map[string]string) int {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestOverviewAccess(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer certs.Close()

	s, _ := newServer(t)
	s.Access = &Access{Team: "team", AUD: "aud1",
		HTTP: &http.Client{Transport: rewrite{certs.URL}}}
	h := s.Handler()

	if c := get(h, "/", nil); c != http.StatusOK {
		t.Fatalf("local request: %d", c)
	}
	tunnel := map[string]string{"Cf-Connecting-Ip": "203.0.113.1"}
	if c := get(h, "/", tunnel); c != http.StatusForbidden {
		t.Fatalf("tunnel without JWT: %d", c)
	}
	exp := time.Now().Add(time.Hour).Unix()
	good := jwt(t, key, "k1", map[string]any{"iss": "https://team.cloudflareaccess.com", "aud": []string{"aud1"}, "exp": exp, "email": "a@b"})
	tunnel["Cf-Access-Jwt-Assertion"] = good
	if c := get(h, "/api/jobs", tunnel); c != http.StatusOK {
		t.Fatalf("valid JWT: %d", c)
	}
	for name, claims := range map[string]map[string]any{
		"wrong audience": {"iss": "https://team.cloudflareaccess.com", "aud": "other", "exp": exp},
		"wrong issuer":   {"iss": "https://evil.cloudflareaccess.com", "aud": "aud1", "exp": exp},
		"expired":        {"iss": "https://team.cloudflareaccess.com", "aud": "aud1", "exp": time.Now().Add(-time.Hour).Unix()},
	} {
		tunnel["Cf-Access-Jwt-Assertion"] = jwt(t, key, "k1", claims)
		if c := get(h, "/", tunnel); c != http.StatusForbidden {
			t.Errorf("%s: %d", name, c)
		}
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	tunnel["Cf-Access-Jwt-Assertion"] = jwt(t, other, "k1", map[string]any{"iss": "https://team.cloudflareaccess.com", "aud": "aud1", "exp": exp})
	if c := get(h, "/", tunnel); c != http.StatusForbidden {
		t.Errorf("forged signature: %d", c)
	}
	s.Access = nil
	if c := get(s.Handler(), "/", map[string]string{"Cf-Connecting-Ip": "203.0.113.1"}); c != http.StatusForbidden {
		t.Errorf("tunnel request without Access configured: %d", c)
	}
}

func TestLoopbackOnly(t *testing.T) {
	s, _ := newServer(t)
	if err := s.ListenAndServe(context.Background(), "0.0.0.0:0"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback refusal, got %v", err)
	}
}

func jwt(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
	p, _ := json.Marshal(claims)
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + enc.EncodeToString(sig)
}

// rewrite sends every request to a test server.
type rewrite struct{ base string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := r.base + req.URL.Path
	out, _ := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
	return http.DefaultTransport.RoundTrip(out)
}
