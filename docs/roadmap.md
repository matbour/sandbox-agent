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

## M0b: development machine (from 2026-11-15, D38)

- [x] Go, gVisor, Firecracker and Cloud Hypervisor in the home directory ([host.md](host.md)).
- [x] `make build` native, `make cross` for linux/amd64 and linux/arm64, both in CI.
- [x] `make hooks` on the development machine.
- [x] The development account in `kvm`; Firecracker and Cloud Hypervisor boot a test guest.
- [x] AppArmor profile with `userns,` for `runsc` on the development machine; rootless gVisor runs.
- [ ] SSH from the development machine to the board (private notes).
- [ ] Decide how builds reach the board: cross-compile and copy the binaries, or `git pull` and
      `make build` on the board. Record it in decisions.md.

## S1: Docker Sandboxes on the board

- [x] The owner installs `docker-sbx` with `--no-install-recommends` (see
      [docker-sandboxes.md](docker-sandboxes.md)).
- [x] `sbx login`, `sbx secret set anthropic` (works headless, 2026-11-13).
- [x] Headless Claude Code on a scratch copy of a sample repository; correct diff (2026-11-13).
- [x] Boot time and memory measured; network policy checked from inside a VM (D14).
- [ ] E-NET and E-SEC tests scripted against `sbx`.

## S2: GitHub App and job runner

- [x] The owner creates the GitHub App (D12) and installs it on the target repository.
- [x] `agentd` (2026-11-13): clone, export without `.git`, setup script, `claude -p` in `sbx`,
      stage against the clean clone, validate, push `agent/*`, draft PR. `-try` runs the sandbox part
      on a local repository; checked on a Bun sample (28 s, $0.13).
- [x] GitHub App created and installed, `agentd -check` passes (2026-11-13).
- [x] Dry run on the target repository in a sandbox: setup, lint and tests work (2026-11-13).
- [x] First real issue end to end: draft PR in 3 min 26, $0.20 with Sonnet (2026-11-13).
- [x] The runtime is an interface, so other runtimes plug in later (milestone P, D30, 2026-11-13).

## S3: Linear trigger

- [x] Polling on the label (D15); comments on the issue (picked up, PR link or error, cost).
- [x] systemd user unit running (`deploy/systemd/`), lingering enabled by the owner (2026-11-13).
- [x] Webhook receiver with signature check, overview at `/`, behind the tunnel and Cloudflare
      Access (D22, 2026-11-13). Checked from outside: login redirect on `/`, 401 for unsigned or
      forged webhooks, 200 for a signed one.
- [ ] Escape tests E-HOOK scripted.
- [ ] End-to-end: label an issue, get a draft PR.

## W: manager and worker protocol (D33, from 2026-11-14)

Design in [decision/protocol.md](decision/protocol.md).

- [x] `make build` builds every `./cmd/...` into `bin/` (2026-11-16).
- [ ] Import test: `cmd/worker`, `cmd/sandbox` and `internal/task` do not depend on `linear`,
      `github`, `config` or `gitops`.
- [ ] `internal/task`: `Task`, `Event` and the event kinds, plus the manager's event reader with its
      limits. E-PROTO tests pass.
- [ ] `ExecOptions` gains `Stdin` and `Stdout` for streaming; the conformance suite covers it. Verify
      that `sbx exec` streams without a TTY.
- [ ] `Spec` gains a read-only mount for the worker binary; the conformance suite checks E-RT-4.
- [ ] `cmd/worker`: setup script, `claude -p --output-format stream-json`, final message parsed into
      `ask`, `done` or `fail`.
- [ ] Manager: render the task, read events, tag their source, forward them to Linear, own the
      session state machine. `internal/job` no longer runs `claude` itself.
- [ ] Check on a real session which status Linear shows after each activity type; record it.
- [ ] A real issue end to end through the worker.

## P: sandbox providers (from 2026-11-13)

