# Global architecture: one manager, several runners

Proposed on 2026-11-20 (D42, with D43 to D45), not implemented yet. It builds on the manager and
worker protocol ([protocol.md](protocol.md), D33) and changes what sits above it. The steps are
milestone R in [roadmap.md](../roadmap.md). Until R is done, [architecture.md](../architecture.md)
describes what runs today.

## Goal

A Go control plane that schedules agent jobs onto Firecracker microVMs on several machines of
different architectures, at the same time. The first demonstration is an arm64 runner (the board)
and an amd64 runner (the development machine) behind one manager, each turning the same OCI image
into a microVM. The workload is the existing one: a Linear issue in, a draft GitHub pull request
out, with screenshots of the result.

It is built to be used by others later, so configuration, credentials and limits are designed for
that from the start, even where the development phase relaxes them (D43, D44).

Not goals:

- A highly available manager. There is one; if it is down, nothing new starts, and running jobs
  finish and wait for it.
- Runners operated by someone else. Every runner runs on a machine of the owner. A runner is trusted
  to see the code it works on, not to publish anything.
- A general job system. The only job is "run an agent on a repository and return a change".
- Other trackers and forges for now: Linear and GitHub only, behind interfaces.

## Overview

```
                     ┌──────────────────────── manager (agentd) ────────────────────────┐
 Linear webhook ──┐  │                                                                  │
 Linear polling ──┼─▶│ sources ─▶ inbox ─▶ sessions (durable steps) ─▶ outbox ─▶ sinks ──┼─▶ Linear
 CLI            ──┘  │                          │        ▲                              │─▶ GitHub
                     │                       scheduler   │ events, change set,          │─▶ overview
                     │                          │        │ screenshots                  │
                     └──────────────────────────┼────────┼──────────────────────────────┘
                                   lease a run  │        │   (runners dial out, HTTPS)
                        ┌───────────────────────┴─┐    ┌─┴───────────────────────┐
                        │ runner (board, arm64)   │    │ runner (desktop, amd64) │
                        │ firecracker (sbx)       │    │ firecracker             │
                        │   └▶ microVM ─▶ worker  │    │   └▶ microVM ─▶ worker  │
                        └────────────┬────────────┘    └────────────┬────────────┘
                                     └──────── tap + NAT ───────────┴──▶ internet (D44)
```

## Roles

| Role | Process | Where | Holds | Does |
| --- | --- | --- | --- | --- |
| Sources | inside `agentd` | Manager host | Webhook secrets, Linear token | Turn webhooks, polling and the CLI into three requests: *start*, *follow-up*, *stop*. No logic |
| Manager | `agentd` | One host (the board) | Linear, GitHub, project secrets | Sessions and their steps, scheduling, workspace input, validation, push, PR, checks, state, overview |
| Sinks | inside `agentd` | Manager host | Linear, GitHub | Publish what the manager decided: activities, comments, uploads, branches, PRs |
| Runner | `runnerd` (new) | Every machine that runs jobs | Its runner token | Lease runs, prepare the workspace and the microVM, stream events, return the change set and the screenshots |
| Provider | package inside `runnerd` | Runner host | Nothing | `firecracker` first, written in Go; `sbx` stays as the baseline (D30) |
| Worker | `worker` | Inside the microVM | What the run spec gives it (D43) | D33: task in, events out, workspace modified. Also runs the preview step (D45) |

The split follows two lines. **Privilege:** Linear and GitHub credentials exist only on the
manager. **Lifetime:** a session lasts days and belongs to the manager, a run lasts minutes and
belongs to one runner, and nothing on a runner outlives its run.

The egress proxy of M4 (`vsock-to-anthropic`, `loopback-to-vsock`) is shelved for the development
phase (D44). It comes back, with network limits, before anyone else uses the system.

## Three levels: session, step, run

- A **session** is one piece of work on one issue (D33): start, questions, follow-ups, CI fixes,
  until complete, error or stop. It lives in the manager's store.
- A **step** is one thing the manager does for a session: prepare the input, run, validate, push,
  open the PR, wait for checks, report. Each step is idempotent and its result is recorded before
  the next one starts, so a manager restart resumes the session at the step it was in. Waiting for
  CI is a step with a timer and a GitHub event, not a blocked goroutine, so it holds no runner.
