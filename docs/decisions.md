# Decisions

Short records: what was decided, why, and when. Newest at the bottom. Change a decision by adding a
new entry that supersedes the old one.

## D1. Go, cross-compiled, `CGO_ENABLED=0` (2026-11-11)

Go is the usual language for this kind of infrastructure (runc, gVisor, Firecracker's Go SDK,
containerd). A static binary built on the Mac and copied to the board avoids installing a toolchain on
a production host. Consequence: no cgo libraries (`libseccomp-golang`); use pure-Go alternatives.

## D2. A purpose-built runtime instead of Docker or runc (2026-11-11)

The point of the project is to apply and demonstrate isolation techniques explicitly, with a test for
each. Docker on this host is also root-equivalent and shared with other services. A production system
would likely build on an existing runtime; the write-up says so.

## D3. Agent-agnostic sandbox (2026-11-11)

The sandbox runs any agent through a small adapter. Claude Code is the first backend;
others can be added without touching the sandbox.

## D4. No secrets in the sandbox (2026-11-11)

API keys live in the egress proxy on the host, which injects them. GitHub and Linear credentials stay
in the worker. A compromised agent has nothing to steal.

## D5. The host publishes the PR from a validated patch (2026-11-11)

The sandbox never sees `.git` and never pushes. Its output is a patch, validated as hostile input and
applied to a clean clone. This blocks Git hook and config attacks on the host and keeps GitHub
credentials out.

## D6. Egress through a Unix socket (vsock for the microVM), no veth (2026-11-11)

Only loopback inside the sandbox. No routing, no nftables, no bridge to get wrong, and it works
without root.

## D7. Rootless development through an AppArmor `userns` profile (2026-11-11)

Ubuntu blocks unprivileged user namespaces by default. A named profile for the runtime binary allows
them, so development and tests run as the development account without sudo. Root is needed once to install the
profile.

## D8. Pin sandboxes to the A76 cores (2026-11-11)

Cores 4-7 are the fast ones, and pinning avoids vCPU migration across core types under KVM. The A55
cores stay for the other services.

## D9. Trigger on a label, not on every issue (2026-11-11)

Controls cost and limits who can start a job.

## D10. Develop on the board, Go in the home directory (2026-11-12)

Supersedes the "built on the Mac" part of D1. The repository lives on the board and is edited there
(VS Code Remote-SSH), so there is a single working copy and tests run where the kernel features are.
Go is installed in the development account's home, not system-wide, so the toolchain stays out of
the rest of the production host. `CGO_ENABLED=0` and the static binary from D1 still hold.

## D11. Docker Sandboxes first, the purpose-built runtime second (2026-11-13)

Partly supersedes D2. The first goal is the Linear-to-PR loop running on the real target, not the
runtime. Docker Sandboxes (`sbx`) gives a KVM microVM per job, a host proxy that injects the API key
(compatible with D4) and deny-by-default egress, without Docker Engine or the `docker` group. Its
package installs no system daemon (see [docker-sandboxes.md](docker-sandboxes.md)). The runtime sits
behind an interface in the worker, so the purpose-built layer 1, Firecracker and gVisor come next as
alternative runtimes, compared on the same escape tests and measurements. Costs: a Docker account,
about 110 desktop libraries pulled in by `gnome-keyring`, and a closed component in the critical path.

## D12. A GitHub App for the host (2026-11-13)

Settles the GitHub question in open-questions.md. A private App, installed on the target repository
only, with `contents: write`, `pull_requests: write` and `metadata: read`. The private key stays on
the host; the worker mints an installation token per job (one hour). PRs are authored by the bot,
access is revoked by uninstalling the App, and nothing depends on a personal account. No `workflows`
permission, so the agent's patch cannot change CI (the validator also rejects `.github/` changes).

## D13. No fixed deadline (2026-11-13)

Supersedes the Sunday 2026-11-15 target in the roadmap. Milestones keep their order; scope is no
longer cut to meet a date.

## D14. Open egress first, except the host, the LAN and loopback (2026-11-13)

To get a working service first, the global `sbx` policy is `allow-all`, so the agent can install
toolchains and dependencies from anywhere and reach `github.com`. Denied rules, checked from inside
a VM: `127.0.0.0/8`, `::1/128`, `localhost`, `0.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`, `169.254.0.0/16`, `100.64.0.0/10`, `fc00::/7`, `fe80::/10`. The board's own LAN
address is refused (403), including for connections that bypass the proxy variables. Accepted risk:
an agent manipulated by an issue can exfiltrate the repository it works on; it holds no credential
(GitHub and Anthropic credentials are injected by the host or absent). Tightening is planned once the
service is useful: deny GitHub writes, then move to an allowlist.

## D15. Linear polling before webhooks (2026-11-13)

`agentd` polls Linear every 30 s for open issues carrying the trigger label, and removes the label
when it claims an issue (adding it again retries). No inbound endpoint, no tunnel rule, no webhook
secret. Webhooks (M6 receiver) can replace polling later without changing the worker.

## D16. Sonnet by default, Opus on request (2026-11-13)

