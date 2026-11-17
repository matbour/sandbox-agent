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
credential (`linear-api-key`, `github-app.pem`; the Linear key was later replaced by the OAuth application, D21 and D34) plus `config.env` for settings that name private
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
renews it a day before its 30-day expiry and once on a 401. The personal API key remained a fallback
while those files were empty, until D34 removed it. Next level, not done: Linear's agent mode (assign or mention the
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

## D24. Mention the agent in a Linear comment to update its pull request (2026-11-13)

A comment that mentions the agent's Linear name (the application's name, matched loosely) on an issue
where the agent already opened a pull request starts an update:

1. The host clones the PR branch (blobless, full history) and merges the latest base branch into
   it. That content comes from the base branch and is trusted. Conflicts are committed with their
   markers to a local commit only.
2. The agent gets the comment, the PR conversation, reviews and inline comments, the failed checks
   and the list of conflicted files, all marked as untrusted data.
3. Only the agent's own changes are validated (against the merged tree), plus a check that no
   conflict marker remains. Conflict resolutions amend the local merge commit, so markers never
   reach the branch; other changes go in their own commit.
4. Fast-forward push to the same branch (no force), only for `agent/*` branches of open PRs;
   a comment on the PR and a reply in the Linear thread.

Mentions are found by polling recent comments (the webhook only wakes the worker). Comments older
than the first run are ignored, handled comment IDs are kept in `state.json`, and the agent's own
comments never trigger it. A model label written in the comment (`agent-opus`) overrides the
model of the original job.

## D25. Linear agent mode: mention or delegate the application (2026-11-13)

The application token is requested with `app:mentionable` and `app:assignable` on top of
`read,write` (client credentials accept them), so the application can be mentioned through the `@`
menu or delegated an issue. Linear then opens an agent session and sends an `AgentSessionEvent`
(`created`, or `prompted` for a follow-up message) to the OAuth application's webhook.

`agentd` answers the webhook at once, stores the session in `state.json`, and posts a `thought`
activity within the 10 s Linear allows. The worker then updates the issue's open agent pull request
if there is one (D24), or opens a new one. Progress is shown as activities in the session (one
thought per stage, then a `response` or an `error`), with links to the overview and the PR. The
plain-text mention of D24 stays as a fallback; a comment that opened a session is not taken twice.
`secrets/linear-webhook-secret` holds one secret per line, since the workspace webhook and the
application's webhook are signed with different secrets. Raw session payloads are kept under
`sessions/` in the state directory while the format is being learnt.

## D26. After the push: checks, fix passes, then ready for review or handed back (2026-11-13)

The agent ends with `STATUS: ready` or `STATUS: needs-decision` plus a `QUESTION:`. A missing
status counts as not ready. After pushing, `agentd`:

- if the agent needs a decision: leaves the pull request as a draft, assigns the issue back to the
  person who asked (session creator, comment author, or the issue's assignee or creator for a
  label), and posts the question (an `elicitation` activity in an agent session);
- otherwise waits for the head commit's checks and statuses (`CI_TIMEOUT`, 25 min; checks named in
  `IGNORE_CHECKS` are skipped, `claude-review` by default). While some fail, it runs a fix pass
  with the failures (same mechanism as D24), up to `CI_FIX_ATTEMPTS` (2), then hands back;
- when all pass, marks the pull request ready for review (GitHub GraphQL
  `markPullRequestReadyForReview`, which REST lacks). Linear's GitHub integration for the team
  already moves the issue to In Review on that event, and to Done on merge; `agentd` does not set
  Linear states itself.

The worker waits for checks inline, so jobs queue behind it; acceptable at the current volume.
Check logs (Vercel in particular) are not readable with the App's permissions: a fix pass gets
the check names and summaries and must reproduce the failure in the sandbox.

## D27. Mention to ask, delegate to do (2026-11-13)

Linear opens an agent session both when the agent is mentioned and when an issue is delegated to
it (with an automatic comment, "This thread is for an agent session with …"); the issue's
`delegate` field tells them apart. Routing:

- An open agent pull request on the issue: update it (D24, D26), whatever the trigger.
- Delegated, no pull request: open one (D26).
- Mentioned or messaged, no pull request: research. The agent reads the code, and the web when
  the answer depends on outside facts, in a sandbox without the setup script; its changes are
  discarded. The answer is posted as notes in the session, saved in the job directory, and given
  as context to later runs on the same issue (for example when the person then delegates it).

Webhook events other than issues with an agent label, comments mentioning the agent, and agent
sessions are verified and ignored. Not handled yet: the session's stop signal, undelegation
during a job, permission changes and the application's revocation.

## D28. Stop signal, and "no changes" is a decision, not a failure (2026-11-13)

