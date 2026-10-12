# Agent root filesystem

The programs the agent runs (Git, Node.js, Bun, Python, Claude Code) are described by a Dockerfile,
built rootless with Podman, and turned into an ext4 disk image that a microVM boots from. No step
needs root or Docker Engine.

```
images/agent/Dockerfile
        │  podman build          (rootless: user namespaces, /etc/subuid)
        ▼
container image
        │  podman export         (one tar; files keep their owner, root, in the tar headers)
        ▼
build/<arch>/rootfs.tar
        │  mkfs.ext4 -d          (scripts/tar-to-ext4.sh, in the images/tools container)
        ▼
build/<arch>/rootfs.ext4         (read-only root disk of the guest)
```

Build it with `scripts/build-rootfs.sh [amd64|arm64]` (host architecture by default). `build/` is
ignored by Git. The only host requirement is rootless Podman.

CI (`.github/workflows/rootfs.yml`) runs the same script for amd64 and arm64, each on a runner of
that architecture, when the image or its scripts change and once a week, then checks that the
tools start in the image.

## Why each step

- **A Dockerfile**: the usual, readable recipe; the same file can feed the gVisor and layer 1
  providers later, since they all start from the same tar.
- **Podman, rootless**: `RUN apt-get install` creates files owned by root. Docker does it as root
  (its daemon, or the `docker` group, which is root-equivalent: D2). Podman does it as a fake root
  in a user namespace, mapped to the ranges in `/etc/subuid` and `/etc/subgid`. Ubuntu ships
  AppArmor profiles for `podman`, `buildah` and `crun` that allow user namespaces, so no profile of
  our own is needed (unlike `runsc`).
- **Tar straight into ext4**: unpacking the tar as a normal user would make every file owned by
  that user. `mkfs.ext4 -d <tar>` copies the owners recorded in the tar instead. It needs
  e2fsprogs 1.47.1 or later built with libarchive: Ubuntu 24.04's is 1.47.0 and Alpine's is built
  without libarchive, so the script runs it in a small Debian 13 container (`images/tools`), the
  same on every machine.

## Contents

Alpine 3.24 (musl), pinned by digest, in two stages: the first downloads and checks every binary
(its `curl` and `unzip` never reach the result), the second installs only what the agent needs.

| Part | Size | Why |
| --- | --- | --- |
| Claude Code (`linux-<arch>-musl` npm package, taken directly) | 327 MB | The agent. One self-contained binary; the `@anthropic-ai/claude-code` package only wraps it, so neither npm nor Node is needed |
| Bun, musl build | 85 MB | The target repository's runtime and package manager |
| Alpine, `bash`, `git`, `ripgrep`, `libstdc++`, `libgcc`, CA certificates | about 10 MB | Shell and Git for the agent; `libstdc++`/`libgcc` for the Bun-built binaries; `ripgrep` because Claude Code does not use its bundled one on musl (`USE_BUILTIN_RIPGREP=0`) |

No Node.js and no Python: the target repository runs everything with Bun (its rules say `bun`
instead of `node`, and its CI installs only Bun). Heavy toolchains used by part of it (Rust,
Solana) are left out until an issue needs them. The agent runs as `agent` (UID 1000) in
`/workspace`.

The ext4 image has no journal and no reserved blocks, and only as many inodes as files plus 10 %,
since the guest mounts it read-only; it is then shrunk with `resize2fs -M` and truncated.

Size, measured: tar 422 MB, ext4 513 MiB. The same tar as squashfs (`sqfstar -comp zstd`) is
180 MB; ext4 or squashfs is still to decide.

A first version on Ubuntu 24.04 with Node.js 24, Python and Claude Code installed through npm was
798 MB as a tar: Node.js with its headers and npm alone took 208 MB.

## Verified (2026-11-16, development machine, amd64)

- Owners checked with `debugfs`: system files are 0:0, `/workspace` is 1000:1000.
- Booted in Firecracker v1.18.0 with Firecracker's CI kernel 6.1.155, root drive read-only,
  2 vCPUs, 1 GiB: `git`, `bun`, `rg` and `claude --version` run; writing to `/usr` fails with
  "Read-only file system".
- In the image under Podman, on a copy of the target repository: `bun install --frozen-lockfile`
  installs 3022 packages in 7 s without Node.js; Biome, Turbo and Lefthook (native binaries) start
  on musl, and `bun run lint` passes.

## To do

- arm64 build on the amd64 machine needs QEMU user emulation (`qemu-user-static` and binfmt); CI
  builds it natively instead. Not built locally yet.
- Our own guest kernel instead of Firecracker's CI kernel, with virtio-net and `CONFIG_IP_PNP`
  for the development network (D44).
- `/etc/resolv.conf` pointing to a public resolver (D44): the host's `127.0.0.53` is not reachable
  from a guest. The image has none; the guest agent writes one on a tmpfs copy of `/etc` (D46),
  and `firecracker/lib.sh` does the same for the shell scripts.
- `curl`: not in the image, and the conformance suite uses it to check reachability.
- Chromium, fonts and Playwright for both architectures, for preview and screenshots (D45).
- Done (D46): `cmd/guest-agent`, a guest init in Go that serves control over vsock.
- A second, writable disk for the workspace.
