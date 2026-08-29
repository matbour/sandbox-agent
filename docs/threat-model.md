# Threat model

## Assets

| Asset | Why it matters |
| --- | --- |
| The host | Runs other self-hosted services; root on the host means control of all of them |
| Host credentials | LLM API keys, GitHub credentials, Linear credentials, the tunnel credentials, admin tokens of the other services |
| The LAN | Router, other machines, the board's own admin interfaces |
| Repositories | Code integrity: a malicious change merged by mistake |
| CI secrets | Secrets available to workflows triggered by the agent's branch |
| Money | LLM spend |
| Availability | The board must keep serving its other workloads |

## Adversaries

1. **Issue author.** Anyone who can write an issue, or text the agent reads (comments, linked pages),
   can attempt prompt injection.
2. **Repository content.** Code, tests, build scripts and dependencies can be malicious or
   compromised (supply chain).
3. **The agent itself.** A model that misbehaves, is jailbroken by 1 or 2, or simply makes mistakes.
4. **Internet attacker.** Can reach the webhook endpoint and try to forge or replay events.

Inside the sandbox, assume full arbitrary code execution as the sandbox user. The question is what
that code can reach.

## Trust boundaries

```
Internet ─▶ [Cloudflare] ─▶ receiver (host)            boundary A: signature + timestamp
host ─▶ sandbox                                         boundary B: namespaces, cgroups, filters, VM
sandbox ─▶ egress proxy (host)                          boundary C: one socket, allowlist, budget
sandbox output ─▶ patch validator (host)                boundary D: output is hostile input
host ─▶ GitHub (branch, draft PR) ─▶ CI ─▶ human merge  boundary E: review before merge
```

## Threats and mitigations

| # | Threat | Mitigation | Test |
| --- | --- | --- | --- |
| T1 | Forged or replayed webhook | HMAC-SHA256 signature on the raw body, timestamp window, delivery ID dedup | Receiver unit tests |
| T2 | Prompt injection asks the agent to leak secrets | No secret in the sandbox; proxy injects keys | E-SEC-* |
| T3 | Exfiltration over the network | No interface but loopback; proxy routes to fixed upstreams only; no DNS | E-NET-* |
| T4 | LAN and host services access | Separate network namespace, no route; Docker socket and host paths not mounted | E-NET-*, E-FS-* |
| T5 | Read host files | Private mount namespace, `pivot_root` into a minimal rootfs, Landlock | E-FS-* |
| T6 | Kernel exploit through rarely used syscalls | seccomp allowlist (or strict denylist): no `bpf`, `io_uring_*`, `userfaultfd`, `keyctl`, `perf_event_open`, nested `unshare`/`clone` with namespace flags, `mount`, `ptrace` on outside processes, `kexec`, module loading. MicroVM as a second layer | E-SYS-* |
| T7 | Privilege escalation inside | User namespace with an unprivileged mapping, all capabilities dropped, `no_new_privs`, no setuid binaries, `nosuid` mounts | E-PRIV-* |
| T8 | Escape through runtime bugs of the kind seen in container runtimes | No file descriptors inherited (CVE-2024-21626, leaked fd); the runtime binary not writable from inside (CVE-2019-5736, `/proc/self/exe`); cgroup v2 only (CVE-2022-0492 needed v1 `release_agent`) | E-RT-* |
| T9 | Resource exhaustion hurting the host | cgroup limits on memory, CPU, PIDs, I/O; workspace size limit; wall-clock timeout; pinning to cores 4-7 | E-RES-* |
| T10 | Malicious patch planting host code execution | No `.git` in the sandbox; patch applied to a clean clone; Git hooks and fsmonitor disabled on the host | E-OUT-* |
| T11 | Malicious patch targeting CI secrets | Deny `.github/**` and CI files; draft PR; CI for bot branches requires approval or runs without secrets; human review | Manual check |
| T12 | Malicious code merged by a human | Draft PR, clear bot label, diff limits; out of technical scope beyond that | — |
| T13 | Budget drain | Trigger on label only; per-job budget in the proxy; concurrency limit | Proxy tests |
| T14 | Data sent to the LLM provider | Accepted: the repository content is sent to the chosen provider by design. Choose providers and repositories accordingly | — |

## Residual risks

- A kernel bug reachable through allowed syscalls breaks layer 1. This is why the microVM layer
  exists: the guest kernel is then the first target, and KVM the second.
- Registry access, once allowed, becomes an exfiltration channel (package names and versions can
  encode data). Keep it closed or behind a caching mirror with an allowlist.
- The PR itself is a channel: the agent can write anything into the diff and the description.
  Reviewers must treat agent PRs like external contributions.
- The egress proxy is reachable from the sandbox: it is attack surface and must be written
  defensively (size limits, strict routing, no open proxying).
