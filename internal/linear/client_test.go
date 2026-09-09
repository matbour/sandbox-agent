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

func TestAPIKeyAndMapping(t *testing.T) {
	srv, reqs, _ := fake(t, false)
	defer srv.Close()
	c := New("lin_api_x")
	c.endpoint = srv.URL + "/graphql"
	ctx := context.Background()
	if id, name, err := c.Me(ctx); err != nil || id != "u1" || name != "Sandbox Agent" {
		t.Fatalf("me: %s %s %v", id, name, err)
	}
	if (*reqs)[0].Auth != "lin_api_x" {
		t.Errorf("API key not sent as is: %q", (*reqs)[0].Auth)
	}
	is, err := c.IssueByID(ctx, "ENG-1")
	if err != nil {
		t.Fatal(err)
	}
	if is.Identifier != "ENG-1" || is.Description != "" || is.Team.Key != "ENG" || is.Creator.ID != "u2" ||
		is.Assignee != nil || is.Delegate.ID != "u1" || is.Labels.Nodes[0].Name != "agent" ||
		is.Comments.Nodes[0].Body != "hi" || is.Comments.Nodes[0].User != nil {
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
	c := NewApp("id", "sec", t.TempDir()+"/token.json")
	c.endpoint, c.tokenURL = srv.URL+"/graphql", srv.URL+"/oauth/token"
	if _, _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *minted != 2 || len(*reqs) != 2 || (*reqs)[0].Auth != "Bearer tok1" || (*reqs)[1].Auth != "Bearer tok2" {
		t.Fatalf("minted %d, auth %v", *minted, *reqs)
	}
}
