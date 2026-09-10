# Sandbox, layer 2: microVM or gVisor

Layer 1 shares the host kernel: one kernel bug reachable through an allowed syscall breaks it. Layer 2
puts a second kernel between the agent and the host.

## Option A: Firecracker (preferred)

Firecracker is a minimal virtual machine monitor on KVM: few devices (virtio-net, virtio-block,
vsock, serial), fast boot, small attack surface. It supports aarch64.

### Why it may not just work on this board

- The RK3588 is big.LITTLE (A55 and A76). A vCPU thread that migrates between core types sees
  different CPU identification registers, caches and PMU. KVM on heterogeneous arm64 systems can fail
  at vCPU init or behave inconsistently. Mitigation: pin the VMM to cores 4-7 (`taskset`, or the
  job's cgroup `cpuset.cpus`) **before** it creates vCPUs.
- Firecracker's aarch64 support targets server CPUs (Graviton). Interrupt controller (GICv3 on the
  RK3588, to confirm) and timer support need a real test.

### Spike

Goal: one microVM boots to a shell on the board.

1. Download the latest arm64 Firecracker release (static binary) and its guest kernel and rootfs
   from Firecracker's CI artifacts (see the project's getting-started guide).
2. Check access: the development account is in `kvm`, so `/dev/kvm` opens without root.
3. Start with `taskset -c 4-7 firecracker --api-sock /tmp/fc.sock`, configure boot source, rootfs,
   1 vCPU and 256 MB through the API, start.
4. Success: guest console prompt, `uname -a` in the guest. Then try 2 vCPUs.
5. Failure: record the exact error, retry without pinning to confirm the cause, then fall back to
   option B.

### Design once it works

- **No network device.** The guest talks to the host over vsock only: one port for the egress proxy,
  one for control (start the agent, stream logs, fetch the output).
- Rootfs: the layer 1 image converted to ext4, read-only, plus a second writable disk for the
  workspace (or virtio-fs later).
- Run the Firecracker process itself inside a layer 1 sandbox (no network, minimal mounts, seccomp,
  cgroup), like Firecracker's `jailer` does. The `jailer` needs root; our runtime can play its role.
- Snapshot and restore for fast starts is a stretch goal.

## Option A2: Cloud Hypervisor

Cloud Hypervisor is a Rust VMM on KVM from the same rust-vmm family as Firecracker, with a wider
device model: PCI, so device passthrough (GPUs), hotplug, virtio-fs, vsock. It supports aarch64 and
has snapshot and restore built in. It is driven through an HTTP API on a Unix socket (and
`ch-remote`), like Firecracker.

Why it is worth trying next to Firecracker: a VMM that can pass a GPU through is the one a
serverless platform for AI workloads ends up on, and the same big.LITTLE pinning question applies.
Known pitfall from published experience: virtio-fs devices do not always survive a restore, so keep
the workspace on a block device. Firmware or direct kernel boot on arm64, and the snapshot state on
arm64: to verify in the spike.

## Option B: gVisor

gVisor (`runsc`) implements the Linux syscall interface in a user-space kernel (the Sentry), written in
Go. The agent's syscalls hit the Sentry, which uses a small set of host syscalls.

- Supports arm64. Platforms: `systrap` (default, no KVM needed) and `kvm`.
- `runsc do <cmd>` runs a command in a sandbox without an OCI bundle: good for a first test.
- Networking can be disabled (`--network=none`); the proxy socket can be passed in.
- Trade-off: slower for syscall-heavy work (builds, `git`, `npm install`), and some syscalls are
  missing. Measure on a real repository.

## Docker Sandboxes, the provider in use

Docker Sandboxes (`sbx`) started as a point of comparison and became the first provider (D11): a
KVM microVM per job, run without Docker Engine or the `docker` group. Measurements, the network
policy and the lessons learnt are in [docker-sandboxes.md](docker-sandboxes.md). It passes the
conformance suite in 12.5 s, with 7 s to start a sandbox (D30).

## Comparison to show in the write-up

| | Layer 1 only | Layer 1 + gVisor | Layer 1 + Firecracker | Docker Sandboxes |
| --- | --- | --- | --- | --- |
| Kernel shared with host | Yes | No (Sentry), host syscalls filtered | No (guest kernel), KVM boundary | No (guest kernel 7.0.14 in a microVM) |
| Start time | to measure | to measure | to measure | 8.2 s to create with the image cached, 7 s in the conformance suite |
| Install time on the target monorepo | to measure | to measure | to measure | 123 s for `bun install` (4 CPUs, 4 GiB) |
| Memory overhead | to measure | to measure | to measure | about 1.4 GB RSS for a 2 GiB sandbox; page cache dropped (D29) |
| Escape tests passed | to measure | to measure | to measure | conformance suite passes; escape tests to script (S1) |
