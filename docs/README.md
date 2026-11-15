# Documentation

Read the relevant file before working on a topic. Add to it when you learn something that cost time
to work out, and mark anything not verified as "to verify".

| File | Read it when |
| --- | --- |
| [host.md](host.md) | Anything that runs on the board or the development machine: kernel features, users, AppArmor, other services, ingress, toolchains |
| [architecture.md](architecture.md) | What runs today: flow from issue to PR, components, egress, validation, credentials, limits |
| [threat-model.md](threat-model.md) | Before adding or relaxing any isolation or network rule |
| [sandbox.md](sandbox.md) | Implementing layer 1: namespaces, mounts, cgroups, capabilities, Landlock, seccomp |
| [docker-sandboxes.md](docker-sandboxes.md) | Installing, scripting or testing Docker Sandboxes (`sbx`), the first runtime |
| [microvm.md](microvm.md) | Firecracker spike, gVisor fallback, layer comparison |
| [rootfs.md](rootfs.md) | Agent root filesystem: Dockerfile, rootless Podman, ext4 image |
| [benchmarks.md](benchmarks.md) | Sandbox benchmarks: `bun install` offline, per runtime |
| [agents.md](agents.md) | Agent backends, how the agent is run today, the prompt |
| [decision/protocol.md](decision/protocol.md) | Manager, worker and binaries; the task and event protocol; session state machine (D33) |
| [decision/runners.md](decision/runners.md) | Global architecture: one manager, several runners on several machines; configuration, secrets, runner protocol, scheduling, failures, Firecracker in Go (D42 to D45) |
| [escape-tests.md](escape-tests.md) | The list of escape tests and their expected results |
| [roadmap.md](roadmap.md) | What to do next |
| [decisions.md](decisions.md) | Why things are the way they are (short records; longer ones under `decision/`) |
| [open-questions.md](open-questions.md) | What is still undecided or unverified |

## Where the code is

| Directory | What it holds |
| --- | --- |
| `cmd/`, `internal/` | Go, apart from the backends: `agentd` (manager), `runnerd` (runner, D48), `worker` (in the sandbox, D33), `guest-agent` (PID 1 of a Firecracker guest, D46), `sandbox`, and `vsock-to-anthropic` and `loopback-to-vsock` (shelved, D44), with their packages; `internal/sandbox` holds the interface every backend implements and its conformance suite (`sandboxtest`) |
| `images/` | Dockerfiles of the agent rootfs and the tools container, shared by every runtime ([rootfs.md](rootfs.md)) |
| `backends/` | One directory per sandbox backend, with its Go package, its scripts and a README: `firecracker/`, `gvisor/`, `sbx/`, `cloud-hypervisor/` (experiment, no provider yet). `backends.go` lists them for every command ([backends/README.md](../backends/README.md)) |
| `bench/` | Benchmarks that compare runtimes ([benchmarks.md](benchmarks.md)) |
| `scripts/` | Repository tooling: rootfs build, commit message checks |
| `deploy/` | systemd units and examples for the board; `network/`: bridge and NAT for Firecracker guests (D44) |
| `build/` | Ignored. Images, kernels, and one directory per Firecracker run in `build/firecracker/<run>/` |