- Linear's Stop button arrives as a `prompted` event with `agentActivity.signal = "stop"` (and the
  text "stop" as content). It cancels the session's running job (the agent and its sandbox stop,
  nothing is pushed), drops its queued events, and answers "Stopped." Before this, "stop" was
  taken as a new request and started a job.
- A delegated job where the agent changes nothing (for example the content lives in a CMS, not in
  the repository) now ends as "needs a decision": the agent's explanation is posted as a question
  and the issue is assigned back, instead of a failure.

## D29. Drop the VM's page cache (2026-11-13)

A job VM filled its memory cap (6.9 GB on the host for a 6 GiB sandbox) while its processes used
443 MB: the rest was the guest's page cache, filled by `bun install` and by reading
`node_modules`. A VM does not return cached pages to the host, and those files are already in
the host's own cache. `agentd` now starts a root loop in each VM that drops the page cache every
20 s, and drops it once after the setup script. Dropped pages go back to the host through free
page reporting: measured 2.3 GB down to 0.8 GB within a few seconds after 1 GB of file I/O. The
memory cap stays as a ceiling for build peaks; the real peak is still to be measured.

## D30. Sandbox providers behind one interface (2026-11-13)

`internal/sandbox` defines `Provider` (`Prepare`, `Start`) and `Sandbox` (`Exec`, `WorkDir`,
`Close`), plus the optional `CacheTrimmer`. Each technology is a package under it; Docker Sandboxes
moved to `backends/sbx` unchanged. `SANDBOX_PROVIDER` picks one (`sbx` by default); the
registry is explicit in `cmd/agentd`. Jobs record the provider in `state.json`.

The contract is written in the package doc and checked by `internal/sandbox/sandboxtest`, a
conformance suite that starts real sandboxes (`SANDBOX_IT=1`): shared workspace as working
directory, unprivileged default user, environment passing, cancellation, no API key inside, host
LAN address and cloud metadata unreachable, nothing left after `Close`. sbx passes it in 12.5 s
(7 s to start). The same suite, then the escape tests, will compare providers.

What sbx gives for free and other providers must rebuild: the image (Claude Code, Git, Python),
key injection through a host proxy (the egress proxy of M4), and the network policy.

## D31. go-github and ghinstallation instead of a hand-written GitHub client (2026-11-13)

`internal/github` keeps its interface but now uses `github.com/google/go-github/v93` (v93.0.0)
for REST and `github.com/bradleyfalzon/ghinstallation/v2` (v2.19.0) to sign the App JWT; the
hand-written JWT and HTTP code is gone. Gains: typed responses, and pagination of comments,
reviews, review comments and check runs (the old client read only the first 100 of each). Leaving
draft has no REST endpoint and stays a GraphQL request sent through go-github's client. These are
the project's first third-party dependencies (plus golang-jwt and go-querystring, and go-github
v88 required by ghinstallation); all pure Go, versions pinned in go.mod and go.sum. Tested against
a fake API and read-only against the real repository.

## D32. A Linear client generated by genqlient (2026-11-13)

Linear's official SDK is TypeScript only. `internal/linear` now uses genqlient (v0.8.1, run as a
Go tool): the operations are in `queries.graphql`, checked against Linear's schema
(`schema.graphql`, from the official SDK repository, commit in `schema.version`) and compiled into
`generated.go`. A field that does not exist now fails `make generate`, not a job (two such errors
happened on 2026-11-13 with hand-written queries). The package keeps its domain types (`Issue`,
`Comment`) and its OAuth layer, now an HTTP transport that also retries once on 401.

Choices: mutations take explicit variables instead of whole input objects, because generated
input structs send unset fields as `null`, which could clear data such as a session's external
links; `IssueFilter` is bound to a map so its recursive filter types are not generated (8,400
lines down to 1,400). genqlient needs `golang.org/x/tools` v0.38.0 to build with Go 1.27.
`make generate` regenerates, `make linear-schema` refreshes the schema.

## D33. Manager, worker and an event protocol modelled on Linear's agent sessions (2026-11-14)

Full record: [decision/protocol.md](decision/protocol.md). In short:

- One Go module, several binaries split by privilege: `agentd` (the manager, with the Linear and
  GitHub credentials), `egress-proxy` (model keys, M4), `sandbox` (the layer 1 runtime), `worker`
  and `escape` (inside the sandbox, no secrets). Providers stay packages inside `agentd` (D30);
  seccomp, Landlock and AppArmor are hardening layers, not providers.
- The worker knows nothing about Linear, GitHub or Git. It reads a task (rendered prompt, model,
  setup script, limits) on stdin, works in the workspace, and writes events on stdout: `thought`,
  `action`, then one terminal `ask`, `done` or `fail`. The manager diffs and validates the
  workspace as before (D5).
- The vocabulary mirrors Linear's agent activities, with our own types in `internal/task`. The
  worker never emits `response`: `done` is a claim that the manager checks before it posts its own
  response with the PR link.
