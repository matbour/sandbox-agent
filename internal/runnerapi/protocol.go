// Package runnerapi is the protocol between the manager (agentd) and runners (runnerd) on other
// machines (D42, docs/decision/runners.md). Runners dial out over HTTPS with a token of their
// own, lease runs, download the workspace, forward the worker's output and upload the changed
// workspace. The manager never trusts a runner more than the worker it ran: the output is read
// with task.Read on the manager's side, and the workspace goes through guestexec.Mirror and the
// usual validation.
//
// Calls, all POST with a JSON body unless noted, authenticated with "Authorization: Bearer":
//
//	/v1/register                         capabilities and protocol version
//	/v1/lease                            long poll: a run that fits the free capacity, or 204
//	/v1/heartbeat                        extends the leases held; answers the cancelled ones
//	GET /v1/runs/{id}/input              the workspace as a tar stream
//	/v1/runs/{id}/output?offset=N        raw worker stdout from byte N (idempotent on retry)
//	PUT /v1/runs/{id}/workspace          the changed workspace as a tar stream
//	/v1/runs/{id}/end                    how the run ended on the runner's side
//
// Run calls carry the lease in the X-Lease header; a call for a lease that expired or belongs to
// another runner is refused.
package runnerapi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/matbour/sandbox-agent/internal/runner"
)

// Protocol is the version both sides must speak.
const Protocol = 1

// LeaseHeader carries the lease ID on run calls.
const LeaseHeader = "X-Lease"

// Capacity is what a runner offers, or has free.
type Capacity struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memory_mib"`
}

// Fits reports whether a run's resources fit in c.
func (c Capacity) Fits(spec runner.Spec) bool {
	mem, err := MemoryMiB(spec.Memory)

	return err == nil && max(spec.CPUs, 1) <= c.CPUs && mem <= c.MemoryMiB
}

// Register is a runner introducing itself.
type Register struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Protocol  int      `json:"protocol"`
	Arch      string   `json:"arch"`
	Providers []string `json:"providers"`
	Capacity  Capacity `json:"capacity"`
}

// LeaseRequest asks for a run that fits Free.
type LeaseRequest struct {
	Free Capacity `json:"free"`
}

// Lease is a run given to one runner until Deadline, extended by heartbeats.
type Lease struct {
	ID         string      `json:"id"`
	Spec       runner.Spec `json:"spec"`
	TTLSeconds int         `json:"ttl_s"`
	MaxUpload  int64       `json:"max_upload"` // bytes of workspace the manager accepts back
	MaxOutput  int64       `json:"max_output"` // bytes of worker output
}

// Heartbeat lists the leases a runner holds.
type Heartbeat struct {
	Leases []string `json:"leases"`
}

// HeartbeatReply lists the runs to stop: cancelled, expired or unknown.
type HeartbeatReply struct {
	Cancel []string `json:"cancel"`
}

// End is the runner's account of how the run ended. The terminal event is not in it: the
// manager reads it from the worker's output itself.
type End struct {
	Error string `json:"error,omitempty"` // the runner could not run it (sandbox, network)
	Log   string `json:"log,omitempty"`   // the worker's stderr, capped
}

// MemoryMiB reads "2g", "512m" or a number of MiB; empty means 1 GiB, as the providers do.
func MemoryMiB(m string) (int, error) {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		return 1024, nil
	}

	mult := 1

	switch {
	case strings.HasSuffix(m, "g"):
		mult, m = 1024, strings.TrimSuffix(m, "g")
	case strings.HasSuffix(m, "m"):
		m = strings.TrimSuffix(m, "m")
	}

	n, err := strconv.Atoi(m)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory %q", m)
	}

	return n * mult, nil
}
