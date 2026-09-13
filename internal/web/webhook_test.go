package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	s.LinearWebhookSecrets = nil
	if c := post(s.Handler(), body, sign("s3cret", body), "d4"); c != http.StatusNotFound {
		t.Fatalf("disabled webhook: %d", c)
	}
}

func TestCommentMentionWakes(t *testing.T) {
	s, triggers := newServer(t)
	s.Mention = func(b string) bool { return strings.Contains(b, "@Sandbox Agent") }
	h := s.Handler()
	now := time.Now().UnixMilli()
	yes := fmt.Sprintf(`{"action":"create","type":"Comment","webhookTimestamp":%d,"data":{"body":"@Sandbox Agent rebase please"}}`, now)

	no := fmt.Sprintf(`{"action":"create","type":"Comment","webhookTimestamp":%d,"data":{"body":"looks good"}}`, now)
	if post(h, no, sign("s3cret", no), "c1"); *triggers != 0 {
		t.Fatal("plain comment woke the worker")
	}

	if post(h, yes, sign("s3cret", yes), "c2"); *triggers != 1 {
		t.Fatal("mention did not wake the worker")
	}
}