- A **run** is one worker in one microVM on one runner. The manager creates it, the scheduler gives
  it to a runner, and it ends with a terminal event (`ask`, `done`, `fail`), a change set and its
  screenshots, or is lost.

Fix passes after failed checks and answers to questions are new runs in the same session, possibly
on another runner.

## Configuration

Three scopes, one file each. The project file is what a user of the system writes; the other two
belong to whoever runs the manager and the machines.

| Scope | Written by | Lives on | Secrets |
| --- | --- | --- | --- |
| Deployment | The operator, once | Manager | Linear, GitHub, webhook secrets |
| Project, one per repository | The user | Manager | References only, values in the manager's secret store |
| Runner, one per machine | The machine's owner | Each runner | Its runner token |

### Deployment

What `config.env` holds today that is not about one repository: the Linear OAuth application and
webhook secrets, the agent's name, the GitHub App ID and private key (installation IDs are looked
up per repository), the public URL and Cloudflare Access settings, the label-to-model mapping, and
global limits (concurrent runs, daily spend).

### Project

```yaml
name: webapp
repository: owner/webapp            # GitHub, through the App
base_branch: main
linear:
  teams: [ENG]                      # which issues map to this repository
  trigger_label: agent

environment:
  image: ghcr.io/owner/agent-env@sha256:…   # multi-arch OCI image, pinned by digest
  setup: setup.sh                   # runs in the microVM before the agent; must be arch-neutral
  notes: setup.notes.md             # appended to the prompt
  resources: { vcpus: 4, memory: 8g, disk: 20g }
  timeouts: { setup: 20m, agent: 45m }
  env: { NODE_ENV: test }           # non-secret
  secret_env: [CLAUDE_CODE_OAUTH_TOKEN]   # development phase only (D43)

agent:
  backend: claude                   # or fake (scripted, no model; for tests and load)
  model: claude-sonnet-5-5
  max_turns: 150
  budget_usd: 5                     # self-reported until the egress proxy returns

preview:                            # D45, optional
  start: bun run dev
  port: 3000
  ready: /
  timeout: 2m
  env: { NEXT_PUBLIC_API_MOCK: "1" }
  viewport: { width: 1440, height: 900 }
  max_pages: 5

publish:
  commit_author: { name: …, email: … }
  deny_paths: [infra/]
  limits: { files: 200, bytes: 2MB }
  ci: { timeout: 25m, fix_attempts: 2, ignore: [claude-review] }

requires: {}                        # optional: arch, provider
```

The file format (YAML needs a dependency, D1) is decided at R3.

### Runner

The manager's URL and the runner token; a name (the architecture is detected); the providers and
their binaries (Firecracker, jailer, kernel); capacity offered to the manager (vCPUs, memory, disk),
the CPU set for guests (cores 4-7 on the board); the taps it may use (D44); a cache directory.

### Secrets

| Kind | Example | Where |
| --- | --- | --- |
| Identity | Linear, GitHub App | Manager only, never on a runner |
| Model credentials | Claude subscription token (dev), API key (later) | Development phase: project secret on the manager, delivered in the run spec as `secret_env`, set in the guest environment (D43). Later: the egress proxy, never in the guest |
| Build credentials | Private registry token | Not supported yet. Later: egress proxy routes with header injection |
| Runtime and test secrets | Database URL, third-party API keys | Not supported. Local services in the image, mocks, or tests left to CI |

The manager scans every change set, event and PR text for the values it sent as `secret_env`, and
rejects the run if one appears.

## Runner

A runner is a long-running daemon on each machine. It:

1. dials the manager and registers: runner ID, version, protocol version, architecture, providers,
   images it already converted, capacity;
2. asks for a lease (long poll) whenever it has room;
3. downloads the run's base tree, prepares the workspace, starts the microVM, runs the worker (D33)
   and streams its events to the manager, tagged with the run ID;
4. computes the change set and uploads it with the screenshots and the terminal event;
5. destroys the microVM and the workspace, and drops the run's secrets.

