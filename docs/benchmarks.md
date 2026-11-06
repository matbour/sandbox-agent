# Benchmarks

There is no standard benchmark for sandbox overhead (standard agent benchmarks such as
Terminal-Bench 2.0 or SWE-bench measure the model and the agent, not the box), so the sandboxes are
compared on fixed workloads from the target repository. The first one is `bun install`.

## `bun install`, offline

`bench/install.sh` measures `bun install --frozen-lockfile --offline` on the target
repository in each sandbox.

- **Same input**: the repository's HEAD at a fixed commit (`git archive`, no `.git`) and a Bun cache
  filled once beforehand (`prepare`, the only step with network).
- **Offline**: a download would mostly measure the registry and the internet link, and the
  Firecracker guest had no network when this was written (it gets one in the development phase,
  D44). What remains is CPU and file system work: about 3000
  packages written into `node_modules`, hard-linked from the cache (Bun's default backend; cache and
  repository are on the same file system in every sandbox).
- **Same resources**: 2 CPUs, 4 GiB of memory.
- **Repeats**: one warm-up install, then 5 measured ones, each from no `node_modules`
  (`bench/install-guest.sh`, run inside the sandbox). The time excludes the clean-up and
  its `sync`.

```
scripts/build-rootfs.sh
bench/install.sh prepare <path to the target repository>
bench/install.sh podman
bench/install.sh firecracker
```

Results are appended to `build/bench/results.tsv` (date, sandbox, commit, run, milliseconds).

| Sandbox | How the workspace is reached | Median of 5 | Spread |
| --- | --- | --- | --- |
| Podman (runc, shared kernel) | bind mount of the host's ext4 | 1.60 s | 1.59 to 1.61 s |
| Firecracker v1.18.0 (CI kernel 6.1.155) | second virtio-blk disk, ext4 image, 8 GiB sparse | 2.51 s | 2.50 to 2.53 s |

Development machine (AMD Ryzen 7 7700X, host kernel 7.0), 2026-11-16, target repository at
`b77ae85`: 80 MB of sources, 3.9 GB of cache, 3022 packages.

Firecracker is about 57 % slower here. Likely causes, none measured yet: every file system operation
goes through the guest kernel and then Firecracker's emulated block device (one queue, served by a
single thread on the host), the data is cached twice (guest and host page caches), and the guest
kernel is older (6.1) than the host's (7.0).

## Checks after a change: lint, type check, tests

`bench/checks.sh` measures what an agent runs to check a change in the front-end app, on the same
input, offline: `biome check .` at the root (3053 files), `tsc --noEmit` in the app (TypeScript 7,
the native compiler, started with `bunx --bun`), and `bun test` in the app (2168 tests; 5 fail
without the app's environment variables, which does not change the time). Each check runs 3 times
after one offline install; build caches (`*.tsbuildinfo`: the app has `incremental: true`) are
removed before each run, so every run is cold, as in a fresh sandbox.

```
bench/checks.sh podman
bench/checks.sh firecracker
```

Results go to `build/bench/checks.tsv` (date, sandbox, commit, check, run, milliseconds, exit code).

| Check | Podman | Firecracker | Difference |
| --- | --- | --- | --- |
| `biome check .` | 5.15 to 5.27 s | 5.29 to 5.53 s | about +4 % |
| `tsc --noEmit` | 12.3 to 12.6 s | 12.5 to 16.5 s (first run slowest) | about +10 % |
| `bun test` | 9.0 to 9.5 s | 9.2 to 9.9 s | about +2 % |

2 CPUs, 8 GiB, no swap; same machine and commit as above, 2026-11-16. On CPU-bound work the VM
costs little; the file-heavy install is where it costs (+57 %).

Pitfalls met on the way, each of which gave wrong numbers first:

- **`tsc` on this app needs more than 4 GiB** (about 3.9 GB resident when it is killed). With
  4 GiB the guest's OOM killer stops it (exit code 137). Podman with `--memory 4096m` and no
  `--memory-swap` let the container swap on the host instead: `tsc` passed in 42 to 53 s, which
  looked like a slow but working run. The script now gives 8 GiB and sets `--memory-swap` equal to
  `--memory`.
- **Incremental build info left in the shared input** made later runs warm (2 s instead of 12 s).
  The guest loop now removes `*.tsbuildinfo` before each run and at the end.
- `bunx tsc` without `--bun` needs `node`, which the rootfs does not have. Bun normally provides a
  `node` shim in `/tmp`; it worked as root in Podman and failed as the `agent` user in the VM (to
  verify why).

## Firecracker microVM start (2026-11-20)

`TestStartBench` (`SANDBOX_BENCH=1 go test ./internal/sandbox/firecracker/ -run StartBench -v`)
times `Provider.Start` from nothing to a guest agent that answers over vsock: the workspace disk
(a few files), the firecracker process up to its API socket, the API calls, then the guest (kernel
boot, the shell init, the guest agent). 1 vCPU, 1 GiB, no network, Firecracker v1.18.0 and its CI
kernel 6.1.155, on the development machine (8-core x86-64, 16 threads). Rootless, no jailer, no
snapshot: a cold boot each time. The tools disk is cached after the first start.

| Phase | Before | After | |
| --- | --- | --- | --- |
| Workspace disk | 11 ms | 10 ms | `mkfs.ext4 -d` from a tar |
| Process to API socket | 50 ms | 5 ms | the poll interval was the measure |
| API calls | 13 ms | 11 ms | 7 PUTs |
| Guest ready | 643 ms | 132 ms | kernel, init, guest agent |
| **Total, p50 of 20** | **719 ms** | **160 ms** | p90 767 → 175 ms |
| 4 at once, all ready | 799 ms | 213 ms | |
| 8 at once, all ready | 855 ms | 216 ms | |

"Before" is with the kernel probing a PCI bus and an i8042 keyboard controller, which a
Firecracker guest does not have (its devices are virtio-mmio), and 50 ms polls; "after" adds
`pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd` to the command line and polls every
5 ms. Starts at once barely add to each other on this machine.

Idle memory: once the guest agent is up, the firecracker process has an RSS of 68 MiB and a PSS of
66 to 68 MiB, for a guest configured with 1 GiB: the guest only touches what it uses. So about 15
idle guests per GiB of host memory, before page sharing or ballooning.

Through the whole chain (`TestFirecrackerFleet`: manager API, two runners, the worker with the
fake backend, the workspace both ways), a run whose worker sleeps 2 s takes 2.27 s: about 270 ms
of overhead per run, the start included.

To do: the same on the board (arm64, cores 4-7), our own kernel, and snapshot and restore
(roadmap R8).

## To do

- gVisor and Docker Sandboxes on the same input.
- The same on the board (arm64), once Firecracker runs there.
- Firecracker with our own kernel, and with the workspace on virtio-fs or a tmpfs instead of a disk.
- Boot time and memory use next to the install time.
- Peak memory of each check, so that sandbox sizes are chosen from data.
