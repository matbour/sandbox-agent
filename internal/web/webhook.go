package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// linearWebhook verifies a Linear delivery and wakes the worker when an issue may need it.
// See https://linear.app/developers/webhooks.
func (s *Server) linearWebhook(w http.ResponseWriter, r *http.Request) {
	if len(s.LinearWebhookSecrets) == 0 {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !s.signed(body, r.Header.Get("Linear-Signature")) {
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
	if p.Timestamp == 0 { // not every payload type carries it; the header always does
		p.Timestamp, _ = strconv.ParseInt(r.Header.Get("Linear-Timestamp"), 10, 64)
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
	switch {
	case p.Type == "AgentSessionEvent" && s.AgentSession != nil:
		ev, err := parseAgentSession(body)
		if err != nil {
			s.Log.Warn("linear webhook: bad agent session event", "err", err)
			break
		}
		s.Log.Info("linear webhook: agent session", "action", ev.Action, "session", ev.SessionID, "issue", ev.Issue)
		s.AgentSession(ev)
	case p.Type == "Issue" && p.Action != "remove" && s.mentionsLabel(p.Data) && s.Trigger != nil:
		s.Log.Info("linear webhook: issue event, polling now", "action", p.Action)
		s.Trigger()
	case p.Type == "Comment" && p.Action == "create" && s.Mention != nil && s.Trigger != nil:
		var d struct {
			Body string `json:"body"`
		}
		if json.Unmarshal(p.Data, &d) == nil && s.Mention(d.Body) {
			s.Log.Info("linear webhook: agent mentioned, polling now")
			s.Trigger()
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) signed(body []byte, header string) bool {
	for _, secret := range s.LinearWebhookSecrets {
		if validSignature(secret, body, header) {
			return true
		}
	}
	return false
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