- [x] `internal/sandbox` interface, sbx moved behind it, conformance suite; sbx passes (D30).
- [ ] gVisor provider (`internal/sandbox/gvisor`): `runsc` arm64 in the home directory, rootless
      (needs an AppArmor profile with `userns,` installed by the owner, as for M2), rootfs from the
      sbx `claude` image pulled without Docker, no network except a Unix socket to the egress
      proxy (M4) that injects the key. Pass the conformance suite, then run a real issue.
- [ ] Comparison table: start time, install time on the target monorepo, memory, escape tests.

## M1: microVM spike (Firecracker and Cloud Hypervisor)

- [x] Boot a test Firecracker microVM on the development machine, with Firecracker's CI kernel
      and rootfs (M0b).
- [x] Agent rootfs from a Dockerfile, built rootless with Podman into an ext4 image; boots read-only
      in Firecracker with the CI kernel ([rootfs.md](rootfs.md)).
- [ ] Firecracker on the development machine first (D39): build our own guest kernel and
      rootfs image, boot the agent's rootfs with no network device, reach the host over vsock.
- [ ] Boot one Firecracker microVM on the board, pinned to cores 4-7 (see [microvm.md](microvm.md)).
- [ ] Same with Cloud Hypervisor: both run on KVM and support arm64. Cloud Hypervisor also has
      PCI passthrough (GPUs), which Firecracker lacks. Both have snapshot and restore (arm64
      support of each to verify).
- [ ] Snapshot a booted VM and restore it on both; measure the restore time against a cold boot.
- [ ] Decide layer 2: Firecracker, Cloud Hypervisor or gVisor. Record the result and the errors
      met.

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

- [x] `cmd/egress-proxy` (2026-11-16, D40): Unix socket server, `/anthropic/*` route, key
      injection, exact method and path allowlist, body size limit, request log. Checked from a
      Firecracker guest over vsock (`scripts/vsock-check.sh`).
- [ ] Routes for other model providers, model allowlist, per-job budget and cost accounting, rate limit.
- [x] `cmd/forwarder` inside the sandbox on `127.0.0.1:8080`, to vsock or a Unix socket
      (2026-11-16).
- [ ] Claude Code runs a real task through it, in Firecracker; list the calls it needs.
- [ ] E-SEC and E-NET-6, E-NET-7 pass.

## M5: agent run in the layer 1 runtime

Done with `sbx` in S1 and S2. This milestone is the same run in the purpose-built runtime.

- [ ] Rootfs with Node.js, Git, Python and the agent CLIs (arm64).
- [ ] Run the worker in the layer 1 sandbox on a sample repository and a written task; get a change.
- [ ] Same with a second agent backend.

## M6: Linear to pull request

Done in S2 and S3 with `sbx` (receiver, job flow, tunnel rule, service). What remains is
tracked there (E-HOOK, S3) and here:

- [x] E-OUT-1 to E-OUT-5 covered by the `internal/patch` unit tests (workflows, `..`, symlinks,
      `.git`, size limits).
- [ ] E-OUT-6: a test for hostile titles and descriptions in the agent's final message.

## M7: layer 2

- [ ] Run the agent inside Firecracker (vsock for proxy and control) or gVisor.
- [ ] Re-run the escape suite; fill the comparison table in [microvm.md](microvm.md).

## M8: write-up

- [ ] README: what it does, architecture diagram, threat model summary, escape test table per layer,
      measurements (start time, build time, memory), limitations.
- [ ] Short demo video: label an issue, watch the job, review the draft PR.

## Later

- A Vercel Sandbox provider (cloud Firecracker microVMs), wanted by the owner as a commercial
  reference point. Not before the local providers. What it will need, found on 2026-11-15: copy the
  workspace in and out instead of mounting it, a worker built for its architecture (amd64 to
  verify), the model key injected by its firewall's header rules, persistence turned off, a Vercel
  access token on the host, and the REST API from Go (no official Go SDK).
- Snapshot and restore of microVMs for fast starts.
- Caching registry mirror with an allowlist.
- Several repositories and per-repository policy files.
- Rootfs per language.