- The session state machine is the manager's. A session spans several runs, and each run is a fresh
  sandbox. Nothing stays alive while a session waits for a person.
- Worker output is hostile input: size and rate limits, strict parsing, mentions stripped,
  self-reported cost labelled as such (E-PROTO tests).
- An import test keeps `cmd/worker`, `cmd/sandbox` and `internal/task` free of `linear`, `github`,
  `config` and `gitops`.

Terminology: in D4 to D32, "worker" means the host-side job runner, which is now called the manager
(`agentd`). From D33 on, "worker" means the process inside the sandbox.

## D34. No Linear personal API key (2026-11-15)

The OAuth application (D21) is the only way `agentd` authenticates to Linear. The fallback to a
personal API key (`secrets/linear-api-key`, `LINEAR_API_KEY`) is gone: agent sessions and
activities need the application token anyway (personal keys cannot publish agent activities), the
key was tied to the owner's account, and one mode means one code path. `agentd` refuses to start
without `secrets/linear-client-id` and `secrets/linear-client-secret`.

## D35. GitHub Actions: build, test and lint (2026-11-15)

`.github/workflows/ci.yml` runs three jobs on every push to `main` and every pull request. Build and
Test call `make build` and `make test`, the same targets as the Git hooks. Lint uses the official
`golangci/golangci-lint-action` at the golangci-lint version the Makefile pins, so `make lint` and
CI agree; the two must change together. The runners are x86-64: `make build` cross-compiles for
linux/arm64, and the tests that start real sandboxes skip without `SANDBOX_IT=1`. Third-party
actions are pinned to a commit SHA, with the tag in a comment. The token is read-only, and checkout
does not keep it in `.git/config`. Every workflow, job and step has a name.

Annotations on the offending lines come from problem matchers: setup-go registers a Go matcher that
reads compiler, vet and golangci-lint output. GitHub shows at most 10 error annotations per step, so
with many issues only the first ones are annotated and the log lists the rest. Annotating every line
would need the Checks API (a tool of our own, or reviewdog); not worth maintaining for this project.
`.golangci.yml` lifts golangci-lint's defaults (3 issues of a kind, 50 per linter), which hid half
of the issues.

## D36. Conventional Commits, checked by Git hooks (2026-11-15)

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org):
`<type>[(scope)][!]: <description>`, with the types `build`, `chore`, `ci`, `docs`, `feat`, `fix`,
`perf`, `refactor`, `revert`, `style` and `test`, a lowercase scope (the package or area: `linear`,
`job`, `sbx`, `web`…), a lowercase imperative description without a final period, and a subject of
at most 72 characters. `!` marks a breaking change, such as a removed setting. The whole history
was rewritten to this format on 2026-11-15 (subjects only; bodies, authors and dates unchanged).

