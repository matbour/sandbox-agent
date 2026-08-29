# Architecture

## Flow

```
Linear ──webhook──▶ Cloudflare Tunnel ──▶ receiver ──▶ queue ──▶ worker
                                                                   │
            ┌──────────────────────────────────────────────────────┘
            ▼
   1. fetch the issue (Linear API, host credentials)
   2. clone the repository on the host (GitHub credentials, host only)
   3. copy the working tree, without .git, into a fresh workspace
   4. start the sandbox on that workspace ──▶ agent runs, talks only to the egress proxy
   5. collect /out/changes.patch and /out/summary.json
   6. validate the patch (paths, size, symlinks, forbidden files)
   7. apply it to a fresh clone on the host, commit, push a branch
   8. open a draft pull request, comment on the Linear issue
   9. destroy the sandbox, the workspace and the cgroup
```

## Components

All components are in one Go binary for now, split into packages. The sandbox init stage is the same
binary re-executed (see [sandbox.md](sandbox.md)).

| Component | Runs where | Holds secrets | Role |
| --- | --- | --- | --- |
| Receiver | Host | Linear webhook secret | Verifies the signature and timestamp, deduplicates, enqueues, answers fast |
| Queue | Host | No | Bounded, persisted (SQLite is enough), one job per issue, idempotent |
| Worker | Host | Linear API key, GitHub credentials | Runs the flow above, enforces concurrency (start with 1 or 2 jobs) |
| Egress proxy | Host | LLM API keys | The only network endpoint the sandbox can reach. Adds the API key, enforces an allowlist, a per-job budget and logging |
| Sandbox runtime | Host, then inside the sandbox | No | Creates namespaces, cgroup, filesystem, filters; then execs the agent |
| Agent | Inside the sandbox | No | Claude Code in non-interactive mode (see [agents.md](agents.md)) |
| Patch validator | Host | No | Treats the sandbox output as hostile input |
| Publisher | Host | GitHub credentials | Applies the patch to a clean clone, pushes, opens the draft PR |

## Trigger

Reacting to every created issue would burn budget and let anyone with Linear write access run code.
Trigger instead on an explicit signal: a dedicated label (for example `agent`) added to an issue, or
an assignment to a bot user. The receiver ignores everything else.

## Egress proxy

The sandbox has no network interface except loopback. The proxy is reached through a Unix socket
bind-mounted into the sandbox (layer 1) or through vsock (microVM layer). A small forwarder inside the
sandbox listens on `127.0.0.1:<port>` and relays to the socket, so agents only need an HTTP base URL.

The proxy is not a generic forward proxy. It exposes one route per upstream:

| Route | Upstream | Injected header |
| --- | --- | --- |
| `/anthropic/*` | `https://api.anthropic.com/*` | `x-api-key` |

Because the agent speaks plain HTTP to the proxy and the proxy speaks TLS upstream, there is no TLS
interception and no CA to install in the sandbox.

The proxy is also the policy point:

- per-job token and cost accounting, with a hard budget that cuts the job;
- model allowlist;
- rate limit;
- request log (metadata, not bodies, by default) tied to the job ID.

Package registries (npm, PyPI, Go modules) are not reachable in the first version. Options are
discussed in [open-questions.md](open-questions.md).

## Workspace and output

- The host clones the repository, then copies the working tree **without `.git`** into the
  workspace. The sandbox never sees Git metadata, so it cannot plant hooks or config that the host
  would later execute.
- The agent works in `/workspace`. Inside the sandbox, the runtime initialises a throwaway Git
  repository so the agent can use `git diff` and produce `/out/changes.patch`.
- `/out/summary.json` holds the PR title and description written by the agent. The host treats it as
  untrusted text: length limits, no HTML, rendered as plain Markdown.

## Patch validation

The host rejects the whole patch if any of these holds:

- a path is absolute, contains `..`, or targets `.git/`;
- a path matches the deny list: `.github/**`, CI config, `CODEOWNERS`, lockfiles unless allowed per
  repository;
- it creates a symlink, a device or changes file modes to setuid or executable outside an allowlist;
- it exceeds a size limit (lines and bytes) or a file count limit;
- `git apply --check` fails on the clean clone.

Git runs on the host only on clean clones, with hooks and risky features disabled
(`-c core.hooksPath=/dev/null -c core.fsmonitor=false`, protocol restrictions).

## Pull request

- Always a **draft** PR, on a branch `agent/<issue-key>`, with a label and a link to the issue.
- A human reviews and merges. The agent never merges.
- CI is part of the attack surface: a PR branch in the same repository runs workflows that may see
  repository secrets. Denying `.github/**` is not enough if CI runs test scripts the patch can change.
  See [threat-model.md](threat-model.md).

## Credentials

| Credential | Where | Notes |
| --- | --- | --- |
| Linear webhook secret | Host, receiver | Verifies `Linear-Signature` |
| Linear API key | Host, worker | Read the issue, post comments |
| GitHub | Host, publisher | Prefer a GitHub App installed on selected repositories (short-lived installation tokens, fine-grained permissions) over a personal access token |
| Anthropic API key | Host, proxy | Dedicated key with a spend limit |

Store them in a root-only (or service-user-only) file loaded by systemd, never in the repository.

## Lifecycle and limits

| Limit | Initial value | Enforced by |
| --- | --- | --- |
| Wall clock per job | 30 min | Worker (kills the cgroup) |
| Memory | 4 GB | cgroup `memory.max`, `memory.swap.max = 0` |
| CPU | 2 cores, pinned to 4-7 | cgroup `cpu.max`, `cpuset.cpus` |
| Processes | 512 | cgroup `pids.max` |
| Disk | 5 GB workspace | Size-limited tmpfs or loop-mounted image |
| LLM budget | a few dollars per job | Egress proxy |
| Concurrency | 1 or 2 jobs | Worker |

## Deployment

- One systemd service on the board, running as a dedicated system user (`sandbox-agent`), with
  `Delegate=yes` for its cgroup subtree and an AppArmor profile allowing user namespaces.
- systemd hardening on the service itself (`ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges`
  where compatible, restricted address families).
- A tunnel hostname rule pointing to the receiver's local port.
