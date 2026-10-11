package guestexec

import (
	"context"
	"io"
)

// Call sends req over conn, routes the answer's frames to sinks and returns the guest's Result.
// Cancelling ctx closes conn, which tells the guest to kill what it started. Call closes conn
// when it returns.
func Call(ctx context.Context, conn io.ReadWriteCloser, req Request, sinks map[byte]io.Writer) (Result, error) {
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

	res, err := ReadFrames(conn, sinks)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}

	return res, err
}
