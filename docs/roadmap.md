# Roadmap

Milestones in order. Each one ends with something that runs on the board and a short note in
[decisions.md](decisions.md) or the relevant doc.

No fixed deadline (D13). First get the full Linear-to-PR loop working on the target repository with
Docker Sandboxes (S1 to S3, D11), then add the other runtimes behind the same interface and compare
them (M1 to M8). The target workspace and repository are in the private notes.

## M0: setup (done 2026-11-11)

- [x] Development account on the board, in `kvm`, not in `docker`; SSH alias (private notes).
- [x] Repository, Go module, `make build`; a stub runs on the board.
- [x] Host facts recorded in [host.md](host.md).

## S1: Docker Sandboxes on the board

- [x] The owner installs `docker-sbx` with `--no-install-recommends` (see
      [docker-sandboxes.md](docker-sandboxes.md)).
- [x] `sbx login`, `sbx secret set anthropic` (works headless, 2026-11-13).
- [x] Headless Claude Code on a scratch copy of a sample repository; correct diff (2026-11-13).
- [x] Boot time and memory measured; network policy checked from inside a VM (D14).
- [ ] E-NET and E-SEC tests scripted against `sbx`.

## S2: GitHub App and worker

- [ ] The owner creates the GitHub App (D12) and installs it on the target repository.
- [x] `agentd` (2026-11-13): clone, export without `.git`, setup script, `claude -p` in `sbx`,
      stage against the clean clone, validate, push `agent/*`, draft PR. `-try` runs the sandbox part
      on a local repository; checked on a Bun sample (28 s, $0.13).
- [x] GitHub App created and installed, `agentd -check` passes (2026-11-13).
- [x] Dry run on the target repository in a sandbox: setup, lint and tests work (2026-11-13).
- [x] First real issue end to end: draft PR in 3 min 26, $0.20 with Sonnet (2026-11-13).
- [ ] The runtime is an interface in the worker, so other runtimes plug in later.

## S3: Linear trigger

- [x] Polling on the label (D15); comments on the issue (picked up, PR link or error, cost).
- [x] systemd user unit running (`deploy/systemd/`), lingering enabled by the owner (2026-11-13).
- [x] Webhook receiver with signature check, overview at `/`, behind the tunnel and Cloudflare
      Access (D22, 2026-11-13). Checked from outside: login redirect on `/`, 401 for unsigned or
      forged webhooks, 200 for a signed one.
- [ ] Escape tests E-HOOK scripted.
- [ ] End-to-end: label an issue, get a draft PR.

## P: sandbox providers (from 2026-11-13)

- [x] `internal/sandbox` interface, sbx moved behind it, conformance suite; sbx passes (D30).
- [ ] gVisor provider (`internal/sandbox/gvisor`): `runsc` arm64 in the home directory, rootless
      (needs an AppArmor profile with `userns,` installed by the owner, as for M2), rootfs from the
      sbx `claude` image pulled without Docker, no network except a Unix socket to the egress
      proxy (M4) that injects the key. Pass the conformance suite, then run a real issue.
- [ ] Comparison table: start time, install time on the target monorepo, memory, escape tests.

## M1: Firecracker spike

- [ ] Boot one microVM pinned to cores 4-7 (see [microvm.md](microvm.md)).
- [ ] Decide layer 2: Firecracker or gVisor. Record the result and the errors met.

## M2: rootless user namespaces

- [ ] Write `deploy/apparmor/sandbox-agent` (`userns,` for the deployed binary path).
- [ ] The owner installs it once (`sudo cp … /etc/apparmor.d/ && sudo apparmor_parser -r …`).
- [ ] `sandbox run -- id` prints UID 0 inside, as the development account outside, without sudo.

## M3: layer 1 sandbox

- [ ] Namespaces, UID/GID maps, `pivot_root` into a minimal rootfs, `/proc`, `/dev`, tmpfs.
- [ ] Capabilities dropped, `no_new_privs`, PID 1 reaper, fd hygiene.
- [ ] cgroup per job (memory, CPU, cpuset 4-7, PIDs), teardown with `cgroup.kill`.
- [ ] Landlock and seccomp.
- [ ] `cmd/escape` and the first escape tests (E-FS, E-NET, E-PRIV, E-SYS, E-RT, E-RES) pass.

## M4: egress proxy

- [ ] Unix socket server with `/anthropic/*` route, key injection, allowlist,
      size limits, per-job budget, logs.
- [ ] Forwarder inside the sandbox on `127.0.0.1:8080`.
- [ ] E-SEC and E-NET-6, E-NET-7 pass.

## M5: agent run, by hand

- [ ] Rootfs with Node.js, Git, Python and the agent CLIs (arm64).
- [ ] Run Claude Code in the sandbox on a sample repository and a written task; get a patch.
- [ ] Same with a second agent backend.

## M6: Linear to pull request

- [ ] Receiver: signature, timestamp, dedup, label filter, size limit (E-HOOK pass).
- [ ] Worker: fetch issue, clone, workspace without `.git`, run, validate patch (E-OUT pass),
      apply on a clean clone, push, draft PR, comment on the issue.
- [ ] Tunnel hostname rule before the wildcard, service on the board.
- [ ] End-to-end on a sample repository.

## M7: layer 2

- [ ] Run the agent inside Firecracker (vsock for proxy and control) or gVisor.
- [ ] Re-run the escape suite; fill the comparison table in [microvm.md](microvm.md).

## M8: write-up

- [ ] README: what it does, architecture diagram, threat model summary, escape test table per layer,
      measurements (start time, build time, memory), limitations.
- [ ] Short demo video: label an issue, watch the job, review the draft PR.

## Later

- Snapshot and restore of microVMs for fast starts.
- Caching registry mirror with an allowlist.
- Several repositories and per-repository policy files.
- Rootfs per language.
