// Package egress is the host-side proxy that sandboxes use to reach model APIs (M4,
// docs/architecture.md). The sandbox speaks plain HTTP to it, without credentials; the proxy checks
// each request against a fixed list of routes, injects the API key and forwards it over TLS.
//
// It is reachable from the sandbox, so it is attack surface: it is not a generic forward proxy
// (no CONNECT, no absolute URLs, no other hosts), it only forwards the exact paths it knows, and
// it never returns the key or its own errors in detail (E-NET-6, E-SEC-2).
package egress

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

// MaxBody bounds the size of a request body. Claude Code's requests carry the whole conversation,
// a few megabytes at most.
const MaxBody = 32 << 20

// Route maps a path prefix to an upstream API.
type Route struct {
	Prefix   string   // path prefix seen by the sandbox, e.g. "/anthropic"
	Upstream *url.URL // scheme and host of the API, e.g. https://api.anthropic.com
	Header   string   // header that carries the key, e.g. "x-api-key"
	Key      string   // the key; never sent back to the sandbox
	Allow    []string // allowed "METHOD /path" pairs, the path taken after the prefix
}

// credentialHeaders are removed from every request: the sandbox has no credential to send, and
// whatever it sends must not reach the upstream.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Cookie", "Proxy-Authorization"}

// Proxy serves the routes. It implements http.Handler.
type Proxy struct {
	routes []Route
	log    *slog.Logger
	rp     *httputil.ReverseProxy
}

// New returns a proxy for routes. Each route needs a prefix, an upstream, a header and a key.
func New(routes []Route, log *slog.Logger) (*Proxy, error) {
	for _, r := range routes {
		if r.Prefix == "" || !strings.HasPrefix(r.Prefix, "/") || strings.HasSuffix(r.Prefix, "/") {
			return nil, errors.New("egress: route prefix must start with / and not end with /")
		}

		if r.Upstream == nil || r.Upstream.Host == "" || r.Header == "" {
			return nil, errors.New("egress: route " + r.Prefix + " needs an upstream and a header")
		}

		if r.Key == "" {
			return nil, errors.New("egress: route " + r.Prefix + " has no key")
		}
	}

	p := &Proxy{routes: routes, log: log}
	p.rp = &httputil.ReverseProxy{
		Rewrite: p.rewrite,

		// Streamed responses (server-sent events) go to the sandbox as they arrive.
		FlushInterval: -1,

		// The upstream error can name hosts or addresses; the sandbox gets a bare 502.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("upstream", "path", r.URL.Path, "err", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}

	return p, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}

	route, rest, reason := p.match(r)
	if reason != "" {
		p.log.Warn("rejected", "method", r.Method, "uri", r.RequestURI, "reason", reason)
		http.Error(rec, reason, http.StatusForbidden)

		return
	}

	r.Body = http.MaxBytesReader(rec, r.Body, MaxBody)
	r.URL.Path = rest
	r.URL.RawPath = ""
	r = r.WithContext(withRoute(r.Context(), route))

	p.rp.ServeHTTP(rec, r)

	p.log.Info("request", "route", route.Prefix, "method", r.Method, "path", rest,
		"status", rec.status, "bytes", rec.bytes, "ms", time.Since(start).Milliseconds())
}

// match finds the route for r and the path after the prefix. It returns a reason when r must be
// rejected.
func (p *Proxy) match(r *http.Request) (*Route, string, string) {
	// Origin-form only: "/path?query". This rejects CONNECT ("host:port"), absolute URLs
	// ("http://host/path") and "*".
	if r.Method == http.MethodConnect || !strings.HasPrefix(r.RequestURI, "/") {
		return nil, "", "not a proxy"
	}

	// No encoded characters in the path (%2F, %2E) and nothing that cleaning would change
	// ("..", "//", "/./"): the path checked is the path forwarded.
	if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") || path.Clean(r.URL.Path) != r.URL.Path {
		return nil, "", "path not canonical"
	}

	for i := range p.routes {
		route := &p.routes[i]

		rest, ok := strings.CutPrefix(r.URL.Path, route.Prefix)
		if !ok || !strings.HasPrefix(rest, "/") {
			continue
		}

		if slices.Contains(route.Allow, r.Method+" "+rest) {
			return route, rest, ""
		}

		return nil, "", "path not allowed"
	}

	return nil, "", "no route"
}

// rewrite builds the upstream request: the route's host, the remaining path and query, no
// credential from the sandbox, the route's key.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	route := routeFrom(pr.In.Context())

	pr.Out.URL.Scheme = route.Upstream.Scheme
	pr.Out.URL.Host = route.Upstream.Host
	pr.Out.Host = route.Upstream.Host

	for _, h := range credentialHeaders {
		pr.Out.Header.Del(h)
	}

	pr.Out.Header.Set(route.Header, route.Key)
}

// statusWriter records the status and size of a response. It keeps http.Flusher working, which
// the reverse proxy needs to stream.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)

	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
