package runnerapi

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// uploader sends the worker's raw output to the manager in order, by byte offset, in batches.
// A failed post is retried with the same offset; the manager skips what it already has.
type uploader struct {
	a   *Agent
	l   *Lease
	log *slog.Logger
	max int64

	mu      sync.Mutex
	buf     []byte
	sent    int64
	closing bool
	err     error
	wake    chan struct{}
	done    chan struct{}
}

func (u *uploader) start(ctx context.Context) {
	u.wake, u.done = make(chan struct{}, 1), make(chan struct{})

	go u.loop(ctx)
}

func (u *uploader) Write(p []byte) (int, error) {
	u.mu.Lock()
	if u.max > 0 && u.sent+int64(len(u.buf))+int64(len(p)) > u.max {
		u.mu.Unlock()

		return len(p), nil // over the manager's limit: it fails the run anyway
	}

	u.buf = append(u.buf, p...)
	u.mu.Unlock()

	select {
	case u.wake <- struct{}{}:
	default:
	}

	return len(p), nil
}

func (u *uploader) loop(ctx context.Context) {
	defer close(u.done)

	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-u.wake:
		case <-t.C:
		case <-ctx.Done():
			return
		}

		u.mu.Lock()
		batch := u.buf[:min(len(u.buf), guestexec.MaxFrame)]
		closing := u.closing
		u.mu.Unlock()

		if len(batch) > 0 {
			if err := u.post(ctx, batch); err != nil {
				u.mu.Lock()
				u.err = err
				u.mu.Unlock()

				return
			}

			u.mu.Lock()
			u.buf = u.buf[len(batch):]
			u.sent += int64(len(batch))
			more := len(u.buf) > 0
			u.mu.Unlock()

			if more {
				select {
				case u.wake <- struct{}{}:
				default:
				}
			}

			continue
		}

		if closing {
			return
		}
	}
}

func (u *uploader) post(ctx context.Context, batch []byte) error {
	path := fmt.Sprintf("/v1/runs/%s/output?offset=%d", u.l.Spec.ID, u.sent)

	for attempt := 0; ; attempt++ {
		err := u.a.stream(ctx, http.MethodPost, path, u.l.ID, bytes.NewReader(batch))
		if err == nil || attempt == 4 {
			return err
		}

		u.log.Warn("output to the manager, retrying", "err", err)

		if !sleep(ctx, backoff(attempt)) {
			return ctx.Err()
		}
	}
}

// close flushes what is left and stops.
func (u *uploader) close() error {
	u.mu.Lock()
	u.closing = true
	u.mu.Unlock()

	select {
	case u.wake <- struct{}{}:
	default:
	}

	<-u.done

	u.mu.Lock()
	defer u.mu.Unlock()

	return u.err
}
