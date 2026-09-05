# sandbox-agent

A self-hosted coding agent that turns Linear issues into GitHub pull requests, running untrusted code
inside a sandbox built from Linux primitives.

Status: work in progress.

## Goal

When an issue is created in Linear, a signed webhook reaches the host. The host clones the repository,
starts an isolated sandbox, and runs a coding agent (Claude Code) inside it. The
sandbox returns a diff; the host, not the sandbox, opens the pull request.

## Security principles

- **Everything inside the sandbox is hostile**: the issue text (prompt injection), the repository code
  and its dependencies, and the agent itself.
- **No secrets in the sandbox**: the agent talks to an egress proxy on the host, which adds the API
  key. GitHub credentials stay on the host.
- **Deny by default**: no network except the proxy, no access to the LAN, bounded CPU, memory, PIDs
  and disk.
- **Defense in depth**: namespaces, cgroups v2, seccomp-bpf, Landlock and dropped capabilities first,
  then a microVM (Firecracker) or gVisor as a second layer.
- **Every protection has a test** that attempts the corresponding escape.

## Target host

Orange Pi 5 Plus (RK3588, arm64, 8 cores, 16 GB), Ubuntu 26.04, kernel 7.0, KVM available. The board
also runs other services, which makes isolation from the host and the LAN a hard requirement.

## Documentation

Design, threat model, escape tests and roadmap: [docs/](docs/README.md).

## Running the worker

`agentd` polls Linear for issues labelled `agent` (default model) or `agent-<model>` (`agent-haiku`,
`agent-sonnet`, `agent-opus`, `agent-fable`), runs Claude Code on each one inside a Docker
Sandboxes microVM, and opens a draft pull request. Setup:

1. Install and configure `sbx` (see [docs/docker-sandboxes.md](docs/docker-sandboxes.md)).
2. Fill in `secrets/` at the repository root (ignored by Git, `chmod 700`, files `chmod 600`;
   `agentd` refuses to start otherwise):
   - `config.env`: settings, from `deploy/examples/config.env`;
   - `linear-client-id` and `linear-client-secret`: a Linear OAuth application with client
     credentials enabled (comments appear as the application), or `linear-api-key`: a personal key;
   - `github-app.pem`: the GitHub App private key;
   - `setup.sh` and `setup.notes.md` (optional): from `deploy/examples/`.
3. `bin/agentd -check` verifies Linear and GitHub access and lists matching issues.
4. `bin/agentd -once` runs what is labelled now; without `-once` it keeps polling. A systemd user
   unit is in `deploy/systemd/`.

To update a pull request opened by the agent, mention it in a comment on the Linear issue
(`@Sandbox Agent please also handle the empty state`): it merges the latest `main`, reads your
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
Cloudflare Access; see docs/decisions.md D22). All jobs are in `~/.local/state/sandbox-agent/state.json` (one file, written atomically under a
lock); each job directory under `jobs/` keeps the prompt, the logs and the patch.

Try the sandbox part on a local repository, without Linear or GitHub:

```bash
bin/agentd -try ~/some/repo -task "Describe the change" [-model claude-opus-5-5]
```

## Development

```bash
make build    # build bin/sandbox (linux/arm64, static)
make test     # run the tests
```

The repository is checked out and built on the target host itself.
