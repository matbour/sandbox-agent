# Architecture

This page describes what runs today, with Docker Sandboxes as the only provider. The target split
into a manager and a worker that speak an event protocol is in
[decision/protocol.md](decision/protocol.md) (D33). Its steps are milestone W in the
[roadmap](roadmap.md).

## Flow

```
Linear ── label (polling, D15) ─────────────┐
       ── webhook (D22), agent session (D25) ─┴─▶ agentd ── queue, state.json (D23)
                                                     │
            ┌────────────────────────────────────────┘
            ▼
   1. fetch the issue and its comments (Linear OAuth application, D21)
   2. clone the repository on the host (GitHub App installation token, D12)
   3. copy the working tree, without .git, into a fresh workspace (D5)
   4. start a sandbox on that workspace through the provider (sbx, D30)
   5. run the setup script, then `claude -p` with the rendered prompt
   6. stage the workspace against the clean clone, validate the change set
   7. commit, push `agent/*`, open a draft pull request
   8. wait for checks, run fix passes, mark ready or hand back (D26)
   9. report in Linear (comments, or activities in an agent session), close the sandbox
```

Mentions on an issue that already has an agent pull request update that pull request (D24).
Mentions without a pull request start a research run that changes nothing (D27).

## Components today

| Component | Where | Holds secrets | Role |
| --- | --- | --- | --- |
| `agentd` | Host, systemd user unit | Linear, GitHub | Polling, webhook receiver, queue, state, job runner, overview at `/` |
| Provider `sbx` | Host | No | Starts one Docker Sandboxes microVM per job ([docker-sandboxes.md](docker-sandboxes.md)) |
| `sbx` proxy | Host | Anthropic key (sbx secret store) | Injects the key and applies the network policy (D14) |
| Agent | Inside the VM | No | Claude Code, non-interactive ([agents.md](agents.md)) |
| `bin/sandbox` | — | — | Stub. Becomes the layer 1 runtime (M3, [sandbox.md](sandbox.md)) |
| `vsock-to-anthropic` | Host | Anthropic key (`secrets/anthropic-api-key`) | First version of the M4 proxy (D40, D41). Used by the Firecracker spike, not by `agentd` yet |
| `loopback-to-vsock` | Inside the microVM | No | Relays `127.0.0.1:8080` to the host over vsock |

The target components (manager, egress proxy, runtime, worker) are listed in
[decision/protocol.md](decision/protocol.md).

## Egress

Today: the `sbx` proxy. The global policy is `allow-all` minus loopback, private ranges and cloud
metadata (D14). Package registries and GitHub are reachable. This is an accepted risk until the
service is useful, and the next steps are to deny GitHub writes and then move to an allowlist.

Target, the egress proxy of M4: the sandbox has no network interface except loopback. The proxy is
reached through a Unix socket bind-mounted into the sandbox (layer 1), or through vsock (microVM).
A small forwarder inside the sandbox listens on `127.0.0.1:<port>` and relays to the socket, so
agents only need an HTTP base URL. It is not a generic forward proxy. It has one route per upstream:

| Route | Upstream | Injected header |
| --- | --- | --- |
| `/anthropic/*` | `https://api.anthropic.com/*` | `x-api-key` |

The agent speaks plain HTTP to the proxy and the proxy speaks TLS upstream, so there is no TLS
interception and no CA to install in the sandbox. The proxy is also the policy point: per-job
token and cost accounting with a hard budget, a model allowlist, a rate limit, and a request log
(metadata, not bodies) tied to the job ID. Dependencies are discussed in
[open-questions.md](open-questions.md).

## Workspace and output

- The host clones the repository, then copies the working tree **without `.git`** into the
  workspace. The sandbox never sees Git metadata, so it cannot plant hooks or config that the host
  would later execute.
- The workspace is shared with the host (virtiofs with `sbx`). After the agent exits, the host runs
  `git add --all` and `git diff --cached` with the workspace as the work tree of the clean clone.
  The sandbox never produces a patch file.
- The pull request title and description come from the agent's final message (`STATUS:`,
  `QUESTION:`, `TITLE:`, D26). The host treats them as untrusted text.

## Change validation

`internal/patch` rejects the whole change set if any of these holds:

- a path is absolute, contains `..`, or targets `.git/`;
- a path matches the deny list (CI directories, `CODEOWNERS`, `.gitmodules`, hook managers, agent
  and editor settings, `.npmrc` and similar, environment files, plus `DENY_PATHS`);
- it creates a symlink or a submodule, or uses an unexpected file mode;
- it exceeds the file count (`MAX_FILES`, 200) or size (`MAX_PATCH_BYTES`, 2 MB) limit.

Lockfile changes, package manifests and new executables pass with a warning. Build and cache
artifacts (`node_modules/`, `dist/`, `__pycache__/`…) are excluded from the change set through the
clean clone's `.git/info/exclude`. Git runs on the host only on clean clones,
with hooks and risky features disabled (`core.hooksPath=/dev/null`, `core.fsmonitor=false`).

## Pull request

- Always a **draft** pull request first, on a branch `agent/<issue-key>`, linked to the issue.
- After the checks pass it is marked ready for review. A human reviews and merges, and the agent
  never merges.
- CI is part of the attack surface: a branch in the same repository runs workflows that may see
  repository secrets. Denying `.github/**` is not enough if CI runs test scripts the change can
  modify. See [threat-model.md](threat-model.md).

## Credentials

All host credentials are files in `secrets/` at the repository root, ignored by Git, `0700`, files
`0600` (D17).

| Credential | Where | Notes |
| --- | --- | --- |
| Linear OAuth application | `agentd` | Client credentials, `app` actor (D21, D25, D34) |
| Linear webhook secrets | `agentd` | One per line: workspace webhook and application webhook (D25) |
| GitHub App private key | `agentd` | Installation token per job, one hour (D12, D19) |
| Anthropic API key | `sbx` secret store | Injected by the `sbx` proxy. Later the egress proxy (M4) |

## Limits

| Limit | Default | Enforced by |
| --- | --- | --- |
| Agent wall clock | `AGENT_TIMEOUT`, 45 min | `agentd` (cancels the exec) |
| Setup script | 20 min | `agentd` |
| Agent turns and budget | `AGENT_MAX_TURNS` 150, `AGENT_BUDGET_USD` 5 | Claude Code flags (self-enforced) |
| CPU | `SANDBOX_CPUS` 4, VM threads on cores 4-7 | `sbx` daemon pinned by `agentd` |
| Memory | `SANDBOX_MEMORY` 6g, page cache dropped (D29) | `sbx` |
| CI wait | `CI_TIMEOUT` 25 min, 2 fix passes | `agentd` |
| Concurrency | One job at a time | `agentd` |

## Deployment

- Today: a systemd **user** unit (`deploy/systemd/`), with lingering enabled, running
  `bin/agentd` from the repository. `agentd` itself runs on cores 0-3, and the VMs on 4-7.
- The HTTP server listens on loopback only and is exposed through a dedicated tunnel hostname,
  behind Cloudflare Access, with a bypass for signed webhooks (D22).
- Later: a dedicated system user, `Delegate=yes` for the cgroup subtree of the layer 1 runtime,
  and systemd hardening on the service (open question in [open-questions.md](open-questions.md)).
