package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var errNoContent = errors.New("no content")

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("%d: %s", e.code, e.msg) }

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	return &httpError{code: resp.StatusCode, msg: string(bytes.TrimSpace(b))}
}

func (a *Agent) request(ctx context.Context, method, path, lease string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.Manager+path, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+a.Token)

	if lease != "" {
		req.Header.Set(LeaseHeader, lease)
	}

	return req, nil
}

// call sends in as JSON and decodes the answer into out (if not nil).
func (a *Agent) call(ctx context.Context, method, path, lease string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := a.request(ctx, method, path, lease, bytes.NewReader(b))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		if out != nil {
			return errNoContent
		}

		return nil
	case resp.StatusCode/100 != 2:
		return statusError(resp)
	case out != nil:
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	}

	return nil
}

// stream sends body as it is, for tar streams and raw output. Uploads have no client timeout:
// a workspace can be large.
func (a *Agent) stream(ctx context.Context, method, path, lease string, body io.Reader) error {
	req, err := a.request(ctx, method, path, lease, body)
	if err != nil {
		return err
	}

	c := *a.HTTP
	c.Timeout = 0

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return statusError(resp)
	}

	return nil
}

func backoff(attempt int) time.Duration {
	return min(time.Duration(1<<attempt)*time.Second, 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}

	return b
}