The rules are checked by [commitlint](https://github.com/conventionalcommit/commitlint) (Go),
configured in `.commitlint.yaml` (2026-11-16; it replaced a hand-written regex). It runs through
`go run` at a pinned version, not as a go.mod tool: as a tool, it bumped `golang.org/x/*` in our own
module graph. Two differences from the regex: a description that starts with a capital letter is no
longer refused (commitlint can only require the whole description in lower case, which refuses
names like `Firecracker`), and commitlint's parser reads a body line that starts with `word: ` as
the start of the footer and refuses the message ("at least one empty line required after body"):
rewrap the line. Lefthook runs commitlint twice: `commit-msg` on the message being written, and
`pre-push` on every commit about to be pushed (`scripts/check-commit-messages.sh` picks those
commits), which also catches messages made with `--no-verify` or by a rebase. `fixup!`, `squash!` and `amend!` commits pass locally and are refused on push. Hooks only
run where `make hooks` was run; CI does not check messages.

## D37. gofmt, and blank lines by judgment (2026-11-15)

Dense Go code was hard to read. Go's only formatting standard is `gofmt`, which keeps the blank
lines it finds but never adds any; the official style guides leave blank lines inside functions to
the author ("write in paragraphs"). We first enabled `wsl_v5`, golangci-lint's whitespace linter,
which turns that into fixed rules, and applied it to the whole code base (921 blank lines). It is
opinionated and uncommon in Go projects, so the linter was dropped the same day: the code keeps
`gofmt` as its only formatter, and blank lines between logical steps are a judgment call, which
Claude owns in the code it writes or touches (CLAUDE.md). The lines added by `wsl` stay; spots
where they read as too much get thinned out when that code is next touched.

## D38. Develop on an x86-64 machine, deploy to the board (2026-11-15)

Supersedes D10's "develop on the board". The repository is now edited, built and tested on a
separate x86-64 development machine; the board stays the deployment target. Tests that need a GPU
also run on the development machine, which the board does not have. Go is still installed in the
home directory (`~/.local/go`) on each machine, and `CGO_ENABLED=0` with static binaries (D1)
still holds.

`make build` now targets the architecture it runs on and writes to `bin/`; another architecture
goes to `bin/linux-<arch>/`, so a cross-compiled binary never replaces a native one. `make cross`
builds linux/amd64 and linux/arm64, and CI runs it, so code that only compiles on one architecture
(syscall numbers in seccomp filters, for example) fails in CI. How the binaries reach the board is
not decided yet (roadmap, M0b).

## D39. Firecracker work starts on the development machine (2026-11-16)

The Firecracker microVM is built and run on the x86-64 development machine first. It already boots
a test guest there (M0b), while the board cannot be reached over SSH yet and its big.LITTLE CPU may
not run Firecracker at all. The guest kernel and disk images are built for x86-64 first; the build
must take the architecture as a parameter so the same steps produce arm64 images for the board
later. The board spike (vCPUs pinned to cores 4-7) is postponed, not dropped: the board stays the
deployment target (D38), and layer 2 is decided only once it has run there.

## D40. Egress proxy: a separate binary, exact paths, Go standard library (2026-11-16)

The first version of the M4 proxy is `cmd/egress-proxy`, a separate process from `agentd` (as
planned in [decision/protocol.md](decision/protocol.md)): it is the only process that reads the
model key, and it is reachable from the sandbox. It is `httputil.ReverseProxy` with a check in
front: origin-form requests only (no `CONNECT`, no absolute URL), a canonical path with no encoded
characters, and an exact `METHOD /path` allowlist per route (`POST /v1/messages` and
`POST /v1/messages/count_tokens` for Anthropic). Credential headers from the sandbox are dropped
and the route's key is set; upstream errors become a bare 502. Rejected calls are logged, which is
how a call Claude Code needs shows up.

The sandbox side is `cmd/forwarder`, a byte relay from loopback TCP to vsock or a Unix socket, with
half-close in each direction. vsock is dialled with `golang.org/x/sys/unix` and wrapped in an
`*os.File` rather than taking a dependency (`mdlayher/vsock`): the guest only dials, which is a few
lines.

## D41. Commands named after what they connect (2026-11-16)

Supersedes the names in D40. `egress-proxy` and `forwarder` did not say which side of the VM they
run on or what they connect, so the commands are named source-to-destination:
`loopback-to-vsock` (in the guest: loopback TCP to a vsock port on the host) and
`vsock-to-anthropic` (on the host: the Unix socket Firecracker maps the vsock port to, to the
Anthropic API with the key added). The relay lost its Unix socket mode, which nothing used; the
layer 1 sandbox gets its own relay when it needs one. The Go packages keep their role names
(`internal/forward`, `internal/egress`), and a second provider route will either be a new command
or a rename of `vsock-to-anthropic`.


## D42. One manager, several runners (2026-11-20)

Full record: [decision/runners.md](decision/runners.md). Proposed, not implemented (milestone R).
In short:

- Jobs must run on several machines at once (the board and the development machine first). `agentd`
  stays the single manager: sources, sessions, scheduling, Linear and GitHub. A new `runnerd` runs
  on each machine that runs jobs, holds the sandbox providers (D30 moves there) and the egress proxy,
  and never holds Linear or GitHub credentials.
- Runners dial out to the manager over HTTPS with a per-runner token, lease runs, stream the D33
  events and return a change set computed against the base tree the manager served without `.git`.
  The manager applies it to its own clean clone and validates it as before (D5).
- A session is made of durable, idempotent steps in the manager, so a restart resumes it and waiting
  for CI holds no runner. A run is one worker in one sandbox on one runner.
- One machine runs the same design over loopback, with no in-process shortcut.
- The worker, the task, the events and the session state machine of D33 are unchanged. The two
  points D33 left open are settled: one runner process per machine, and `Tracker` and `Forge`
  interfaces in the session engine.

## D43. Development phase: the Claude subscription token enters the guest (2026-11-20)

An exception to "no secret in the sandbox", for the development phase only. Runs authenticate
Claude Code with the owner's subscription through `CLAUDE_CODE_OAUTH_TOKEN` (made by
`claude setup-token`, valid one year), the documented route for headless use of the unmodified
binary. Having a host proxy hold the token and inject it was ruled out: the terms forbid
intermediating subscription credentials, and the system will later serve users who must bring
their own key or subscription.

Conditions:

- The token is a project secret on the manager, named in the project's `secret_env`, sent to the
  runner in the run spec, set in the guest's environment, and never logged or written to disk by
  the runner.
- Only the owner's Linear workspace and repositories are configured while it is in use.
- The manager rejects any change set, event or PR text that contains the value. Screenshots cannot
  be scanned (D45), so they are published only to private places.
- E-SEC is reported as "expected to fail: development token present", not skipped.
- The token is revoked and replaced at the end of the phase, or after any suspicious run (how to
  revoke one is to verify).

With an open network (D44), any run can exfiltrate the token. That is the accepted cost of the
phase.

