package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/matbour/sandbox-agent/backends/firecracker/fcapi"
)

// api is a client for the parts of Firecracker's HTTP API we use, over its Unix socket. The
// request bodies are the types generated from Firecracker's own OpenAPI specification (fcapi,
// D54); the transport is a few lines of net/http.
type api struct {
	c *http.Client
}

func newAPI(sock string) *api {
	return &api{c: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer

				return d.DialContext(ctx, "unix", sock)
			},
		},
	}}
}

// put sends a PUT; Firecracker answers 204 on success and a JSON fault_message otherwise.
func (a *api) put(ctx context.Context, path string, body any) error {
	return a.send(ctx, http.MethodPut, path, body)
}

// patch sends a PATCH: changes to a VM that already runs, or was loaded from a snapshot.
func (a *api) patch(ctx context.Context, path string, body any) error {
	return a.send(ctx, http.MethodPatch, path, body)
}

func (a *api) send(ctx context.Context, method, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.c.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 == 2 {
		return nil
	}

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var fault struct {
		FaultMessage string `json:"fault_message"`
	}
	if json.Unmarshal(msg, &fault) == nil && fault.FaultMessage != "" {
		msg = []byte(fault.FaultMessage)
	}

	return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, msg)
}

// step is one API call of the boot sequence.
type step struct {
	path string
	body any
}

// configure sets up the whole VM, then starts it.
func (a *api) configure(ctx context.Context, vm vmConfig) error {
	steps := []step{
		{"/machine-config", fcapi.MachineConfiguration{VCPUCount: int64(vm.vcpus), MemSizeMiB: int64(vm.memMiB)}},
		{"/boot-source", fcapi.BootSource{KernelImagePath: vm.kernel, BootArgs: fcapi.Ptr(vm.bootArgs)}},
	}

	// Drives get /dev/vda, vdb, … in the order they are added; the root device is always vda.
	for _, d := range vm.drives {
		steps = append(steps, step{"/drives/" + d.DriveID, d})
	}

	if vm.tap != "" {
		steps = append(steps, step{"/network-interfaces/eth0",
			fcapi.NetworkInterface{IfaceID: "eth0", HostDevName: vm.tap, GuestMAC: fcapi.Ptr(vm.mac)}})
	}

	steps = append(steps,
		step{"/vsock", fcapi.Vsock{GuestCID: 3, UDSPath: vm.vsock}},
		step{"/actions", fcapi.InstanceActionInfo{ActionType: fcapi.InstanceActionInfoActionTypeInstanceStart}},
	)

	for _, s := range steps {
		if err := a.put(ctx, s.path, s.body); err != nil {
			return err
		}
	}

	return nil
}

// Snapshots (D49). Paths in the requests may be relative: firecracker resolves them against its
// working directory, which is the VM's run directory.

// snapshot pauses the VM and writes a full snapshot: the device and CPU state, and the guest's
// memory.
func (a *api) snapshot(ctx context.Context, state, mem string) error {
	if err := a.patch(ctx, "/vm", fcapi.Vm{State: fcapi.VmStatePaused}); err != nil {
		return err
	}

	return a.put(ctx, "/snapshot/create", fcapi.SnapshotCreateParams{
		SnapshotType: fcapi.Ptr(fcapi.SnapshotCreateParamsSnapshotTypeFull), SnapshotPath: state, MemFilePath: mem})
}

// restore loads a snapshot into a firecracker process that has no VM yet, and leaves it paused.
// The memory file is mapped private: every VM restored from it shares its pages until it writes
// them. tap, when set, replaces the template's tap behind eth0.
func (a *api) restore(ctx context.Context, state, mem, tap string) error {
	l := fcapi.SnapshotLoadParams{
		SnapshotPath:  state,
		MemBackend:    &fcapi.MemoryBackend{BackendType: fcapi.MemoryBackendBackendTypeFile, BackendPath: mem},
		VsockOverride: &fcapi.VsockOverride{UDSPath: vsockName},
		ResumeVM:      fcapi.Ptr(false),
	}

	// KVM_CLOCK_REALTIME exists on x86 only; elsewhere the guest agent sets the clock.
	if runtime.GOARCH == "amd64" {
		l.ClockRealtime = fcapi.Ptr(true)
	}

	if tap != "" {
		l.NetworkOverrides = []fcapi.NetworkOverride{{IfaceID: "eth0", HostDevName: tap}}
	}

	if err := a.put(ctx, "/snapshot/load", l); err != nil {
		return err
	}

	// The workspace disk is this VM's file under the template's name: patching the drive makes
	// the guest read its size again.
	return a.patch(ctx, "/drives/workspace", fcapi.PartialDrive{DriveID: "workspace", PathOnHost: fcapi.Ptr(workspaceName)})
}

// resume starts the vCPUs of a paused VM.
func (a *api) resume(ctx context.Context) error {
	return a.patch(ctx, "/vm", fcapi.Vm{State: fcapi.VmStateResumed})
}
