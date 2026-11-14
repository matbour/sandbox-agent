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
- [x] Decide how builds reach the board (D50, 2026-11-22): CI publishes the binaries of every
      commit of main that passed to the `builds` branch, and a user timer on the board pulls,
      checks and installs them, then restarts the service (`deploy/update/`).

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
- [x] Import test (2026-11-20, `internal/task/boundary_test.go`): `internal/task`, `cmd/worker`,
      `cmd/guest-agent`, `cmd/sandbox` and `internal/guestexec` do not depend on `linear`,
      `github`, `gitops`, `config` or `job`.
- [x] `internal/task` (2026-11-20): `Task`, `Event` and the event kinds, the final-message parsers,
      and the manager's event reader with its limits. E-PROTO-1 and 3 to 7 are unit tests.
- [x] `ExecOptions` gains `Stdin` and `Stdout` (2026-11-20); the conformance suite covers it and
      Firecracker passes. `sbx` returns `ErrNoStreaming` until `sbx exec` is checked on the board.
- [x] The worker binary read-only in the guest (2026-11-20): on Firecracker's tools disk, reported
      through `sandbox.WorkerHost`; the microVM tests check E-RT-4 (not even root can write it).
      For `sbx`: a read-only mount, still to do.
- [x] `cmd/worker` (2026-11-20): setup script, `claude -p --output-format stream-json`, final
      message parsed into `ask`, `done` or `fail`; a `fake` backend scripted by `FAKE:` lines.
- [x] Manager, first part (2026-11-20): with a sandbox that has the worker, `internal/job` renders
      the task, reads the events (`events.jsonl`), forwards thoughts to the reporter (at most one
      every 30 s) and maps the terminal event back to the final message. Checked with `agentd -try`
      on Firecracker: the fake backend in 0.9 s, Claude Code (Haiku) in 9.4 s.