## D44. Development phase: open network, egress proxy shelved (2026-11-20)

Supersedes, for the development phase, D6 (no network device) and the M4 plan. Network limits are
for a later version.

- Each microVM gets a tap device and reaches the internet through NAT on the runner host. No
  filtering: the guest reaches the internet, the LAN and the host. Claude Code talks to
  `api.anthropic.com` directly with the token of D43.
- The layout is Docker's default bridge, with taps instead of veths (Firecracker only takes taps):
  a bridge `fc0` holding the gateway of a /24 (`10.230.0.0/24` by default, outside Docker's default
  address pools), one persistent tap per concurrent microVM attached to it as an isolated port (no
  guest-to-guest traffic), IP forwarding, and a masquerade rule for the subnet. Docker sets the
  `FORWARD` policy to `DROP`, so the accept rules go into `DOCKER-USER`, the chain Docker leaves to
  users and jumps to before its own rules and ufw's.
- `deploy/network/fc-net.sh` does it, run as root by the `sandbox-agent-net` system unit, which the
  machine's owner installs once with `sudo deploy/network/install.sh`. The taps belong to the
  account that runs Firecracker, so `runnerd` needs no privilege: guest N gets `fc-tapN` and
  `10.230.0.(10+N)`, gateway `.1`, passed on the kernel command line (`ip=`, needs `CONFIG_IP_PNP`
  in the guest kernel; Firecracker's CI kernel has it, verified 2026-11-20). The guest's `/etc/resolv.conf` points to a public resolver, since the host's
  `127.0.0.53` is not reachable from it. Whether the rules survive a Docker restart is to verify.
- On the board this changes the firewall next to Docker's, which this project does not do without
  the owner's agreement; it is decided when the board's runner joins, and until then the board's
  runner can use `sbx`.
- `vsock-to-anthropic`, `loopback-to-vsock` and their packages stay in the tree but are not part of
  the flow. Cost and budget are self-reported by Claude Code until the proxy returns.
- E-NET is reported as "expected to fail: open network".

The proxy and network limits (allowlist through a `CONNECT` proxy, no LAN, no host) come back before
anyone else uses the system.

## D45. Preview and screenshots (2026-11-20)

A project can declare a dev server (`preview`: start command, port, readiness path, timeout,
non-secret environment, viewport, page limit). The image carries Chromium and Playwright for both
architectures (arm64 Chromium through Playwright, to verify on the board).

- While it works, the agent can start the dev server and drive a headless browser through a tool
  (Playwright MCP), to check its own change.
- After the agent ends, the worker captures the screenshots itself: the agent lists paths in its
  final message (`SCREENSHOTS:`), the worker starts the dev server, waits until it is ready, opens
  each path on `localhost` only, takes a full-page PNG and stops the server. The agent chooses what
  to show, not how it is captured.
- Screenshots travel as `artifact` events and files in the run's result, separate from the change
  set, and are never committed. The manager enforces count, size and dimension limits and decodes
  and re-encodes every PNG with `image/png`.
- They are published to Linear (file upload) and the overview first. GitHub has no API to attach
  images to a PR; linking them from a separate branch is to verify.
- Chromium runs with `--no-sandbox` in the guest: the microVM is the boundary. Rendering is in
  software; there is no GPU in the guest.

## D46. A guest agent over vsock controls the microVM; the workspace comes back as a tar stream (2026-11-20)

D44 shelved vsock for egress (the relay and proxy for the model API). Control is a different job:
the host needs to run commands in the guest, read their output, cancel them and get the workspace
back, and vsock is how Firecracker and Kata reach their guests. A TCP service on the tap would tie
control to the network that the next version closes. So:

- `cmd/guest-agent` is PID 1 in the guest. A one-line shell init mounts `/run` and the agent's own
  read-only disk, then execs it. It mounts the rest (`/proc`, `/sys`, `/dev/pts`, `/dev/shm`,
  `/tmp`, a tmpfs home for the agent user, the workspace disk), copies `/etc` to a tmpfs and adds
  `resolv.conf` and the hostname, brings `lo` up, and listens on vsock port 1024. As PID 1 it
  reaps every child: commands are started with `os.StartProcess` and their status comes from its
  `wait4(-1)` loop, never from `Process.Wait`, which would race with it.
- `internal/guestexec` is the protocol: one JSON request line, then frames (stdout, stderr, tar
  data, end) of at most 1 MiB each. Closing the stream cancels: the guest kills the command's
  process group. Standard library only.
- The workspace goes in as an ext4 image built by the host's `mkfs.ext4 -d` from a tar whose
  owner is forced to the guest's agent user (1000), so it works whatever the host UID is (1002 on
  the board). It comes back through `sandbox.Syncer`, a new optional interface: the guest sends
  `/workspace` as a tar stream, minus the build artifacts of `internal/job`'s exclude list, and
  `guestexec.Mirror` applies it to the host directory. The stream is hostile: every entry is
  resolved under the workspace with `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)`, with size and
  entry limits (E-OUT-7). The manager then validates the change set as before (D5).

