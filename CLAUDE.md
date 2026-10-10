# sandbox-agent

Personal project: a self-hosted coding agent that turns Linear issues into draft GitHub pull
requests, running the agent inside a sandbox built from Linux primitives, with a microVM or gVisor as
a second layer. See [README.md](README.md).

Everything in this repository is in English: code, docs and commit messages.

Commit messages follow Conventional Commits (`feat(linear): …`, `fix: …`, `docs: …`), checked by the
`commit-msg` and `pre-push` hooks with commitlint (D36, `.commitlint.yaml`).

Go code is formatted by `gofmt` (`make fmt`) and reads in paragraphs: put a blank line between the
logical steps of a function, so a reader sees the structure at a glance. No linter checks this
(D37): it is a judgment call, and Claude is responsible for it in the code it writes or touches.
Keep `err := …` and its `if err != nil` together.

In conversation, answer in the language of the owner's last message: English when they write in
English, French when they switch to French, and back again. This applies only to replies; what goes
into the repository stays in English.

## Knowledge base

Detailed context lives in [`docs/`](docs/README.md). Read the relevant file before working on a
topic, and write what you learn there (not in local assistant memory). Mark anything not verified as
"to verify".

Start with [docs/roadmap.md](docs/roadmap.md) to know where things stand.

## Private notes

`docs/private/` is ignored by Git. **If it exists, read `docs/private/README.md` first**: it holds
host access, the home network layout and the other services on the board.

- Anything specific to the owner's network or machines goes there: LAN addresses, account names and
  UIDs, names of other services, paths of their data, tunnel and DNS details.
- Public docs stay generic and point to the private notes.
- Never copy private content into a tracked file, a commit message, an issue or a PR.

## Target host

Generic facts in [docs/host.md](docs/host.md); access and specifics in the private notes. The rules
below are about the board, the deployment target.

- The development account is in `kvm`, **not** `docker`. sudo asks for a password: never try to
  bypass it. Ask the owner to run the command (`! ssh -t <host> sudo …`).
- Unprivileged user namespaces are blocked by AppArmor unless the binary has a profile with `userns,`.
- big.LITTLE: cores 0-3 are Cortex-A55, 4-7 are Cortex-A76. Pin sandboxes and KVM guests to 4-7.
- The board runs other services in production. Never touch Docker, the other services, the tunnel or
  system configuration from this project.

## Workflow

Two machines (D38): an x86-64 development machine and the board (arm64), which is the deployment
target. Details of both are in the private notes.

- Development happens on the development machine: edit, build and test there. Tests that need a GPU
  also run there; the board has none.
- Go is installed in the home directory (`~/.local/go`) on each machine, not system-wide.
  `make build` builds `bin/agentd` and `bin/sandbox` for the machine it runs on, `make cross` builds
  linux/amd64 and linux/arm64 (the arm64 binaries go to `bin/linux-arm64/`), `make test` runs the
  tests.
- The board keeps its own checkout in `~/sandbox-agent`. How builds are deployed there is not
  settled yet (roadmap, M0b).

## Rules

- No secret ever enters the sandbox. API keys and GitHub tokens live on the host only. One
  exception, for the development phase only: the Claude subscription token (D43). GitHub and
  Linear credentials never enter it.
- Every isolation feature comes with an escape test ([docs/escape-tests.md](docs/escape-tests.md)).
- Record decisions in [docs/decisions.md](docs/decisions.md); longer records go in `docs/decision/`
  with a short entry in `decisions.md` that links to them.
- Nothing in this repository mentions an employer, a job application or a company the project is
  meant for.
