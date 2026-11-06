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

Firecracker work starts on the x86-64 development machine (D39), where a test guest already
boots; the steps below are for the board, once it is reachable.

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
  one for control (start the agent, stream logs, fetch the output). Superseded for the development
  phase by D44: the guest gets a tap on a bridge with NAT (see [Network](#network-development-phase-d44)).
  The vsock work below stays valid for the next version.
- Rootfs: the layer 1 image converted to ext4, read-only, plus a second writable disk for the
  workspace (or virtio-fs later).
- Run the Firecracker process itself inside a layer 1 sandbox (no network, minimal mounts, seccomp,
  cgroup), like Firecracker's `jailer` does. The `jailer` needs root; our runtime can play its role.
- Snapshot and restore for fast starts is a stretch goal.

### Network (development phase, D44)

Laid out like Docker's default bridge, with taps instead of veths since Firecracker only takes taps.
`deploy/network/fc-net.sh`, run as root once per machine by the `sandbox-agent-net` system unit:

- bridge `fc0` with the gateway `10.230.0.1/24` (outside Docker's default address pools);
- persistent taps `fc-tap0` to `fc-tapN`, owned by the account that runs Firecracker, attached as
  isolated bridge ports (guests cannot reach each other);
- IP forwarding and a masquerade rule for the subnet; the accept rules go into `DOCKER-USER` when
  Docker is installed, because Docker sets the `FORWARD` policy to `DROP`.

Guest N uses `fc-tapN` and `10.230.0.(10+N)`, set with `ip=` on the kernel command line, and a
public resolver in `/etc/resolv.conf`. No filtering: the guest reaches the LAN and the host.
Installed on the development machine on 2026-11-20.

Verified on 2026-11-20 (development machine, `firecracker/net-check.sh`, with Docker installed on
the host): Firecracker's CI kernel 6.1.155 applies `ip=` (it has `CONFIG_IP_PNP`); the guest
pings the gateway, resolves `api.anthropic.com` through 1.1.1.1 and gets an HTTPS answer from it,
on `fc-tap0` and `fc-tap1`. So the `DOCKER-USER` and NAT rules let the traffic through. The
agent rootfs has no `/etc/resolv.conf`; `lib.sh` copies `/etc` to a tmpfs and adds one until the
image ships it. `FC_NET=<N>` in `lib.sh` turns the network device on; without it, guests have
none, as before.

### The provider in Go (R1, D46)

`internal/sandbox/firecracker` starts one microVM per sandbox through Firecracker's API socket
(machine config, boot source, three drives, the tap, vsock, start), with
`taskset -c <CPUs>` when a CPU set is configured. Disks: the agent rootfs read-only (vda), the
workspace (vdb, an ext4 image made by `mkfs.ext4 -d` from a tar of the host directory), and the
guest agent (vdc, cached per binary hash). `cmd/guest-agent`, PID 1 in the guest, answers on
vsock port 1024. A failed boot leaves `<state>/vms/<name>.failed.log` with the console. It runs rootless, without
the jailer (D47).

Verified on 2026-11-20 on the development machine: the conformance suite passes, and a command in
the guest runs as root on request, has the VM's hostname, finds `git`, `bun` and `claude`, and
reaches `api.anthropic.com` over HTTPS. The guest agent answers 0.8 s after `Start` (kernel boot
included; the workspace was a few files), then 160 ms once the kernel stopped probing for PCI and
an i8042 controller ([benchmarks.md](benchmarks.md#firecracker-microvm-start-2026-11-20)).

First real agent run (2026-11-20, development machine): `agentd -try` with
`SANDBOX_PROVIDER=firecracker` on a small Bun repository, Claude Code with Haiku on the owner's
subscription token (D43), 2 vCPUs, 2 GiB. The agent added a function and its test and ran
`bun test` in the guest; the patch held exactly those two files. 10.9 s wall clock in all
(clone, workspace disk, boot, 8.7 s of agent over 7 turns, sync back, diff and validation). No VM
or tap lease left behind.

Found on the way:

- A token pasted from `claude setup-token` can keep the terminal's line wrap: the API then
  answers "401 OAuth access token is invalid". The file must hold one line, `sk-ant-oat01-`
  followed by letters, digits, `-` and `_` (108 characters here).
- busybox `mount` without `-t` detects the type through `/proc`, which the init has not mounted
  yet: it fails with "No such file or directory" although the device exists.
- `mkfs.ext4 -d` reads a tar (e2fsprogs 1.47.1 or later, here 1.47.2 on the host) and keeps the
  owners written in the tar, not the host's: that is how the workspace belongs to the guest's
  agent user on any host.
- `mount` of the workspace and the agent disk name `ext4` explicitly for the same reason.

### vsock, as Firecracker does it

vsock is a socket family (`AF_VSOCK`) for a VM and its host, with no network stack: an address is a
context ID (CID; the host is always 2) and a port. The guest uses it like TCP; its kernel carries
the bytes over a `virtio-vsock` device to the VMM. A guest that has only vsock can reach exactly the
ports the host listens on, and nothing else.

Firecracker does not use the host kernel's `vhost-vsock`. It maps vsock to Unix sockets on the
host, from the device's `uds_path` in the VM config:

- Guest to host: a guest connection to (2, port) becomes a connection to the Unix socket
  `<uds_path>_<port>`, which a host program must listen on.
- Host to guest: the host connects to `<uds_path>`, writes `CONNECT <port>\n`, reads `OK ...`, and
  the stream then reaches the guest's listener on that port.

So the host side needs nothing vsock-specific, and file permissions on the socket decide who may
use it. Unix socket paths are limited to 108 bytes, which rules out deep directories for
`uds_path`.

### Egress over vsock (verified 2026-11-16, development machine)

`firecracker/vsock-check.sh` boots the agent rootfs with no network device and a vsock device. In the
guest, `loopback-to-vsock` listens on `127.0.0.1:8080` (after `ip link set lo up`) and relays each
connection to vsock port 8080 on the host; there, `vsock-to-anthropic` listens on `<uds_path>_8080`,
adds the key and forwards to `api.anthropic.com`. With a placeholder key the API answers 401
"invalid x-api-key" (the path works end to end); a path outside the allowlist gets 403 from the
proxy. Firecracker's CI kernel 6.1.155 has `virtio_vsock` built in.

### Claude Code in the VM (first run, 2026-11-16, development machine)

`firecracker/run-agent.sh` ran Claude Code (Sonnet) on a small refactor-and-test task in the target
repository, inside a VM with 2 vCPUs, 4 GiB, no network device, egress over vsock: 299 s, $0.23, 11
turns, 10 API calls (all `POST /v1/messages`, none rejected), a correct-looking two-file diff. The
API took 31 s of the 299. Most of the rest was `tsc` thrashing at the 4 GiB limit until the
guest's OOM killer stopped it, twice. The shell printed nothing, so the agent read an empty output
and reported the type check as clean: the change was never type-checked. Lessons:

- Size the VM for the repository's heaviest check (`tsc` on the front-end app needs more than
  4 GiB, [benchmarks.md](benchmarks.md)); `run-agent.sh` now gives 8 GiB.
- A killed command must be visible to the agent. Without swap, an OOM kill is silent in a
  non-interactive shell; the guest console shows it ("Out of memory: Killed process"). The worker
  should report OOM kills from the guest as events.
- The rootfs has no `python3` and no `node`; the agent tried both, lost a turn, and fell back to
  Bun. Either add them or say so in the prompt.

### Starts from a snapshot (D49, 2026-11-22)

The provider builds, once per machine size, a **template**: a guest booted with no job and
snapshotted while its guest agent waits, the workspace disk attached but not mounted. Each start
then loads it in a new firecracker process and sends `OpConfigure`: the clock, a reseed of the
random pool, the hostname, and the job's workspace, mounted after dropping the block cache. Every
file of a VM has a fixed name in its run directory, where firecracker runs, so the snapshot's
relative paths open the right disks. 50 ms per start instead of 159 ms, and restored guests share
the template's memory pages ([benchmarks.md](benchmarks.md)). Not yet for VMs with a tap.

Found on the way:

- `timeout` without `--foreground` puts firecracker in a background process group: when it sets
  up the terminal as the guest's serial console it gets `SIGTTOU` and stops before creating its
  vCPU threads (state `T`). In a terminal, the serial console also takes Ctrl-C: stop the VM with
  `kill`. The provider is not affected: it sends the console to a file.
- Firecracker v1.18's `/snapshot/load` has `vsock_override`, `network_overrides` (the tap behind
  an interface) and `clock_realtime` (x86 only), and maps the memory file private or serves it
  through userfaultfd (`Uffd`).

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
