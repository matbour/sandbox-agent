package sbx

import (
	"context"
	"fmt"
	"time"

	"github.com/matbour/sandbox-agent/internal/sandbox"
)

// TrimCache starts a loop, as root in the VM, that drops the guest's page cache every interval.
// A VM does not give its file cache back to the host, so a job that reads or writes gigabytes
// (node_modules) would otherwise keep its whole memory cap; dropped pages are returned to the host
// through free page reporting within a second. Measured: 2.3 GB down to 0.8 GB after 1 GB of I/O.
func (s *Sandbox) TrimCache(ctx context.Context, interval time.Duration) error {
	loop := fmt.Sprintf("while :; do sleep %d; sync; echo 1 > /proc/sys/vm/drop_caches; done", int(interval.Seconds()))

	_, se, err := s.Exec(ctx, sandbox.ExecOptions{User: "root"}, "sh", "-c", "nohup sh -c '"+loop+"' </dev/null >/dev/null 2>&1 &")
	if err != nil {
		return fmt.Errorf("start cache trimmer: %w: %s", err, tail(se))
	}

	return nil
}

// DropCache drops the guest's page cache once (after an install, for example).
func (s *Sandbox) DropCache(ctx context.Context) {
	_, _, _ = s.Exec(ctx, sandbox.ExecOptions{User: "root"}, "sh", "-c", "sync; echo 1 > /proc/sys/vm/drop_caches")
}