It keeps no state between runs except caches (base trees by commit, converted images). The
development machine can come and go: its runner registers when it starts, and the manager stops
giving it runs when its heartbeats stop.

### The Firecracker provider, in Go

The point of the demonstration, so it is written in Go rather than driven by the shell scripts in
`firecracker/`:

- a small client for Firecracker's HTTP API over its Unix socket (machine config, boot source,
  drives, network interface, actions), without `firecracker-go-sdk`;
- the jailer, with a cgroup and the runner's CPU set per microVM;
- an OCI image (multi-arch index, pinned by digest) turned into a read-only ext4 root disk for the
  runner's architecture, cached by digest, plus a writable disk per run;
- the workspace as a disk built from the base tree and read back after the run (Firecracker has no
  shared directory; virtio-fs is not supported);
- one tap per running microVM on a bridge with NAT, as Docker does (`deploy/network/`), the guest
  address on the kernel command line (D44);
- the worker binary for the runner's architecture on a read-only disk, so its version matches the
  runner;
- later: snapshot a booted guest once per image and architecture, restore it per run, keep a small
  warm pool, and measure cold boot against restore.

How the worker's task and events cross the VM boundary without vsock (serial console, or the guest
reaching the runner over the tap) is decided at R2 (roadmap).

## Manager and runner protocol

HTTPS with JSON bodies, Go standard library on both sides. The runner always dials out, so runners
need no open port and can sit behind NAT. Each runner has its own token, stored on the runner host
under `secrets/`; the manager keeps a hash per runner ID and can revoke one.

| Call | Direction | Meaning |
| --- | --- | --- |
| `register` | runner → manager | Capabilities and versions. A protocol version the manager does not speak is refused |
| `lease` | runner → manager | Long poll. Returns a run spec with a lease ID and a deadline, or nothing |
| `heartbeat` | runner → manager | Extends the leases the runner holds. The response carries cancellations (Linear Stop, D28) |
| `input` | runner → manager | Downloads the run's base tree |
| `events` | runner → manager | Appends event lines for a lease, with a sequence number so a retry is not counted twice |
| `result` | runner → manager | Uploads the change set, the screenshots and the terminal event; ends the lease |

Every call after `register` names a lease, and the manager rejects calls for a lease that expired
or belongs to another runner. Late events from a lost run are dropped.

### The run spec

The D33 task (prompt, model, agent, setup script, limits), plus:

| Field | Meaning |
| --- | --- |
| `run` | Run ID, session ID, attempt |
| `base` | Repository name and base commit, for the input and the runner's cache |
| `image` | Image digest; the runner picks its platform from the index |
| `resources` | vCPUs, memory, disk |
| `requires` | Architecture, provider, if any. Empty means any runner |
| `env`, `secret_env` | Environment for the guest. `secret_env` is never logged or written to disk by the runner (D43) |
| `preview` | The project's preview block, if any (D45) |
| `task` | The D33 task, unchanged |

It contains no Linear or GitHub identity, as in D33.

### Workspace in and out

- **In.** The manager clones with its GitHub token, as today, and serves the tree at the base
  commit **without `.git`** (D5) as a compressed tar. The runner caches it by commit, so a follow-up
  run on the same commit downloads nothing. The runner never has GitHub credentials and never runs
  Git on repository content.
- **Out.** The runner keeps a pristine copy of the input tree and compares the workspace with it
  after the run: changed and new files (contents and mode), deleted paths. The change set is
  uploaded as a tar with a manifest, after the same size and count limits as `internal/patch`.
- **Screenshots** come back separately (D45) and are never part of the change set.
- **Authority stays with the manager.** It applies the change set to its own clean clone and runs
  `patch.Validate`, the conflict-marker check (D24) and the secret scan. A change set is hostile
  input whether it comes from the guest or from a runner.

## Scheduling

- Each runner offers a capacity (vCPUs, memory, disk); each run asks for `resources`. The manager
  places a waiting run on a runner that matches `requires` and has room, and bin-packs: the runner
  that ran the session's previous run first (warm cache), then the one with the least room left
  that still fits. No preemption.
- The board offers a small capacity (cores 4-7, a few GB) because it serves other workloads; the
  development machine offers more.
