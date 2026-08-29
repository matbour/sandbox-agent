# Sandbox, layer 1: Linux primitives

The first layer is a purpose-built runtime, written in Go, using the same kernel features as
container runtimes (runc, crun, bubblewrap, nsjail), without Docker or an OCI runtime. The goal is a
small runtime where every protection is explicit and tested.

## Two-stage process

Go programs are multithreaded from the start, and some operations (joining a user namespace with
`setns`, for example) require a single-threaded process. runc solves this with a C constructor
(`nsexec`). The simpler pattern here:

1. **Parent** (on the host): prepares the workspace, the cgroup and the rootfs, then starts the
   child with `exec.Cmd` and `SysProcAttr.Cloneflags`, so the kernel creates the namespaces at
   `clone` time. It writes the UID and GID maps (`SysProcAttr.UidMappings`, `GidMappings`).
2. **Child** (`/proc/self/exe init`, the same binary re-executed): runs inside the new namespaces,
   sets up mounts, `pivot_root`, hostname, drops capabilities, applies Landlock and seccomp, closes
   every file descriptor it does not need, then `execve`s the agent.

The re-executed binary must not be writable from inside the sandbox (see CVE-2019-5736). Options:
copy it to a sealed `memfd` before re-exec, as runc does, or make sure the file the child execs is
outside every mount visible from inside. The second is enough here because the child pivots into a
rootfs that does not contain it, but write a test anyway.

## Namespaces

| Flag | Purpose |
| --- | --- |
| `CLONE_NEWUSER` | Root inside maps to an unprivileged UID outside. Created first, so the others are owned by it |
| `CLONE_NEWNS` | Private mounts; then `pivot_root` and unmount the old root |
| `CLONE_NEWPID` | The agent cannot see or signal host processes; the init stage is PID 1 and must reap zombies |
| `CLONE_NEWNET` | Only loopback. Also isolates abstract Unix sockets from the host |
| `CLONE_NEWUTS` | Own hostname |
| `CLONE_NEWIPC` | Own SysV IPC and POSIX message queues |
| `CLONE_NEWCGROUP` | `/sys/fs/cgroup` shows only the sandbox's own subtree |
| `CLONE_NEWTIME` | Optional, not needed |

User namespaces need the AppArmor profile described in [host.md](host.md), or a root parent.

## Filesystem

```
/            read-only rootfs (minimal distribution + agent runtime + toolchains)
/workspace   read-write, size-limited, the repository working tree (no .git from the host)
/out         read-write, small, for changes.patch and summary.json
/tmp         tmpfs, size-limited
/proc        new procfs for the PID namespace, with hidepid if useful
/dev         minimal: null, zero, full, random, urandom, tty, pts, shm (no block devices)
/run/proxy   the egress proxy Unix socket, nothing else
```

- All mounts `nosuid`, `nodev`; `noexec` where possible (not on `/workspace`: builds and tests run there).
- Mask sensitive procfs and sysfs paths like container runtimes do (`/proc/kcore`, `/proc/keys`,
  `/proc/sysrq-trigger`, `/sys/firmware`, …) or do not mount sysfs at all.
- Rootfs source: export an arm64 image (for example Debian or Ubuntu with Node.js, Git, Python, Go)
  to a directory or an ext4 image. The same image serves the microVM layer. Building it does not
  need Docker on the board: `crane export` or `skopeo` + `umoci`, run as the development account.

## cgroups v2

- One cgroup per job under a delegated subtree. Write `memory.max`, `memory.swap.max`, `cpu.max`,
  `cpuset.cpus` (4-7), `pids.max`, `io.max` before starting the child, then put the child in it
  (or use `clone3` with `CLONE_INTO_CGROUP`).
- Teardown: `cgroup.kill`, wait for `cgroup.events` to show `populated 0`, remove the directory.
- Delegation: as a service, systemd `Delegate=yes`. During development without root, a systemd user
  manager can delegate (`systemd-run --user --scope -p Delegate=yes`), which needs linger or an
  active session.

## Capabilities and privileges

- Drop every capability from all sets (bounding, permitted, effective, inheritable, ambient) after
  setup.
- `PR_SET_NO_NEW_PRIVS = 1` (also required to install seccomp without `CAP_SYS_ADMIN`).
- Run the agent as a non-zero UID inside the namespace.
- `RLIMIT_CORE = 0`, sensible `RLIMIT_NOFILE`.

## Landlock

Restrict file access even inside the mount namespace (defense in depth):

- read and execute: `/usr`, `/bin`, `/lib`, `/etc` (rootfs);
- read and write: `/workspace`, `/out`, `/tmp`;
- nothing else.
- With Landlock ABI 4 and later, also restrict TCP bind and connect (only the local forwarder port).

Library: `github.com/landlock-lsm/go-landlock` (pure Go, works with `CGO_ENABLED=0`). Check the ABI
level the kernel supports at runtime and log it.

## seccomp

- `CGO_ENABLED=0` excludes `libseccomp-golang`. Use a pure-Go BPF generator (for example
  `github.com/elastic/go-seccomp-bpf`) or assemble the filter with `golang.org/x/net/bpf` and install
  it with `unix.Prctl` or the `seccomp` syscall.
- Check the architecture field (`AUDIT_ARCH_AARCH64`) first and kill on mismatch.
- Start from Docker's default profile logic, then tighten. Explicitly blocked:
  `bpf`, `io_uring_setup`, `io_uring_enter`, `io_uring_register`, `userfaultfd`, `keyctl`, `add_key`,
  `request_key`, `perf_event_open`, `kexec_load`, `init_module`, `finit_module`, `delete_module`,
  `mount`, `umount2`, `pivot_root`, `unshare`, `setns`, `clone` and `clone3` with namespace flags,
  `ptrace`, `process_vm_readv`, `process_vm_writev`, `open_by_handle_at`, `name_to_handle_at`,
  `swapon`, `swapoff`, `reboot`, `settimeofday`, `clock_settime`, `acct`, `quotactl`, `fsopen`,
  `fsconfig`, `fsmount`, `move_mount`, `open_tree`.
- `clone3` arguments live in memory, so seccomp cannot inspect its flags: return `ENOSYS` for
  `clone3` so libc falls back to `clone`, whose flags can be filtered.
- Default action for unknown syscalls: `EPERM` (or `ENOSYS`) during development, with logging, then
  decide.

## Network

- New network namespace: bring `lo` up, nothing else.
- The proxy Unix socket is bind-mounted at `/run/proxy/proxy.sock`.
- A tiny forwarder (part of the init stage, running as the sandbox user) listens on
  `127.0.0.1:8080` and relays to the socket. Agents get `ANTHROPIC_BASE_URL=http://127.0.0.1:8080/anthropic`
  and the equivalent for any other provider.
- No `/etc/resolv.conf` with a real resolver: DNS resolution must fail.

This design needs no veth pair, no bridge and no nftables rule on the host, so it works rootless.

## AppArmor

Beyond `userns,`, a later version of the profile can confine the runtime itself (deny access to
`/var/run/docker.sock`, `/data`, `/root`, other users' homes), as a third independent control.

## Libraries and references

- `golang.org/x/sys/unix` for syscalls, mounts, `pivot_root`, `prctl`, capabilities.
- runc `libcontainer` and bubblewrap sources as references for mount and procfs masking order.
- nsjail as a reference for seccomp and resource limit options.