Claude Code's default model in the `sbx` image is Opus 5.5. `agentd` passes `--model` explicitly:
`claude-sonnet-5-5` for issues labelled `agent`, `claude-opus-5-5` for issues labelled
`agent-opus`. A failed run comments with a suggestion to retry with `agent-opus`; there is no
automatic escalation, so unclear issues do not cost twice. On the Bun sample task: Opus $0.13 in
5 turns, Sonnet $0.06 in 4 turns, equivalent patches. Haiku stays available through `AGENT_MODEL`;
not tried on real issues yet.

## D17. Host credentials in `secrets/` at the repository root (2026-11-13)

Easier for the owner to manage than `~/.config`. `secrets/` is ignored by Git and holds one file per
credential (`linear-api-key`, `github-app.pem`) plus `config.env` for settings that name private
things (repository, App IDs) and the repository-specific `setup.sh`. `agentd` refuses to start if
the directory is not `0700` or a file it reads is accessible to group or others. The Anthropic key
stays in the `sbx` secret store, which injects it through its proxy.

## D18. Agent commits authored by the reviewing operator (2026-11-13)

The target repository deploys previews on a platform that only builds commits whose author is a
team member, so commits authored by the App's identity were blocked. `COMMIT_AUTHOR_NAME` and
`COMMIT_AUTHOR_EMAIL` (in `secrets/config.env`) set the author and committer to the operator who
reviews and merges; a `Co-authored-by: sandbox-agent` trailer keeps the agent's part visible, and
the push and the PR are still made by the App. Alternatives not taken: a paid seat for a bot
account, or relaxing the platform's author check.

## D19. Read-only CI and deployment permissions for the App (2026-11-13)

Adds to D12: `Commit statuses`, `Checks` and `Deployments`, all read-only, so the host can see
whether CI and preview deployments of agent PRs succeed (and later feed failures back to the
agent). Still no write access beyond `contents` and `pull_requests`, and no `workflows`.

## D20. One label per model (2026-11-13)

Supersedes the `agent-opus` part of D16. `agent` runs with the default model (`AGENT_MODEL`,
Sonnet 5.5). `MODEL_LABELS` maps explicit labels to models, weakest first: `agent-haiku`,
`agent-sonnet`, `agent-opus`, `agent-fable` (Fable 5.2). With several model labels on one issue,
the strongest wins. A failed run suggests `RETRY_LABEL` (`agent-opus`). All four models answered
from inside an `sbx` VM with Claude Code 2.1.322; a minimal call cost $0.002 (Haiku), $0.034
(Sonnet), $0.070 (Opus) and $0.149 (Fable).

## D21. Linear as an OAuth application, not a personal key (2026-11-13)

Same idea as the GitHub App (D12). An OAuth application with client credentials gives tokens with
the `app` actor: comments are created as the application instead of the owner, the application takes
no seat, and it can be revoked independently. `agentd` mints a token from `secrets/linear-client-id`
and `secrets/linear-client-secret` (scope `read,write`), caches it in the state directory (0600),
renews it a day before its 30-day expiry and once on a 401. The personal API key remains a fallback
while those files are empty. Next level, not done: Linear's agent mode (assign or mention the
application, live activities in the issue), which needs agent session webhooks and therefore a public
endpoint.

## D22. HTTP server behind the tunnel: Access for people, signatures for webhooks (2026-11-13)

`agentd` serves on loopback only (`127.0.0.1:8787`, refused otherwise), exposed by a dedicated
tunnel hostname rule placed before the wildcard. Two kinds of clients:

- People (`/`, `/api/jobs`): a Cloudflare Access application on the hostname. `agentd` also verifies
  the `Cf-Access-Jwt-Assertion` JWT (RS256, team certs, issuer, audience, expiry), so an edge
  misconfiguration does not expose the overview. Requests without `Cf-Connecting-Ip` come from the
  host itself (the edge always adds it) and are allowed.
- Webhooks (`/webhooks/*`): a more specific Access application in Bypass, because Linear and GitHub
  cannot authenticate to Access. `agentd` checks the HMAC-SHA256 signature of the raw body
  (`Linear-Signature`), rejects timestamps more than a minute off, and ignores repeated
  `Linear-Delivery` IDs. A valid issue event carrying a trigger label wakes the worker at once;
  polling stays as the fallback.

Also: a stop signal lets the running job finish (the unit allows 70 min) instead of killing its
sandbox; a restart during a job killed one on 2026-11-13.

## D23. One state file for all jobs (2026-11-13)

Replaces the per-job `status.json`. `state.json` in the state directory lists every job (stage,
model, times, cost, PR, error, directory). Writes take an exclusive `flock` on `state.json.lock`
and replace the file atomically, so the service and a concurrent `agentd -try` never lose
updates; readers take a shared lock. Older per-job files are imported once and removed. SQLite was
not needed at this volume and would bring cgo or a large pure-Go dependency (D1). After each job
the clone and the workspace are deleted (a few GB with `node_modules`); the job directory keeps
the prompt, the logs and the patch.