Settles the open point of runners.md on how the task and events cross the VM boundary: over this
channel (R2 adds stdin and streaming for the worker).

## D47. Firecracker runs rootless, without the jailer (2026-11-20)

Firecracker's jailer needs root: it creates a chroot, cgroups and namespaces, then drops to an
unprivileged user before exec'ing the VMM. The owner prefers rootless, and the project already
plans a rootless runtime that does the same job (layer 1, M3: user and mount namespaces,
`pivot_root`, cgroups through systemd delegation, seccomp, Landlock). So:

- `runnerd` starts `firecracker` directly, as the runner's account, which is in `kvm`. Nothing
  runs as root at run time; the only root steps are one-time host setup by the owner (the network
  of D44, and later the AppArmor `userns,` profile of M2).
- Until M3, the VMM process has no jail of its own: it runs with the account's rights, plus
  Firecracker's built-in seccomp filters, which apply without the jailer. A guest escaping into
  the VMM would act as that account. Accepted for the development phase, with D43 and D44.
- From M3, the layer 1 runtime wraps the VMM (one namespace set, cgroup and Landlock domain per
  VM), as `microvm.md` already planned ("our runtime can play its role").

## D48. The runner protocol as built: runs, raw output by offset, whole workspace back (2026-11-20)

How R4 implements D42 (`internal/runner`, `internal/runnerapi`, `cmd/runnerd`), and where it
differs from the text of `runners.md`:

- The unit is a **run**: one sandbox, the worker, its output, the workspace back. The job hands it
  to an `Executor`: `runner.Local` (in-process, Firecracker) or the runners' queue, which is the
  same interface served over HTTP. `sbx`, which carries no worker, keeps the direct path.
- The runner forwards the worker's **raw stdout**, in batches, each with its byte offset. The
  manager runs `task.Read` on it itself, so a runner gets no more trust than the worker (D42), and
  a retried batch is counted once while a gap is refused (E-RUNNER-6). D42 had "events with a
  sequence number"; offsets over the raw stream give the same with nothing to parse on the runner.
