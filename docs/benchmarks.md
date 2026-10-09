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
  Firecracker guest has no network by design. What remains is CPU and file system work: about 3000
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

## To do

- gVisor and Docker Sandboxes on the same input.
- The same on the board (arm64), once Firecracker runs there.
- Firecracker with our own kernel, and with the workspace on virtio-fs or a tmpfs instead of a disk.
- Boot time and memory use next to the install time.