- Per-model and global limits live in the manager, so total spend stays bounded whatever the
  number of runners.
- The `fake` agent backend makes scheduling testable at load: many runs at once, no model, no cost.

## Failures

| What fails | What happens |
| --- | --- |
| A run's lease expires (runner or network gone) | The run is `lost`. If the worker had not started the agent yet, the manager retries it once on any runner. Otherwise the session gets an error and a person decides: an agent run costs money and may not be repeatable |
| The runner restarts | It kills the microVMs it owns and registers again. Its old leases expire |
| The manager restarts | Sessions resume from the step they were in. Runners keep running, buffer events on disk, and resend them with their sequence numbers once the manager answers, within the lease deadline |
| Linear or GitHub is down | The outbox retries. Steps that need them wait |
| Stop in Linear | The manager marks the run cancelled; the runner sees it in its next heartbeat response and kills the microVM. Nothing is pushed |

## Trust

A runner host is the owner's machine and is not hostile by itself. Its output is still handled like
worker output (boundary D in [threat-model.md](../threat-model.md)): the guest may have attacked
the worker, the runner or the host. So the manager applies the same limits and strict parsing to
everything a runner sends (E-PROTO), and validates every change set.

Boundary F, runner ↔ manager. A stolen runner token gives the base trees of the repositories it
leases runs for, the `secret_env` of those runs, and the ability to submit change sets and events
for its own leases, which still go through validation, the draft PR and human review. It gives
nothing on Linear or GitHub, and no other runner's runs. Tokens are per machine, revocable, and
never enter a guest. The runner endpoint requires TLS except on loopback. How the development
machine reaches the manager is in the private notes once decided.

The development phase weakens the guest boundary on purpose: the guest holds a model credential
(D43) and reaches the whole network, LAN included (D44). See those entries for what that allows.

## One machine is the same design

On one machine, `agentd` and `runnerd` run side by side and talk over loopback through the same
protocol. There is no in-process shortcut, so the single-machine setup tests the multi-machine one.
`agentd -try` moves to the runner (`runnerd -try`): it runs one task on a local repository with no
manager, Linear or GitHub.

## Binaries after R

| Binary | Where | Change |
| --- | --- | --- |
| `agentd` | Manager host | Loses the providers; gains the session engine, scheduler and runner API |
| `runnerd` | Every runner host | New. Holds the providers (D30 moves here), the Firecracker provider in Go |
| `worker` | Inside the guest | D33, plus the preview step (D45) |
| `sandbox`, `escape` | Unchanged | — |
| `vsock-to-anthropic`, `loopback-to-vsock` | Shelved (D44) | Kept in the tree, not built into the flow |

The import test of D33 is extended: `cmd/runnerd` must not depend on `internal/linear`,
`internal/github` or `internal/gitops`, and `cmd/agentd` must not depend on `internal/sandbox`.

## What this changes in D33

- Providers are compiled into `runnerd`, not `agentd`.
- "A separate host process per job" (left open in D33): one runner process per machine, several
  runs per runner.
- "Interfaces for the tracker and the forge" (left open in D33): the session engine uses them from
  the start.
- A new non-terminal event, `artifact` (D45).
- The task, the other events, the worker's role and the session state machine are unchanged.

## Open

- **Store.** SQLite was turned down in D23 (cgo, or a large pure-Go dependency). Options: JSON files
  with one writer in memory and append-only logs, bbolt, or `modernc.org/sqlite`. To decide at R3.
- **Task and events across the VM boundary** without vsock (R2).
- **Firecracker on the board**, with vCPUs pinned to cores 4-7: not tried yet (M1). If it does not
  boot there, the board's runner uses `sbx` for the demonstration.
- **Network on the board.** A tap and NAT change the board's system configuration, which this
  project does not touch without the owner's agreement (CLAUDE.md, D44). Decided when the board's
  runner joins.
- **How runners reach the manager** from outside the board's loopback (private notes).
- **Base trees for large repositories.** A tar per commit may be slow for the target monorepo (to
  measure). Fallback: a delta against a commit the runner already has.
- **Cloud runners** (roadmap, "Later"): a runner of the owner using a cloud provider's API, so the
  model stays the same.