- The workspace comes back **whole** (minus the build artifacts of the job's exclude list) as a tar
  stream, applied with `guestexec.Mirror`, and the job diffs it against its clean clone as before.
  D42 had a change set computed on the runner against a pristine copy: the whole tree is simpler,
  needs no second copy on the runner, and the manager does the diff it trusts anyway.
- Scheduling is first fit in queue order against the free capacity the runner reports with each
  lease request (vCPUs, MiB); the runner counts what it runs. The bin-packing preferences of D42
  (previous runner first, least room left) are not implemented.
- `agentd` serves the API on `RUNNER_ADDR`, in plain HTTP. Runners refuse plain HTTP to anything
  but loopback, so a runner on another machine needs TLS in front of the manager: how is R5 (the
  tunnel, or a certificate on the LAN; private notes).
- Tokens: `agentd -add-runner <name> -token-out <file>` writes a random token to the file (0600)
  and its SHA-256 to `secrets/runner-tokens`. The runner's name is the token's, whatever it claims.
- Checked on the development machine: `agentd -try` through `runnerd` over loopback, with the fake
  backend and with Claude Code (12 s); and eight microVM runs at once on two runners of 3 and 1
  vCPUs, in two waves of four, 5.8 s in all (`TestFirecrackerFleet`).

## D49. MicroVMs start from a snapshot of a booted guest (2026-11-22)

`Provider.Start` restores a **template** instead of booting: a snapshot of a guest that booted
with no job, its guest agent answering and the workspace disk attached but not mounted
(`sandbox.template=1`). The first start of a machine size builds it (a cold boot, then pause and
a full snapshot); every start after that runs a new firecracker process that loads it
(`/snapshot/load`), and the guest agent makes the guest this VM's (`OpConfigure`). Cold boots
remain for `NoSnapshots`, and as a fallback when a template cannot be built.

- **Relative paths.** firecracker runs in the VM's run directory, and the VM's configuration
  names its files relatively (`rootfs.ext4`, `agent.ext4`, `workspace.ext4`, `v.sock`, the first
  two links to the shared disks). The snapshot records those names, so a restore in another
  directory opens that directory's disks, and Unix socket paths stay short. `vsock_override` is
  set anyway; after the load, the workspace drive is patched to the same name so the guest reads
  its size again.
- **What the guest must not keep** (`OpConfigure`, once per guest): the clock (`clock_realtime`
  on x86, and `Settimeofday` everywhere: KVM's option is x86 only), the random pool (64 host
  bytes through `RNDADDENTROPY`, then `RNDRESEEDCRNG`: guests restored from one snapshot start
  with the same pool), the hostname, and the workspace, mounted only now, after `BLKFLSBUF`: the
  template's kernel read the disk it was built with, and those blocks are stale for the job's
  disk. The template never mounts its workspace, so no file system state of another disk is in
  the snapshot. Without the explicit reseed, guests restored in the experiment already read
  different random bytes (probably VMGenID; to verify): kept, because the guest's kernel and
  Firecracker version may change.
- **Shared memory.** The memory file is mapped private (`mem_backend: File`): every VM restored
  from a template shares its pages through the host's page cache until it writes them. Measured:
  27 MiB RSS but 11 to 16 MiB PSS per idle VM, against 68 MiB for a cold boot.
- **Key and cache.** A template is named after what the snapshot depends on: the firecracker
  binary (a snapshot loads in the version that made it), the kernel and rootfs (size and mtime),
  the tools disk (named after its content), vCPUs, memory, the workspace disk's size and the boot
  arguments. Templates live in `<state>/templates/<key>/`; a build takes a lock file and is
  renamed into place whole, so the first starts of a fleet build it once.
- **Network.** With a tap, the template has an interface: its build borrows the starting VM's tap
  (a second one could be missing when every tap is held by a VM waiting for that template), and
  the key records only that there is a network. The load replaces the tap behind `eth0`
  (`network_overrides`), and `OpConfigure` gives the guest its address and gateway and the MAC of
  its tap with busybox `ip`: every guest restored from a template would otherwise share its MAC
  and address on the bridge.
- **No secret in a template.** It is built before any job, with no workspace and no token (the
  development token of D43 goes in with each command's environment), and every VM restored from
  it shares its memory: nothing of one job may be in it.
- Measured on the development machine (benchmarks.md): 50 ms from nothing to a configured guest
  (p50 of 20, 1 vCPU, 1 GiB), against 159 ms cold; eight at once all ready in 99 ms.
- Checked: the conformance suite on restored VMs with and without a tap, the guest checks
  (worker, HTTPS to the model API, the VM's own address and MAC), and eight runs at once on two
  runners sharing four taps (`TestFirecrackerFleet`, 5.2 s against 5.8 s before).
- Left: a warm pool of restored VMs, the same on the board.

## D50. The board pulls the binaries CI built (2026-11-22)

Settles M0b's open point on how builds reach the board:

- **CI publishes.** When CI passes on a push to main, `.github/workflows/deploy.yml` builds both
  architectures (`make cross`) and force-pushes them to the `builds` branch as one orphan commit:
  `linux-amd64/`, `linux-arm64/`, `SHA256SUMS`, and `COMMIT`, the source commit
  (`scripts/publish-builds.sh`). Only the last build is kept.
- **The board pulls.** A user timer runs `deploy/update/update.sh` every minute: fetch `builds`
  with the checkout's own remote and read-only deploy key, stop if `COMMIT` is the one installed,
  else check the files against `SHA256SUMS`, check that `agentd` starts, keep the current binaries
  in `bin.prev/`, rename the new ones into `bin/` one by one, fast-forward the checkout to the same
  commit (for `deploy/` and the docs), and restart the service. The restart lets a running job
  finish (`TimeoutStopSec`).
- **Why pull.** Nothing connects to the board, and no secret that reaches it lives in GitHub: the
  alternatives were an SSH key and a tunnel token in the repository's secrets (CI pushes), or a
  self-hosted runner, which would run CI code on a machine that also runs other services.
- **Why a branch.** The repository is private: a release asset needs an API token on the board,
  while a branch needs only the deploy key it already has. A build is about 30 MB per
  architecture, and the force-push keeps one.
- Latency: a minute or so after CI. Later, if needed: CI calls agentd's endpoint (behind
  Cloudflare Access) to make it check at once; the signal would carry no binary.

## D51. gVisor runs as the OCI runtime of rootless Podman (2026-11-22)

The gVisor provider (`backends/gvisor`) starts one Podman container per sandbox with
`--runtime <runsc> --userns=keep-id`, idle (`sleep infinity`), and runs commands with
`podman exec`:

- **Why Podman.** `runsc --rootless` alone maps only the account's UID, to root inside: the agent
  (UID 1000 in the image) sees the workspace as root's and cannot write it, and a finer map is
  refused (the gofer cannot fork with it). Podman builds a full user namespace from `/etc/subuid`
  (`newuidmap`), and `keep-id` makes UID 1000 inside the account outside: the workspace is a plain
  bind mount, shared both ways, and no `Syncer` is needed.
- **Limits.** Podman's systemd cgroup manager needs an interactive authorization here, and runsc
  would not use cgroupfs ones (`ignore-cgroups`), so the podman command runs in a transient
  `systemd-run --user --scope` with `MemoryMax`, `MemorySwapMax=0` and `CPUQuota`, in the account's
  delegated tree: conmon, runsc and the sandbox's processes are born in it and stay there. The CPU
  set goes through `taskset`.
- **Network.** Podman's rootless network (pasta) under gVisor's own network stack
  (`--network=sandbox`). Measured: the model API is reachable, the host's LAN address and the
  cloud metadata address are not, so the conformance suite runs with its network checks on, unlike
  Firecracker's (D44). Host loopback through pasta's gateway: to verify.
- **Cancellation.** `podman exec` leaves the command running when its client is killed: each command
  runs under a shell that writes its pid to a file, and a cancelled one is killed in the container
  with its children; deeper descendants die with the container at `Close`.
- **The worker** is bind-mounted read-only at `/run/agent/worker`.
- Measured on the development machine: 89 ms to a started container, the conformance suite and
  the guest checks pass, and `agentd -try` ran Claude Code on a task in 12 s.

## D52. The sandbox runs on its own: a repository and a task in, a patch out (2026-11-22)

`sandbox task` (`cmd/sandbox`) is the project's core without Linear, GitHub or a queue, so it can
be shown, installed and used on any machine, and the Linear flow becomes one client of it:

- **The contract.** In: a Git repository (its HEAD) and a task in Markdown, plus optional setup
  script, provider, agent, model and limits. Out, in a new directory: `changes.patch`
  (`git diff --binary` against HEAD), `result.json`, `events.jsonl`, `worker.log`. Exit status 0
  done, 3 a question, 1 failed.
- **The pieces are the existing ones**: `internal/runner` runs the worker in a fresh sandbox from
  any provider (now listed once, in `backends`, for agentd too), and the prompt
  frames the task as untrusted data, as agentd's frames an issue. The import boundary test keeps
  `cmd/sandbox` free of Linear, GitHub, Git operations and agentd's configuration.
- **The patch is taken on the host, with metadata the agent never saw.** The repository is cloned
  (`repo/`), its tracked files are exported to `workspace/` without `.git`, the sandbox gets only
  the workspace, and the patch comes from the clean clone's Git directory with the workspace as
  work tree only, a separate index, hooks and fsmonitor off (E-OUT-8). Whatever the agent wrote,
  including a `.git` of its own, is content, never configuration.
- **Credentials**: only the development subscription token of D43, from `-token-file` or
  `$CLAUDE_CODE_OAUTH_TOKEN`, for the claude agent; the output is checked for it, and nothing is
  kept if it came back.
- Checked on the development machine: a scripted task on Firecracker (9.9 s with the template
  build) and gVisor (0.3 s) gives the same patch; Claude Code (Haiku) on a demo repository, 8
  turns, 14.8 s, $0.04, and the patch applies with `git apply --check`.
- Next, for a repository that can be shown and installed elsewhere: an install script per host
  (the development machine, the board, a VPS), a demo repository with tasks, and the rest of the
  roadmap's pieces behind the same contract.

## D53. One directory per backend (2026-11-22)

The backends move to `backends/<name>/`: the Go package, the backend's scripts and a README in one
place (`internal/sandbox/{firecracker,gvisor,sbx}` and the top-level `firecracker/` and
`cloud-hypervisor/` before). `backends/backends.go` is the registry every command uses, and
`backends/README.md` compares them. What all backends share stays in `internal/sandbox`: the
interface and the conformance suite. The import boundary test now covers `backends/...` too, so no
backend can depend on Linear, GitHub or agentd's configuration. Shell scripts find the repository
root by looking for `go.mod` rather than by their depth, so they survive the next move.

## D54. Firecracker's API types come from its OpenAPI specification (2026-11-22)

The provider's request bodies are generated from the OpenAPI (Swagger 2.0) specification that
Firecracker publishes in its repository for each release (`src/firecracker/swagger/firecracker.yaml`),
pinned in `backends/firecracker/fcapi/` to the installed release (v1.18.0, commit in
`firecracker.version`). They were handwritten before.

- **Not firecracker-go-sdk**: archived on 2026-11-10 ("no longer maintained", tied to the end of
  firecracker-containerd, no replacement), last release v1.0.0 in 2022, and its API stops at 1.4.1:
  it has none of `network_overrides`, `vsock_override` or `clock_realtime`, which the restore
  path needs (D49).
- **Not a general generator**: oapi-codegen reads OpenAPI 3 only; after kin-openapi's conversion it
  panics on this file, and its compatibility mode gives empty structs. The specification uses
  objects, strings, integers, booleans, arrays, references and enums only, so `fcapi/gen` (one
  file, gopkg.in/yaml.v3) writes plain structs: optional fields are pointers with `omitempty`,
  inline enums become named string types with constants.
- **Kept in step**: `make firecracker-api VERSION=v1.x.y` pins a new release and regenerates; a
  test fails when `types.go` and the specification differ; `Prepare` warns when the installed
  firecracker is not the version of the types.
- The HTTP transport stays a few lines of net/http over the Unix socket.
