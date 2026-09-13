package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPI serves the GitHub endpoints the package uses and records requests.
func fakeAPI(t *testing.T, key *rsa.PrivateKey) (*httptest.Server, *[]string) {
	var seen []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			http.Error(w, "no JWT", http.StatusUnauthorized)
			return
		}

		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		if !strings.Contains(string(claims), `"iss":"7"`) {
			http.Error(w, "wrong issuer: "+string(claims), http.StatusUnauthorized)
			return
		}

		body, _ := io.ReadAll(r.Body)
		seen = append(seen, "token "+string(body))
		_, _ = fmt.Fprintf(w, `{"token":"ghs_test","expires_at":"%s"}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer ghs_test" {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}

			h(w, r)
		}
	}
	page := func(w http.ResponseWriter, r *http.Request, srv string, first, second string) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, second)
			return
		}

		w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, srv, r.URL.Path))
		_, _ = fmt.Fprint(w, first)
	}

	var srv *httptest.Server

	mux.HandleFunc("GET /repos/o/r", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"default_branch":"main"}`)
	}))
	mux.HandleFunc("POST /repos/o/r/pulls", auth(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, "create "+string(body))
		_, _ = fmt.Fprint(w, `{"number":20,"node_id":"PR_x","html_url":"https://github.com/o/r/pull/20"}`)
	}))
	mux.HandleFunc("GET /repos/o/r/issues/20/comments", auth(func(w http.ResponseWriter, r *http.Request) {
		page(w, r, srv.URL, `[{"user":{"login":"a"},"body":"one"}]`, `[{"user":{"login":"b"},"body":"two"}]`)
	}))
	mux.HandleFunc("GET /repos/o/r/pulls/20/reviews", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"user":{"login":"c"},"body":"please fix","state":"CHANGES_REQUESTED"},{"user":{"login":"d"},"body":"","state":"APPROVED"}]`)
	}))
	mux.HandleFunc("GET /repos/o/r/pulls/20/comments", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"user":{"login":"e"},"body":"nit","path":"a.ts","line":3}]`)
	}))
	mux.HandleFunc("GET /repos/o/r/commits/abc/check-runs", auth(func(w http.ResponseWriter, r *http.Request) {
		page(w, r, srv.URL, `{"check_runs":[{"name":"Tests","status":"completed","conclusion":"failure","output":{"title":"2 failed","summary":"x"}}]}`,
			`{"check_runs":[{"name":"Lint","status":"in_progress"}]}`)
	}))
	mux.HandleFunc("GET /repos/o/r/commits/abc/status", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"statuses":[{"context":"Vercel","state":"failure","description":"Deployment has failed"}]}`)
	}))
	mux.HandleFunc("POST /graphql", auth(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, "graphql "+string(body))
		_, _ = fmt.Fprint(w, `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`)
	}))
	mux.HandleFunc("POST /repos/o/r/issues/20/comments", auth(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, "comment "+string(body))
		_, _ = fmt.Fprint(w, `{}`)
	}))
	srv = httptest.NewServer(mux)

	return srv, &seen
}

func TestAppAndRepo(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	srv, seen := fakeAPI(t, key)
	defer srv.Close()

	ctx := context.Background()

	app, err := newApp("7", "42", pemKey, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}

	tok, err := app.InstallationToken(ctx, "r")
	if err != nil || tok.Value != "ghs_test" || time.Until(tok.ExpiresAt) < 50*time.Minute {
		t.Fatalf("token: %+v %v", tok, err)
	}

	if !strings.Contains((*seen)[0], `"repositories":["r"]`) {
		t.Errorf("token not restricted to the repository: %s", (*seen)[0])
	}

	repo := &Repo{Token: tok, Owner: "o", Name: "r", baseURL: srv.URL + "/"}
	if b, err := repo.DefaultBranch(ctx); err != nil || b != "main" {
		t.Fatalf("default branch: %q %v", b, err)
	}

	pr, err := repo.CreateDraftPR(ctx, "agent/x", "main", "T", "B")
	if err != nil || pr.Number != 20 || pr.NodeID != "PR_x" {
		t.Fatalf("create: %+v %v", pr, err)
	}

	var created map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix((*seen)[1], "create ")), &created); err != nil {
		t.Fatal(err)
	}

	if created["draft"] != true || created["head"] != "agent/x" || created["base"] != "main" {
		t.Errorf("create body: %v", created)
	}

	fb, err := repo.PRFeedback(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, f := range fb {
		got = append(got, f.Author+":"+f.Kind+":"+f.Body)
	}

	want := "a:comment:one|b:comment:two|c:review (changes_requested):please fix|e:review comment:nit"
	if strings.Join(got, "|") != want {
		t.Errorf("feedback (pagination?):\n got %s\nwant %s", strings.Join(got, "|"), want)
	}

	checks, err := repo.Checks(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}

	var cs []string
	for _, c := range checks {
		cs = append(cs, c.Name+"="+c.Conclusion)
	}

	if strings.Join(cs, ",") != "Tests=failure,Lint=in_progress,Vercel=failure" {
		t.Errorf("checks: %v", cs)
	}

	if err := repo.MarkReady(ctx, "PR_x"); err != nil {
		t.Fatal(err)
	}

	if err := repo.Comment(ctx, 20, "hello"); err != nil {
		t.Fatal(err)
	}

	last := strings.Join((*seen)[len(*seen)-2:], "\n")
	if !strings.Contains(last, "markPullRequestReadyForReview") || !strings.Contains(last, `"variables":{"id":"PR_x"}`) ||
		!strings.Contains(last, `comment {"body":"hello"}`) {
		t.Errorf("mark ready / comment: %s", last)
	}
}

func TestNewAppErrors(t *testing.T) {
	if _, err := newApp("x", "1", nil, ""); err == nil {
		t.Error("bad App ID accepted")
	}

	if _, err := newApp("1", "1", []byte("not a key"), ""); err == nil {
		t.Error("bad key accepted")
	}
}

func TestPRNumber(t *testing.T) {
	if n, ok := PRNumber("https://github.com/o/r/pull/20"); !ok || n != 20 {
		t.Fatal(n, ok)
	}

	if _, ok := PRNumber("https://github.com/o/r/issues/20"); ok {
		t.Fatal("issue URL accepted")
	}
}
