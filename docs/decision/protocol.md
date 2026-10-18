# Manager, worker and protocol

Decided on 2026-11-14 (D33). Implemented on 2026-11-20 for Firecracker (`internal/task`,
`cmd/worker`, `internal/job/agentrun.go`); `sbx` still runs the agent from the host. What is left
is in [roadmap.md](../roadmap.md), milestone W.

As built, two details differ from the text below: the worker binary sits on Firecracker's
read-only tools disk at `/run/agent/worker` (not `/opt/sandbox-agent/worker`), found through
`sandbox.WorkerHost`; and the limits are sent in seconds (`agent_timeout_s`, `setup_timeout_s`).

## Summary

- **One Go module**, several binaries. No `go.work`, no module per component.
- **Binaries are split by privilege**, not by feature: the manager holds the secrets, the worker
  holds nothing, and the runtime holds only what it needs to build a sandbox.
- **The manager owns everything that has an identity**: Linear, GitHub, Git, the queue, the state
  and the session state machine.
- **The worker knows nothing about Linear, GitHub or Git.** It gets a directory and a task, and it
  returns a modified directory and a stream of events.
- **The event vocabulary mirrors Linear's agent protocol**, but the types are ours, and the worker
  never emits `response`.
- **A session is the manager's.** It spans one or more runs, and a run is one worker in one
  sandbox. Nothing stays alive while a session waits for a person.

## Binaries

| Binary | Runs where | Holds secrets | Role |
| --- | --- | --- | --- |
| `agentd` (manager) | Host | Linear, GitHub | Webhooks and polling, queue, state, sessions, prompt rendering, clone and export, sandbox start, diff and validation, push, PR, checks, overview |
| `egress-proxy` | Host | Model API keys | Key injection, allowlist, budget, cost accounting (M4). It may start as a part of `agentd`, but the target is a separate process. First version: `vsock-to-anthropic` (D41). Shelved for the development phase (D44) |
| `sandbox` | Host, then inside the sandbox | No | The layer 1 runtime (M3): namespaces, cgroup, mounts, Landlock, seccomp, then exec. The AppArmor `userns,` profile attaches to its path |
| `worker` | Inside the sandbox | No | Runs the setup script and the agent CLI, turns the agent's output into events |
| `escape` | Inside the sandbox | No | The escape tests ([escape-tests.md](../escape-tests.md)) |

Providers (`internal/sandbox/sbx`, `gvisor`, `native`) are not binaries. They are packages compiled
into `agentd` (into `runnerd` from D42 on) and selected through the registry (D30). `native` runs the `sandbox` binary, `gvisor`
runs `runsc`, and `sbx` runs `sbx`.

Hardening layers are not providers. seccomp and Landlock are applied by the `sandbox` runtime
(`internal/isolation/...`). The AppArmor profile is host policy, in `deploy/apparmor/`, and the
owner installs it.

## Packages and the import boundary

```
internal/task        Task, Event and the event kinds. Imports only the standard library.
cmd/worker           Imports internal/task, nothing else from the module.
cmd/sandbox          Imports internal/isolation/..., nothing that talks to the network.
internal/job         The manager side: Linear issue → Task, events → Linear and the overview.
```

A test (`go list -deps`) fails if `cmd/worker`, `cmd/sandbox` or `internal/task` depend on
`internal/linear`, `internal/github`, `internal/config` or `internal/gitops`. Go's `internal/`
directory only controls visibility from outside the module. This test is what enforces the
boundary inside it.

## The contract of one run

```
manager ── task (stdin, JSON) ─────────────────▶ worker, in the sandbox, cwd = workspace
manager ◀─ events (stdout, JSON lines) ──────── worker
manager ◀─ the workspace after the run ──────── (shared directory, no .git)
```

Input, one JSON object on stdin:

| Field | Meaning |
| --- | --- |
| `prompt` | The full prompt, rendered by the manager. It contains the issue's text, quoted as untrusted data. It holds no Linear identity: no IDs, no URLs, no token |
| `model` | The model name passed to the agent CLI |
| `agent` | The agent backend (`claude` first) |
| `setup` | The setup script, optional. Research runs have none (D27) |
| `limits` | Maximum turns, agent timeout, setup timeout, budget |

Output, one JSON object per line on stdout. Each has a `kind`:

