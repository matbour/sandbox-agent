// Command loopback-to-vsock runs inside a microVM guest. It listens on a loopback TCP address,
// where agents find the model API (ANTHROPIC_BASE_URL=http://127.0.0.1:8080/anthropic), and
// relays every connection, byte for byte, to a vsock port on the host, where vsock-to-anthropic
// answers. It reads no HTTP and holds no credential.
//
//	loopback-to-vsock -listen 127.0.0.1:8080 -port 8080
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/matbour/sandbox-agent/internal/forward"
	"github.com/matbour/sandbox-agent/internal/vsock"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "loopback TCP address to listen on")
	cid := flag.Uint("cid", vsock.HostCID, "vsock context ID to connect to (2 is the host)")
	port := flag.Uint("port", 8080, "vsock port to connect to")

	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*listen, uint32(*cid), uint32(*port), log); err != nil {
		log.Error("loopback-to-vsock", "err", err)
		os.Exit(1)
	}
}

func run(listen string, cid, port uint32, log *slog.Logger) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dial := func() (forward.Conn, error) {
		return vsock.Dial(cid, port)
	}

	log.Info("relaying", "listen", ln.Addr().String(), "vsock_cid", cid, "vsock_port", port)

	return forward.Serve(ctx, ln, dial, log)
}
