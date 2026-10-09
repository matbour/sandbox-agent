// Command vsock-to-anthropic runs on the host. It listens on the Unix socket that Firecracker
// connects a guest's vsock port to (<uds_path>_<port>), and forwards the Anthropic API calls it
// allows to api.anthropic.com with the key added (M4, docs/architecture.md). The key never enters
// the guest; loopback-to-vsock is the guest side.
//
//	vsock-to-anthropic -listen build/firecracker/<run>/v.sock_8080
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/egress"
)

func main() {
	listen := flag.String("listen", "", "Unix socket to listen on, e.g. <vsock uds_path>_8080 for Firecracker")
	keyFile := flag.String("key-file", filepath.Join("secrets", config.AnthropicFile), "file holding the Anthropic API key (mode 0600)")
	upstream := flag.String("anthropic-upstream", egress.AnthropicUpstream, "Anthropic API base URL")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*listen, *keyFile, *upstream, log); err != nil {
		log.Error("vsock-to-anthropic", "err", err)
		os.Exit(1)
	}
}

func run(listen, keyFile, upstream string, log *slog.Logger) error {
	if listen == "" {
		return errors.New("-listen is required")
	}

	key, err := readKey(keyFile)
	if err != nil {
		return err
	}

	up, err := url.Parse(upstream)
	if err != nil {
		return err
	}

	proxy, err := egress.New([]egress.Route{egress.Anthropic(up, key)}, log)
	if err != nil {
		return err
	}

	// A socket left by a previous run would make Listen fail.
	if err := os.Remove(listen); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	ln, err := net.Listen("unix", listen)
	if err != nil {
		return err
	}

	// Only this user may connect: Firecracker runs as the same user.
	if err := os.Chmod(listen, 0o600); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	// Serve returns as soon as Shutdown starts, not when it ends: done lets run wait for the
	// in-flight requests (often long streaming responses) before the process exits.
	done := make(chan struct{})

	go func() {
		defer close(done)

		<-ctx.Done()

		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Shutdown does not close active connections when the deadline passes; Close does.
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
		}
	}()

	log.Info("listening", "socket", listen, "upstream", up.String())

	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-done

	return nil
}

func readKey(path string) (string, error) {
	if err := config.CheckPrivate(path); err != nil {
		return "", err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errors.New(path + " is empty")
	}

	return key, nil
}
