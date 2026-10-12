package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// api is a client for the parts of Firecracker's HTTP API we use, over its Unix socket. The API
// is small and stable, so it is written by hand rather than taken from firecracker-go-sdk.
// Reference: https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml
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

type bootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type machineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMiB int `json:"mem_size_mib"`
}

type networkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac"`
}

type vsockDevice struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type action struct {
	ActionType string `json:"action_type"`
}

// put sends a PUT; Firecracker answers 204 on success and a JSON fault_message otherwise.
func (a *api) put(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.c.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", path, err)
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

	return fmt.Errorf("PUT %s: %s: %s", path, resp.Status, msg)
}

// step is one API call of the boot sequence.
type step struct {
	path string
	body any
}

// configure sets up the whole VM, then starts it.
func (a *api) configure(ctx context.Context, vm vmConfig) error {
	steps := []step{
		{"/machine-config", machineConfig{VCPUCount: vm.vcpus, MemSizeMiB: vm.memMiB}},
		{"/boot-source", bootSource{KernelImagePath: vm.kernel, BootArgs: vm.bootArgs}},
	}

	// Drives get /dev/vda, vdb, … in the order they are added; the root device is always vda.
	for _, d := range vm.drives {
		steps = append(steps, step{"/drives/" + d.DriveID, d})
	}

	if vm.tap != "" {
		steps = append(steps, step{"/network-interfaces/eth0",
			networkInterface{IfaceID: "eth0", HostDevName: vm.tap, GuestMAC: vm.mac}})
	}

	steps = append(steps,
		step{"/vsock", vsockDevice{GuestCID: 3, UDSPath: vm.vsock}},
		step{"/actions", action{ActionType: "InstanceStart"}},
	)

	for _, s := range steps {
		if err := a.put(ctx, s.path, s.body); err != nil {
			return err
		}
	}

	return nil
}
