// Package forward relays TCP connections to another stream, byte for byte, with half-close in each
// direction. cmd/loopback-to-vsock uses it to carry an agent's loopback connections to the host
// over vsock (docs/architecture.md). It does not read HTTP and holds no credential.
package forward

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
)

// Conn is a stream that can close its sending side, so that end of file propagates in each
// direction on its own. *net.TCPConn, *net.UnixConn and *vsock.Conn all qualify.
type Conn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// Dialer opens a connection to the target.
type Dialer func() (Conn, error)

// Serve accepts connections on ln and relays each one to a new connection from dial, until ctx
// is done.
func Serve(ctx context.Context, ln net.Listener, dial Dialer, log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		src, ok := c.(Conn)
		if !ok {
			_ = c.Close()

			return errors.New("forward: listener connections cannot half-close")
		}

		wg.Go(func() {
			if err := relay(src, dial); err != nil {
				log.Warn("relay", "err", err)
			}
		})
	}
}

func relay(src Conn, dial Dialer) error {
	defer func() { _ = src.Close() }()

	dst, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()

	errs := make(chan error, 2)

	pipe := func(to, from Conn) {
		_, err := io.Copy(to, from)
		_ = to.CloseWrite()
		errs <- err
	}

	go pipe(dst, src)
	go pipe(src, dst)

	return errors.Join(<-errs, <-errs)
}
