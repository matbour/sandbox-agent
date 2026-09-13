package web

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
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
