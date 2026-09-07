package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentSessionWebhook(t *testing.T) {
	s, _ := newServer(t)
	var got []AgentSessionEvent
	s.AgentSession = func(ev AgentSessionEvent) { got = append(got, ev) }
	h := s.Handler()
	now := time.Now().UnixMilli()
	created := `{"type":"AgentSessionEvent","action":"created","agentSession":{"id":"s1","issue":{"id":"i1","identifier":"ENG-1"},"comment":{"id":"c1","body":"@Sandbox Agent fix it"},"creator":{"name":"Mathieu"}}}`
	r := httptest.NewRequest(http.MethodPost, "/webhooks/linear", strings.NewReader(created))
	r.Header.Set("Linear-Signature", sign("s3cret", created))
	r.Header.Set("Linear-Timestamp", fmt.Sprint(now)) // no webhookTimestamp in the body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(got) != 1 {
		t.Fatalf("created: %d %d", w.Code, len(got))
	}
	ev := got[0]
	if ev.SessionID != "s1" || ev.IssueID != "i1" || ev.Issue != "ENG-1" || ev.CommentID != "c1" ||
		ev.Request != "@Sandbox Agent fix it" || ev.Requester != "Mathieu" {
		t.Fatalf("parsed %+v", ev)
	}
	prompted := fmt.Sprintf(`{"type":"AgentSessionEvent","action":"prompted","webhookTimestamp":%d,"agentSession":{"id":"s1","issue":{"id":"i1","identifier":"ENG-1"}},"agentActivity":{"body":"also the tests","user":{"name":"Mathieu"}}}`, now)
	if c := post(h, prompted, sign("s3cret", prompted), "p1"); c != http.StatusOK || len(got) != 2 || got[1].Request != "also the tests" {
		t.Fatalf("prompted: %d %+v", c, got)
	}
}

func TestAgentSessionStopSignal(t *testing.T) {
	s, _ := newServer(t)
	var got []AgentSessionEvent
	s.AgentSession = func(ev AgentSessionEvent) { got = append(got, ev) }
	body := fmt.Sprintf(`{"type":"AgentSessionEvent","action":"prompted","webhookTimestamp":%d,"agentSession":{"id":"s1","issue":{"id":"i1","identifier":"ENG-1"}},"agentActivity":{"signal":"stop","content":{"type":"prompt","body":"stop"}}}`, time.Now().UnixMilli())
	if c := post(s.Handler(), body, sign("s3cret", body), "x1"); c != http.StatusOK || len(got) != 1 || got[0].Signal != "stop" || got[0].Request != "stop" {
		t.Fatalf("%d %+v", c, got)
	}
}
