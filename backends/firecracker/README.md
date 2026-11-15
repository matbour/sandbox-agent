# Firecracker

One Firecracker microVM per sandbox, driven through its API socket from Go: the agent rootfs as a
read-only disk, the workspace as a second disk, a vsock to the guest agent (`cmd/guest-agent`, its
PID 1), and a tap on the host bridge when the network is on. The first sandbox of a given shape
boots cold and is snapshotted into a template; the next ones are restored from it, then
reconfigured (hostname, clock, entropy, network, workspace) before they run anything (D49).

- Go package: this directory. Integration tests: `*_it_test.go` (`SANDBOX_IT=1`), benchmark:
  `bench_it_test.go` (`SANDBOX_BENCH=1`).
- `scripts/`: the shell experiments that came before the provider. `lib.sh` (kernel, disks, VM
  config, run), `net-check.sh`, `vsock-check.sh`, `run-agent.sh`, and `guest/` for their guest side.
- Host setup: the bridge and NAT in `deploy/network/` (D44); the kernel and rootfs in `build/<arch>/`
  (`scripts/build-rootfs.sh`, `lib.sh`).
- Design and measurements: [docs/microvm.md](../../docs/microvm.md),
  [docs/benchmarks.md](../../docs/benchmarks.md).
