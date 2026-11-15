// Package sandboxtest is the conformance suite every sandbox provider must pass. It starts real
// sandboxes, so providers run it from an integration test gated by an environment variable:
//
//	SANDBOX_IT=1 go test ./backends/sbx/ -run Conformance -v
package sandboxtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// Options relax the suite for a provider's known, recorded exceptions.
type Options struct {
	// OpenNetwork: the sandbox reaches the LAN and the host on purpose (D44, development phase).
	// The reachability checks are skipped, not passed.
	OpenNetwork bool
}

// Run checks the contract of package sandbox against p. workRoot is a host directory under which
// the workspace is created (some providers only mount from allowed locations).
func Run(t *testing.T, p sandbox.Provider, workRoot string) { RunWith(t, p, workRoot, Options{}) }

// RunWith is Run with exceptions.
func RunWith(t *testing.T, p sandbox.Provider, workRoot string, o Options) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := p.Prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	ws, err := os.MkdirTemp(workRoot, "conformance-")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = os.RemoveAll(ws) }()

	if err := os.WriteFile(filepath.Join(ws, "from-host.txt"), []byte("hello from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()

	sb, err := p.Start(ctx, sandbox.Spec{Name: fmt.Sprintf("conformance-%d", time.Now().Unix()%100000),
		Workspace: ws, CPUs: 1, Memory: "1g"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	t.Logf("%s: started %s in %s", p.Name(), sb.Name(), time.Since(start).Round(time.Millisecond))

	closed := false
	defer func() {
		if !closed {
			_ = sb.Close()
		}
	}()

	sh := func(o sandbox.ExecOptions, script string) (string, string, error) {
		so, se, err := sb.Exec(ctx, o, "sh", "-c", script)
		return strings.TrimSpace(string(so)), strings.TrimSpace(string(se)), err
	}

	if err := os.WriteFile(filepath.Join(ws, "deleted-in-sandbox.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("workspace", func(t *testing.T) {
		out, se, err := sh(sandbox.ExecOptions{}, "pwd; cat from-host.txt; echo from sandbox > from-sandbox.txt; "+
			"rm deleted-in-sandbox.txt; mkdir -p node_modules/dep && echo x > node_modules/dep/index.js")
		if err != nil {
			t.Fatalf("%v: %s", err, se)
		}

		if s, ok := sb.(sandbox.Syncer); ok {
			if err := s.SyncWorkspace(ctx, []string{"node_modules/"}); err != nil {
				t.Fatalf("sync: %v", err)
			}

			if _, err := os.Stat(filepath.Join(ws, "deleted-in-sandbox.txt")); err == nil {
				t.Error("a file deleted in the sandbox is still on the host after sync")
			}

			if _, err := os.Stat(filepath.Join(ws, "node_modules")); err == nil {
				t.Error("an excluded directory was synced to the host")
			}
		}

		if !strings.HasPrefix(out, sb.WorkDir()+"\n") || !strings.Contains(out, "hello from host") {
			t.Errorf("workdir or host file not visible: %q", out)
		}

		if b, err := os.ReadFile(filepath.Join(ws, "from-sandbox.txt")); err != nil || strings.TrimSpace(string(b)) != "from sandbox" {
			t.Errorf("file written in the sandbox not visible on the host: %v %q", err, b)
		}
	})
	t.Run("output and exit status", func(t *testing.T) {
		so, se, err := sb.Exec(ctx, sandbox.ExecOptions{}, "sh", "-c", "echo out; echo err >&2; exit 3")
		if err == nil || strings.TrimSpace(string(so)) != "out" || strings.TrimSpace(string(se)) != "err" {
			t.Errorf("stdout %q stderr %q err %v; want out, err and an exit status error", so, se, err)
		}
	})
	t.Run("stdin and streamed stdout", func(t *testing.T) {
		var out strings.Builder

		_, _, err := sb.Exec(ctx, sandbox.ExecOptions{Stdin: strings.NewReader("line one\nline two\n"), Stdout: &out},
			"sh", "-c", "cat; echo done")
		if errors.Is(err, sandbox.ErrNoStreaming) {
			t.Skip(err)
		}

		if err != nil || out.String() != "line one\nline two\ndone\n" {
			t.Errorf("streamed output %q, err %v", out.String(), err)
		}
	})
	t.Run("unprivileged by default", func(t *testing.T) {
		out, _, err := sh(sandbox.ExecOptions{}, "id -u")
		if err != nil || out == "0" {
			t.Errorf("default user is root inside the sandbox (%q, %v)", out, err)
		}
	})
	t.Run("env", func(t *testing.T) {
		out, _, err := sh(sandbox.ExecOptions{Env: []string{"CONFORMANCE=42"}}, "echo $CONFORMANCE")
		if err != nil || out != "42" {
			t.Errorf("env not passed: %q %v", out, err)
		}
	})
	t.Run("no API key inside", func(t *testing.T) {
		out, _, _ := sh(sandbox.ExecOptions{}, "env")
		for _, line := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(line, "=")
			if strings.HasPrefix(v, "sk-ant-") || (strings.Contains(k, "ANTHROPIC") && strings.HasPrefix(v, "sk-")) {
				t.Errorf("an API key is visible in the sandbox: %s=…", k)
			}
		}
	})
	t.Run("cancel stops the command", func(t *testing.T) {
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
		defer ccancel()

		begin := time.Now()

		_, _, err := sb.Exec(cctx, sandbox.ExecOptions{}, "sleep", "60")
		if err == nil || time.Since(begin) > 30*time.Second {
			t.Errorf("cancelled exec returned after %s, err %v", time.Since(begin), err)
		}
	})
	t.Run("host and LAN unreachable", func(t *testing.T) {
		if o.OpenNetwork {
			t.Skip("open network on purpose (D44, development phase)")
		}

		if _, _, err := sh(sandbox.ExecOptions{}, "command -v curl"); err != nil {
			t.Fatal("curl is missing in the sandbox: reachability cannot be checked")
		}

		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()

		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}

				_, _ = fmt.Fprint(c, "HTTP/1.0 200 OK\r\n\r\nreached-the-host")
				_ = c.Close()
			}
		}()

		port := l.Addr().(*net.TCPAddr).Port

		targets := []string{"http://169.254.169.254/"}
		if ip := lanIP(); ip != "" {
			targets = append(targets, fmt.Sprintf("http://%s:%d/", ip, port))
		}

		for _, u := range targets {
			out, _, _ := sh(sandbox.ExecOptions{}, "curl -s -m 5 "+u+" || true")
			if strings.Contains(out, "reached-the-host") || strings.Contains(out, "ami-id") {
				t.Errorf("%s is reachable from the sandbox", u)
			}
		}
	})
	t.Run("close removes the sandbox", func(t *testing.T) {
		if err := sb.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		closed = true

		if _, _, err := sb.Exec(ctx, sandbox.ExecOptions{}, "true"); err == nil {
			t.Error("exec still works after Close")
		}
	})
}

// lanIP returns the host's first non-loopback IPv4 address.
func lanIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && n.IP.IsPrivate() {
			return n.IP.String()
		}
	}

	return ""
}
