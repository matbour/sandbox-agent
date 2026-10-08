// Command forwarder runs inside the sandbox. It listens on a loopback TCP address, where agents
// find the egress proxy (ANTHROPIC_BASE_URL=http://127.0.0.1:8080/anthropic), and relays every
// connection to the host: over vsock in a microVM, over a Unix socket in the layer 1 sandbox.
//
//	forwarder -to vsock:2:8080
//	forwarder -to unix:/run/egress.sock
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/matbour/sandbox-agent/internal/forward"
	"github.com/matbour/sandbox-agent/internal/vsock"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "loopback TCP address to listen on")
	to := flag.String("to", fmt.Sprintf("vsock:%d:8080", vsock.HostCID), "target: vsock:<cid>:<port> or unix:<path>")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*listen, *to, log); err != nil {
		log.Error("forwarder", "err", err)
		os.Exit(1)
	}
}

func run(listen, to string, log *slog.Logger) error {
	dial, err := dialer(to)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("forwarding", "listen", ln.Addr().String(), "to", to)

	return forward.Serve(ctx, ln, dial, log)
}

func dialer(to string) (forward.Dialer, error) {
	kind, addr, _ := strings.Cut(to, ":")

	switch kind {
	case "unix":
		return func() (forward.Conn, error) {
			c, err := net.Dial("unix", addr)
			if err != nil {
				return nil, err
			}

			return c.(*net.UnixConn), nil
		}, nil
	case "vsock":
		cid, port, ok := strings.Cut(addr, ":")

		c, err1 := strconv.ParseUint(cid, 10, 32)
		p, err2 := strconv.ParseUint(port, 10, 32)

		if !ok || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad vsock target %q, want vsock:<cid>:<port>", to)
		}

		return func() (forward.Conn, error) {
			return vsock.Dial(uint32(c), uint32(p))
		}, nil
	}

	return nil, errors.New("target must be vsock:<cid>:<port> or unix:<path>")
}
