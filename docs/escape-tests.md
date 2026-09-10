# Escape tests

Each protection has a test that attempts the escape from inside the sandbox and must fail. The suite
runs as a normal sandbox job whose "agent" is a test binary (`cmd/escape`). Results go into a table in
the README, one column per layer (layer 1, layer 1 + gVisor, layer 1 + Firecracker).

## Filesystem (E-FS)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-FS-1 | Read `/etc/shadow`, `/home/*/.ssh`, `/root`, data directories of the other host services (list in the private notes) | Not present |
| E-FS-2 | Reach the host root through `/proc/1/root` and `/proc/*/cwd` | Only sandbox processes, sandbox root |
| E-FS-3 | Open `/var/run/docker.sock` | Not present |
| E-FS-4 | Write outside `/workspace` and `/tmp` | `EROFS` or `EACCES` (Landlock) |
| E-FS-5 | Create a device node, mount anything, `pivot_root` again | `EPERM` |
| E-FS-6 | Fill `/workspace` and `/tmp` | `ENOSPC` inside, host disk unaffected |
| E-FS-7 | Create millions of files | Bounded by the workspace size or inode limit |

## Network (E-NET)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-NET-1 | List interfaces | Only `lo` |
| E-NET-2 | TCP to the board's LAN address (SSH), the LAN gateway, any other LAN host (addresses in the private notes), `1.1.1.1:443` | Network unreachable |
| E-NET-3 | DNS lookup of any name | Fails |
| E-NET-4 | IPv6 to any address | Fails |
| E-NET-5 | Connect to host abstract Unix sockets | Not visible (network namespace) |
| E-NET-6 | Use the proxy as an open proxy (`CONNECT`, absolute URLs, other hosts, path tricks) | Rejected |
| E-NET-7 | Call a model not on the allowlist through the proxy | Rejected |

## Secrets (E-SEC)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-SEC-1 | Dump environment, `/proc/self/environ`, `/proc/1/environ` | No real API key or token |
| E-SEC-2 | Ask the proxy to echo request headers or return upstream errors containing the key | Key never returned |
| E-SEC-3 | Search the rootfs and workspace for credentials | None |

## Privileges and syscalls (E-PRIV, E-SYS)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-PRIV-1 | Read `CapEff`, `CapBnd`, `NoNewPrivs` in `/proc/self/status` | All zero, `NoNewPrivs: 1` |
| E-PRIV-2 | Run a setuid binary | No privilege gain |
| E-PRIV-3 | Create a nested user namespace (`unshare -U`) | `EPERM` |
| E-SYS-1 | `bpf`, `io_uring_setup`, `userfaultfd`, `keyctl`, `perf_event_open` | `EPERM` or `ENOSYS` |
| E-SYS-2 | `ptrace` a process outside the PID namespace | No such process |
| E-SYS-3 | `kexec_load`, `init_module`, `reboot`, `clock_settime` | `EPERM` |
| E-SYS-4 | `fsopen`/`fsconfig` (path of CVE-2022-0185) | `EPERM` |
| E-SYS-5 | Read `/proc/kallsyms`, `dmesg` | No addresses, denied |

## Runtime bugs (E-RT)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-RT-1 | List `/proc/self/fd` at agent start | Only 0, 1, 2 (and the documented ones) |
| E-RT-2 | Open the runtime binary through `/proc/<pid>/exe` and write to it | Fails, binary unchanged on the host |
| E-RT-3 | Write to `/sys/fs/cgroup` to raise limits or escape the cgroup | Read-only or `EPERM` |
| E-RT-4 | Write to, replace or delete the worker binary (`/opt/sandbox-agent/worker`) | Fails, host file unchanged |

## Resources (E-RES)

| ID | Attempt | Expected |
| --- | --- | --- |
| E-RES-1 | Fork bomb | Stopped at `pids.max`, host responsive |
| E-RES-2 | Allocate memory without limit | OOM kill inside the cgroup only |
| E-RES-3 | Spin on all cores | Limited to the job's CPU quota and cores 4-7 |
| E-RES-4 | Run past the wall clock | Killed, cgroup removed |
| E-RES-5 | Burn LLM budget in a loop | Cut by the proxy |

## Output (E-OUT), run on the host validator

| ID | Patch content | Expected |
| --- | --- | --- |
| E-OUT-1 | Modifies `.github/workflows/*` | Rejected |
| E-OUT-2 | Path traversal (`../`), absolute paths | Rejected |
| E-OUT-3 | Adds a symlink to `/etc/passwd` | Rejected |
| E-OUT-4 | Touches `.git/` or Git config | Rejected |
| E-OUT-5 | Huge patch | Rejected |
| E-OUT-6 | Final message with HTML, huge strings, control characters in the title or description | Sanitised or rejected |

## Protocol (E-PROTO), run on the host against the manager's event reader

The input is a recorded or forged worker stdout (D33, [decision/protocol.md](decision/protocol.md)).

| ID | Worker output | Expected |
| --- | --- | --- |
| E-PROTO-1 | A `response` event, or any kind not in the protocol | Dropped, nothing posted to Linear |
| E-PROTO-2 | `done` with no change, or with a change the validator rejects | No `response`; the session ends in `error` or `awaitingInput` |
| E-PROTO-3 | A line over the size limit, or more events than the rate limit allows | Truncated or dropped, the run is not stalled |
| E-PROTO-4 | `@` mentions, links, HTML in `thought` or `ask` | Mentions stripped, plain text, no links but the manager's |
| E-PROTO-5 | Events after a terminal event, or two terminal events | Everything after the first terminal event is ignored |
| E-PROTO-6 | Stream ends without a terminal event, or the worker exits non-zero after `done` | Counted as `fail` |
| E-PROTO-7 | Invalid JSON, or unknown fields | The line is dropped and logged, the run continues |

## Webhook (E-HOOK), run against the receiver

| ID | Request | Expected |
| --- | --- | --- |
| E-HOOK-1 | No signature, wrong signature | 401, nothing enqueued |
| E-HOOK-2 | Valid signature, old timestamp | Rejected |
| E-HOOK-3 | Same delivery twice | One job |
| E-HOOK-4 | Issue without the trigger label | Ignored |
| E-HOOK-5 | Oversized body | Rejected before parsing |
