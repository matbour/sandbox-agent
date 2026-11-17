package firecracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// dial opens a stream to the guest agent. Firecracker maps guest vsock ports to the device's Unix
// socket: the host writes "CONNECT <port>" and reads "OK <port>" before the stream starts.
func (s *Sandbox) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer

	c, err := d.DialContext(ctx, "unix", s.vsock)
	if err != nil {
		return nil, err
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}

	if _, err := fmt.Fprintf(c, "CONNECT %d\n", guestexec.Port); err != nil {
		_ = c.Close()

		return nil, err
	}

	// Read the answer one byte at a time: nothing past the newline may be consumed.
	line := make([]byte, 0, 32)
	b := make([]byte, 1)

	for len(line) < 64 {
		if _, err := c.Read(b); err != nil {
			_ = c.Close()

			return nil, fmt.Errorf("vsock handshake: %w", err)
		}

		if b[0] == '\n' {
			break
		}

		line = append(line, b[0])
	}

	if !bytes.HasPrefix(line, []byte("OK ")) {
		_ = c.Close()

		return nil, fmt.Errorf("vsock handshake: %q", line)
	}

	_ = c.SetDeadline(time.Time{})

	return c, nil
}

// call runs one request and fails if the guest reports an error.
func (s *Sandbox) call(ctx context.Context, req guestexec.Request, sinks map[byte]io.Writer) error {
	res, err := s.request(ctx, req, sinks)
	if err != nil {
		return err
	}

	if res.Error != "" {
		return errors.New(res.Error)
	}

	return nil
}

func (s *Sandbox) request(ctx context.Context, req guestexec.Request, sinks map[byte]io.Writer) (guestexec.Result, error) {
	return s.requestStdin(ctx, req, nil, sinks)
}

func (s *Sandbox) requestStdin(ctx context.Context, req guestexec.Request, stdin io.Reader, sinks map[byte]io.Writer) (guestexec.Result, error) {
	select {
	case <-s.exited:
		return guestexec.Result{}, errors.New("the VM is not running")
	default:
	}

	c, err := s.dial(ctx)
	if err != nil {
		return guestexec.Result{}, err
	}

	return guestexec.CallStdin(ctx, c, req, stdin, sinks)
}
