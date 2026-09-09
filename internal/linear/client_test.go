package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
	Auth      string
}

// fake serves Linear's GraphQL endpoint and OAuth token endpoint. reject401 makes the first
// GraphQL call fail with 401, as with a revoked token.
func fake(t *testing.T, reject401 bool) (*httptest.Server, *[]gqlReq, *int) {
	var reqs []gqlReq
	minted := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "sec" {
			http.Error(w, "bad", 400)
			return
		}
		minted++
		fmt.Fprintf(w, `{"access_token":"tok%d","expires_in":2592000}`, minted)
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var q gqlReq
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		q.Auth = r.Header.Get("Authorization")
		reqs = append(reqs, q)
		if reject401 && len(reqs) == 1 {
			http.Error(w, "revoked", 401)
			return
		}
		switch {
		case strings.Contains(q.Query, "query Viewer"):
			fmt.Fprint(w, `{"data":{"viewer":{"id":"u1","name":"Sandbox Agent"}}}`)
		case strings.Contains(q.Query, "query IssueByID"):
			fmt.Fprint(w, `{"data":{"issue":{"id":"i1","identifier":"ENG-1","title":"T","description":null,"url":"https://l/ENG-1",
				"team":{"key":"ENG"},"creator":{"id":"u2","name":"Mathieu"},"assignee":null,"delegate":{"id":"u1","name":"Sandbox Agent"},
				"labels":{"nodes":[{"id":"l1","name":"agent"}]},"comments":{"nodes":[{"body":"hi","user":null}]}}}}`)
		case strings.Contains(q.Query, "mutation CreateComment"), strings.Contains(q.Query, "mutation CreateAgentActivity"):
			fmt.Fprint(w, `{"data":{"commentCreate":{"success":true},"agentActivityCreate":{"success":true}}}`)
		default:
			fmt.Fprint(w, `{"errors":[{"message":"unexpected query"}]}`)
		}
	})
	return httptest.NewServer(mux), &reqs, &minted
}

func TestMapping(t *testing.T) {
	srv, reqs, _ := fake(t, false)
	defer srv.Close()
	c := New("id", "sec", "")
	c.endpoint, c.tokenURL = srv.URL+"/graphql", srv.URL+"/oauth/token"
	ctx := context.Background()
	if me, err := c.Me(ctx); err != nil || me.ID != "u1" || me.Name != "Sandbox Agent" {
		t.Fatalf("me: %+v %v", me, err)
	}
	if (*reqs)[0].Auth != "Bearer tok1" {
		t.Errorf("application token not sent: %q", (*reqs)[0].Auth)
	}
	is, err := c.IssueByID(ctx, "ENG-1")
	if err != nil {
		t.Fatal(err)
	}
	if is.Identifier != "ENG-1" || is.Description != "" || is.TeamKey != "ENG" || is.Creator.ID != "u2" ||
		is.Assignee != nil || is.Delegate.ID != "u1" || is.Labels[0].Name != "agent" ||
		is.Comments[0].Body != "hi" || is.Comments[0].Author != nil {
		t.Errorf("mapping: %+v", is)
	}
	if err := c.Reply(ctx, "i1", "c9", "hello"); err != nil {
		t.Fatal(err)
	}
	v := (*reqs)[len(*reqs)-1].Variables
	if v["issueId"] != "i1" || v["parentId"] != "c9" || v["body"] != "hello" {
		t.Errorf("reply variables: %v", v)
	}
	if err := c.AgentActivity(ctx, "s1", "thought", "on it"); err != nil {
		t.Fatal(err)
	}
	v = (*reqs)[len(*reqs)-1].Variables
	if content, _ := v["content"].(map[string]any); v["session"] != "s1" || content["type"] != "thought" || content["body"] != "on it" {
		t.Errorf("activity variables: %v", v)
	}
}

func TestAppTokenRetriesOn401(t *testing.T) {
	srv, reqs, minted := fake(t, true)
	defer srv.Close()
	c := New("id", "sec", t.TempDir()+"/token.json")
	c.endpoint, c.tokenURL = srv.URL+"/graphql", srv.URL+"/oauth/token"
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *minted != 2 || len(*reqs) != 2 || (*reqs)[0].Auth != "Bearer tok1" || (*reqs)[1].Auth != "Bearer tok2" {
		t.Fatalf("minted %d, auth %v", *minted, *reqs)
	}
}