- [ ] Manager, rest: tag events with their source in the overview, the session state machine of
      D33 (moves into R3's durable steps), and `sbx` through the worker so the direct path goes.
- [ ] Check on a real session which status Linear shows after each activity type; record it.
- [x] A real issue end to end through the worker (2026-11-22, Firecracker restored from a
      snapshot: label to ready pull request in 2 min 32 s).

## R: one manager, several runners (D42 to D45, from 2026-11-20)

Design in [decision/runners.md](decision/runners.md). The priority of the project: a Go control
plane scheduling Firecracker microVMs on an arm64 and an amd64 machine at once. Linear polish, the
gVisor provider and a second agent backend wait until after the demonstration (M8). Network limits are for
the next version (N).

- [ ] R0, Firecracker on the board: boot the CI kernel with vCPUs pinned to cores 4-7 (M1). If it
      does not boot, the board's runner uses `sbx` for the demonstration.
- [x] R1a, Firecracker provider in Go on the development machine (2026-11-20, D46):
      `internal/sandbox/firecracker` with its own API client over the Unix socket, a tap per
      microVM from a flock'd pool (D44), the workspace disk in and out (`sandbox.Syncer`), the
      guest agent on a cached read-only disk, `cmd/guest-agent` as PID 1 over vsock. Passes the
      conformance suite (network checks skipped on purpose); `agentd` can use it
      (`SANDBOX_PROVIDER=firecracker`). Guest agent ready 0.8 s after start. First real agent
      run through `agentd -try` with the subscription token (D43): 10.9 s end to end.
- [x] R1b, part (2026-11-20): `curl` in the image; `Prepare` reaps the microVMs of a dead owner
      (checked by killing `agentd` mid-run). No jailer: rootless, and the layer 1 runtime wraps
      the VMM from M3 (D47).
- [ ] R1c: an OCI image (multi-arch, by digest) turned into a cached ext4 root disk per
      architecture in Go, instead of `scripts/build-rootfs.sh`.
- [x] R2, worker (2026-11-20): stdin and streamed events over the guest agent (D46), the dev token
      in the guest's environment (D43), and a `fake` agent backend. See milestone W for the rest.
- [ ] R3, manager core: sessions made of durable steps, `Tracker` and `Forge` interfaces, inbox and
      outbox, secret scan of outputs; waiting for CI becomes a step with a timer. Decide the store
      (D23 does not fit leases) and the project file format.
- [x] R4, runners (2026-11-20, D48): `cmd/runnerd`, per-runner tokens, register, lease (long
      poll), heartbeat, cancellation, output by byte offset, workspace back as a tar stream;
      `agentd` and `runnerd` on one machine over loopback; import test extended. Eight microVM runs
      at once on two runners pass (`TestFirecrackerFleet`). Left: TLS for runners on other
      machines (R5); `agentd -try` stays in agentd (it starts the API when `RUNNER_ADDR` is set).
- [ ] R5, two machines: decide how a runner reaches the manager (private notes) and the board's
      network (D44); scheduling by resources and `requires`; two runs at once on arm64 and amd64.
- [ ] R6, preview and screenshots (D45): Chromium and Playwright in the image for both
      architectures, the `SCREENSHOTS:` capture step, `artifact` events, upload to Linear and the
      overview.
- [ ] R7, failures: lease expiry, runner and manager restarts mid-run, Stop during a run. Each one
      exercised once and recorded. Done (unit tests in `internal/runnerapi`): a lease lost before any
      output is queued again and run by another runner; one lost after output fails the job and
      is not retried; a cancel on the manager reaches the runner through its next heartbeat and
      stops the sandbox. A dead agentd's microVMs are reaped (R1b). Left: the manager restarting
      mid-run (runs are in memory until R3's store), and each case once with real microVMs.
- [ ] R8, fast starts: snapshot and restore per image and architecture, warm pool; measure lease to
      first guest instruction, restore against cold boot, memory per idle microVM, on both machines
      ([benchmarks.md](benchmarks.md)). Done on the development machine (2026-11-20): cold start
      to a guest agent that answers in 160 ms (p50), eight at once in 216 ms, 68 MiB per idle
      guest (`TestStartBench`). Snapshots (2026-11-22, D49): every start restores a template of a
      booted guest, 50 ms (p50) instead of 159 ms, eight at once in 99 ms, 11 to 28 MiB PSS per
      idle guest since restored VMs share the template's pages; with the network too. Left: a
      warm pool, the board.
- [ ] E-RUNNER tests ([escape-tests.md](escape-tests.md)) pass. Covered by unit tests: 1 (token),
      2 (foreign lease), 3 (expired lease), 6 (replayed output), 7 (protocol). Left: 4 and 5 as
      explicit tests (the code paths are Mirror's limits and the output size limit).

## P: sandbox providers (from 2026-11-13)

- [x] `internal/sandbox` interface, sbx moved behind it, conformance suite; sbx passes (D30).
- [x] gVisor provider (`internal/sandbox/gvisor`, D51, 2026-11-22, development machine): runsc as
      the OCI runtime of rootless Podman, the agent image, the workspace as a bind mount, limits
      through a systemd scope. Passes the conformance suite with the network checks on (the
      model API is reachable, the host's LAN address and cloud metadata are not), starts in
      89 ms, and ran Claude Code on a task through `agentd -try` (12 s). Left: the board (arm64),
      a real issue, the egress proxy of M4 instead of pasta.
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
      Done on the development machine (2026-11-22): Firecracker restores in 3 to 7 ms through the
      provider (D49), Cloud Hypervisor in 14 to 17 ms with `copyonwrite`
      (`cloud-hypervisor/snapshot-check.sh`). Left: the board.
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

Shelved for the development phase (D44): the code stays, the flow does not use it. It returns
with milestone N.

- [x] `cmd/vsock-to-anthropic` (2026-11-16, D40, D41): Unix socket server, `/anthropic/*` route, key
      injection, exact method and path allowlist, body size limit, request log. Checked from a
      Firecracker guest over vsock (`firecracker/vsock-check.sh`).
- [ ] Routes for other model providers, model allowlist, per-job budget and cost accounting, rate limit.
- [x] `cmd/loopback-to-vsock` inside the microVM guest on `127.0.0.1:8080` (2026-11-16). The
      layer 1 sandbox will need its own loopback-to-Unix-socket relay.
- [x] Claude Code runs a real task through it, in Firecracker: only `POST /v1/messages` needed
      (2026-11-16, [microvm.md](microvm.md)).
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

## N: network limits (next version)

- [ ] Bring back the egress proxy (M4) with the model key, so no credential enters the guest; drop
      the development token (D43) and revoke it.
- [ ] No LAN, no host: a `CONNECT` proxy with a per-project host allowlist, or no network device at
      all for projects that need none.
- [ ] Build credentials through proxy routes with header injection.
- [ ] E-NET and E-SEC pass again.

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
