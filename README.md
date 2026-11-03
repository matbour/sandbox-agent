# sandbox-agent

A self-hosted coding agent that turns Linear issues into GitHub pull requests, running untrusted code
inside a sandbox built from Linux primitives.

Status: work in progress. The target architecture is one manager scheduling agent runs onto
Firecracker microVMs on several machines of different architectures
([docs/decision/runners.md](docs/decision/runners.md)).

## Goal

When an issue is labelled or delegated to the agent in Linear, the host clones the repository,
starts an isolated sandbox, and runs a coding agent (Claude Code today, other agent CLIs later) inside it. The
host takes the changed working tree, validates it, and opens the pull request itself. Inside the
sandbox there is no Linear, GitHub or Git, only a task and the workspace
([docs/decision/protocol.md](docs/decision/protocol.md)).

## Security principles

- **Everything inside the sandbox is hostile**: the issue text (prompt injection), the repository code
  and its dependencies, and the agent itself.
- **No secrets in the sandbox**: the agent talks to an egress proxy on the host, which adds the API
  key. GitHub credentials stay on the host.
- **Deny by default**: no network except the proxy, no access to the LAN, bounded CPU, memory, PIDs
  and disk.
- **Development phase** (D43, D44): two of these principles are relaxed on purpose until the next
  version. The guest holds the Claude subscription token, and it reaches the whole network. Only
  the owner's workspace and repositories are configured meanwhile.
- **Defense in depth**: namespaces, cgroups v2, seccomp-bpf, Landlock and dropped capabilities first,
  then a microVM (Firecracker) or gVisor as a second layer.
- **Every protection has a test** that attempts the corresponding escape.

## Target host

Orange Pi 5 Plus (RK3588, arm64, 8 cores, 16 GB), Ubuntu 26.04, kernel 7.0, KVM available. The board
also runs other services, which makes isolation from the host and the LAN a hard requirement.

## Documentation

Design, threat model, escape tests and roadmap: [docs/](docs/README.md).

## Running agentd

`agentd` polls Linear for issues labelled `agent` (default model) or `agent-<model>` (`agent-haiku`,
`agent-sonnet`, `agent-opus`, `agent-fable`), runs Claude Code on each one inside a Docker
Sandboxes microVM, and opens a draft pull request. Setup:

1. Install and configure `sbx` (see [docs/docker-sandboxes.md](docs/docker-sandboxes.md)).
2. Fill in `secrets/` at the repository root (ignored by Git, `chmod 700`, files `chmod 600`;
   `agentd` refuses to start otherwise):
   - `config.env`: settings, from `deploy/examples/config.env`;
   - `linear-client-id` and `linear-client-secret`: a Linear OAuth application with client
     credentials enabled (comments appear as the application);
   - `github-app.pem`: the GitHub App private key;
   - `setup.sh` and `setup.notes.md` (optional): from `deploy/examples/`.
3. `bin/agentd -check` verifies Linear and GitHub access and lists matching issues.
4. `bin/agentd -once` runs what is labelled now; without `-once` it keeps polling. A systemd user
   unit is in `deploy/systemd/`.

In Linear, **mention** the agent with a question to get research notes (it reads the code and
the docs, changes nothing), and **assign** (delegate) the issue to it to have it implemented.

To update a pull request opened by the agent, mention it in a comment on the Linear issue
(`@<agent name> please also handle the empty state`): it merges the latest `main`, reads your
comment, the PR reviews and the failed checks, and pushes to the same branch.

After pushing, `agentd` waits for the pull request's checks, runs up to two fix passes if they
fail, then marks the PR ready for review (Linear moves the issue to In Review). If the agent needs
a decision, or the checks keep failing, the PR stays a draft and the issue is assigned back.

Follow the jobs:

```bash
bin/agentd -status          # service, running sandboxes, recent jobs with stage, time, cost, PR
bin/agentd -show ENG-123    # one job: stage, PR, error or the agent's last message, files
watch -n 5 bin/agentd -status
```

The same overview is served at `http://127.0.0.1:8787/` (and through the tunnel, behind
Cloudflare Access; see docs/decisions.md D22). All jobs are in
`~/.local/state/sandbox-agent/state.json` (one file, written atomically under a lock); each job
directory under `jobs/` keeps the prompt, the logs and the patch.

Try the sandbox part on a local repository, without Linear or GitHub:

```bash
bin/agentd -try ~/some/repo -task "Describe the change" [-model claude-opus-5-5]
```

## Runners

Runs can go to `runnerd` processes, on this machine or others (D42, D48). On the manager:

```bash
bin/agentd -add-runner desktop -token-out secrets/runner-token   # the token goes to the file only
RUNNER_ADDR=127.0.0.1:8788 bin/agentd ...                         # serve the runners' API
```

On the runner's machine (the token file copied there, mode 600):

```bash
bin/runnerd -manager http://127.0.0.1:8788 -token-file secrets/runner-token -name desktop \
    -cpus 4 -memory 8192 -cpuset 4-7
```

Plain HTTP is only accepted to a loopback address; a runner on another machine needs HTTPS in
front of the manager (to set up, roadmap R5).

## Development

```bash
make build    # build bin/agentd and bin/sandbox for this machine (static)
make cross    # build for linux/amd64 and linux/arm64 (bin/linux-<arch>/ for the other one)
make test     # run the tests
```

Development happens on an x86-64 machine; the target host above runs arm64.
