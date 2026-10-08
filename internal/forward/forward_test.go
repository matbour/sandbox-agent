package forward

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// The target reads the whole request until end of file, then answers: this only works if the
// forwarder passes the client's half-close through.
func TestRelayHalfClose(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "target.sock")

	target, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()

	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}

			b, _ := io.ReadAll(c)
			_, _ = io.WriteString(c, strings.ToUpper(string(b)))
			_ = c.Close()
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	dial := func() (Conn, error) {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return nil, err
		}

		return c.(*net.UnixConn), nil
	}

	go func() { done <- Serve(ctx, ln, dial, slog.New(slog.DiscardHandler)) }()

	for _, msg := range []string{"hello", "second connection"} {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}

		_, _ = io.WriteString(c, msg)
		_ = c.(*net.TCPConn).CloseWrite()

		got, err := io.ReadAll(c)
		_ = c.Close()

		if err != nil || string(got) != strings.ToUpper(msg) {
			t.Fatalf("got %q, %v", got, err)
		}
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}