| Kind | Fields | Terminal |
| --- | --- | --- |
| `thought` | `text` | No |
| `action` | `tool`, `input`, `result` (from the agent's tool calls) | No |
| `ask` | `question`, `title`, `body` (the partial change can still be reviewed) | Yes |
| `done` | `title`, `body`, `cost_usd`, `turns` | Yes |
| `fail` | `reason`, `cost_usd`, `turns` | Yes |
| `artifact` | `name`, `kind` (`screenshot`), `caption`; the file comes with the run's result (D45) | No |

The last line must be a terminal event. A run that ends without one, or exits non-zero after a
`done`, counts as `fail`. The workspace is the only other output. The manager computes the diff
itself against its clean clone, as today (D5).

What moves into the worker is what `internal/job/execute.go` does inside the sandbox today: running
the setup script, running `claude -p`, and parsing its final message (`STATUS:`, `QUESTION:`,
`TITLE:`, D26) into `ask` or `done`. The prompt rendering, the diff, `patch.Validate`, the
conflict-marker check (D24) and everything after stay in the manager.

## Sessions and runs

A Linear agent session can last for days. A run lasts minutes.

- A **session** belongs to the manager. It is persisted in `state.json` and spans one or more runs.
- A **run** is one worker in one fresh sandbox. It starts, emits events and ends.
- When a person answers a question, the manager starts a new run. The workspace is the PR branch
  (D24), and the context is rendered into the prompt by the manager, as research notes are today
  (D27). Resuming the agent CLI's own conversation (`claude --resume`) is not used: its session
  files live in the sandbox and are destroyed with it.

```
        webhook or label
pending ───────────────▶ active ──┬── run ends with ask ──────▶ awaitingInput
                          ▲       │                                 │
                          │       │                     prompt from │ a person
                          └───────┼─────────────────────────────────┘
                                  │
                                  ├── run ends with done ─▶ validate ─▶ push ─▶ PR ─▶ checks ─▶ complete
                                  │                          (rejected or failed) ──────────▶ error
                                  ├── run ends with fail, or times out ─────────────────────▶ error
                                  └── stop signal ─▶ stopping ─▶ cancel the run ─▶ stale
```

Inside `active`, the manager has its own phases (clone, sandbox, setup, agent, validate, push,
checks). It reports them as `thought` and `action` events of its own. The stream sent to Linear has
two sources, the manager and the worker. Each event is tagged with its source before it is
forwarded, and the overview shows the tag.

Linear's Stop signal (D28) cancels the run's context. The provider then kills the sandbox, and
nothing is pushed.

## Mapping to Linear

| Ours | Linear agent activity | Session status in Linear |
| --- | --- | --- |
| `thought` | `thought` | `active` |
| `action` | `action` | `active` |
| `ask` | `elicitation`, the issue assigned back (D26) | `awaitingInput` |
| `done` | nothing. The manager validates, pushes and waits for checks, **then** emits its own `response` with the PR link | `complete` |
| `fail`, or a manager error | `error` | `error` |
| Linear `prompt` | the next run's task | |
| Linear `stop` signal | cancellation | |

Linear infers the session status from the last activity, and the agent does not set it ("Linear
tracks session lifecycle automatically based on the last emitted activity", Linear's agent
interaction guide, read on 2026-11-14). So the manager's state machine only has to emit the right
activity at each transition. `state.json` stays the source of truth. The exact status each activity
type produces is not in the guide: **to verify** on a real session.

The types are ours, not the generated Linear types. That way the worker never imports
`internal/linear`, the overview, PR comments and `agentd -try` can use the same stream, and a schema
change at Linear only touches the manager's mapping.

## Why the worker never emits `response`

`done` is a claim, and the manager checks it. A `response` closes the session in Linear. If the
worker could send one, a prompt-injected agent could post "Done, PR merged" with no PR behind it.
The same goes for links: the manager only posts links it created (the PR, the overview).

## The worker's output is hostile

The worker runs with the same UID as an agent that may have been prompt-injected (and in `sbx` the
agent can become root through `sudo`). The agent can tamper with the worker process, write to its
stdout, or kill it. So the manager:

- reads at most a fixed number of bytes per line and per run, and caps the event rate;
- parses strictly, drops unknown kinds and fields, and drops everything after a terminal event;
- truncates texts, strips `@` mentions so the agent cannot ping people, and posts text as plain
  Markdown with no HTML;
- treats `cost_usd` and `turns` as self-reported. They are shown labelled as such until the egress
  proxy (M4) measures the cost itself. With `sbx`, its proxy does not report cost to us (to verify);
- never executes anything from the output. The diff is taken from the workspace against a clean
  clone, with Git hooks disabled (D5, T10).

The escape tests for this are E-PROTO in [escape-tests.md](../escape-tests.md).

## What providers must add

- **Streaming exec.** `Sandbox.Exec` returns whole buffers today. Events must reach Linear while the
  agent works, so `ExecOptions` gains `Stdin io.Reader` and `Stdout io.Writer`. With `sbx`: whether
  `sbx exec` streams stdin and stdout without a TTY is **to verify**.
- **A read-only mount for the worker binary**, at the same path in every provider
  (`/opt/sandbox-agent/worker`). With `sbx`, the extra mount `PATH:TARGET:ro` works and refuses
  writes ([docker-sandboxes.md](../docker-sandboxes.md)). The conformance suite checks that writing
  to it fails and that the host file is unchanged.

The worker is a static `linux/arm64` binary (`CGO_ENABLED=0`), so it runs in any image.

## Not decided here

- A separate host process per job. One job at a time on one board does not need it. Settled by
  D42: one runner process per machine ([runners.md](runners.md)), several runs per runner. The contract is
  files and streams, so a job runner process, or a worker on another machine, can be added later.
- Interfaces for the tracker and the forge (settled by D42: the session engine uses them). `internal/job` keeps calling Linear and GitHub directly
  until a second tracker or forge exists.
- A second agent backend. The `agent` field is there for it.
