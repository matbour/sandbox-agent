package guestexec

import (
	"context"
	"io"
)

// Call sends req over conn, routes the answer's frames to sinks and returns the guest's Result.
// Cancelling ctx closes conn, which tells the guest to kill what it started. Call closes conn
// when it returns.
func Call(ctx context.Context, conn io.ReadWriteCloser, req Request, sinks map[byte]io.Writer) (Result, error) {
	return CallStdin(ctx, conn, req, nil, sinks)
}

// CallStdin is Call with input for the command: stdin is sent as frames while the answer is
// read, then closed. req.Stdin is set when stdin is not nil.
func CallStdin(ctx context.Context, conn io.ReadWriteCloser, req Request, stdin io.Reader, sinks map[byte]io.Writer) (Result, error) {
	req.Stdin = stdin != nil

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })

	defer func() {
		stop()

		_ = conn.Close()
	}()

	if err := WriteRequest(conn, req); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}

		return Result{}, err
	}

	if stdin != nil {
		// The only writer after the request: no lock needed. A write error means the stream is
		// gone, which ReadFrames reports.
		go func() {
			buf := make([]byte, 64<<10)

			for {
				n, rerr := stdin.Read(buf)
				if n > 0 && WriteFrame(conn, StdinData, buf[:n]) != nil {
					return
				}

				if rerr != nil {
					_ = WriteFrame(conn, StdinClose, nil)

					return
				}
			}
		}()
	}

	res, err := ReadFrames(conn, sinks)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}

	return res, err
}
