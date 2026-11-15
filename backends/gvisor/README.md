# gVisor

One rootless Podman container per sandbox with `runsc` as its runtime: the guest's system calls go
to gVisor's user-space kernel, not to the host's. The workspace is a bind mount (`--userns
keep-id`, so the agent owns its files), the network goes through pasta with gVisor's own network
stack, and a systemd user scope bounds CPU, memory and PIDs (D51). No KVM needed, which makes it
the backend for a machine without nested virtualisation, such as most VPS.

- Go package: this directory. Integration test: `gvisor_it_test.go` (`SANDBOX_IT=1`).
- Needs Podman and `runsc` on PATH.
