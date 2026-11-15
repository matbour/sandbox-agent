# Backends

A backend is one way of isolating the agent: it starts a sandbox, runs commands in it, and gives
the workspace back. Every backend implements the same interface (`internal/sandbox`) and passes
the same conformance suite (`internal/sandbox/sandboxtest`), so `sandbox task -provider <name>` and
`agentd` (`SANDBOX_PROVIDER`) can use any of them. [backends.go](backends.go) lists them.

| Backend | Isolation | Needs | Status |
| --- | --- | --- | --- |
| [firecracker](firecracker/) | microVM on KVM, one VMM process per sandbox; restored from a snapshot | `/dev/kvm`, the `kvm` group | Main backend: conformance passes, restore in 3 to 7 ms (D49) |
| [gvisor](gvisor/) | user-space kernel (`runsc`) under rootless Podman | Podman, `runsc`; no KVM | Conformance passes, starts in about 90 ms (D51) |
| [sbx](sbx/) | Docker Sandboxes: a microVM per sandbox, managed by Docker's daemon | `docker-sbx`, a Docker account | The first backend, on the board (D11) |
| [cloud-hypervisor](cloud-hypervisor/) | microVM on KVM, with PCI devices | `/dev/kvm` | Experiment: boot, snapshot and restore measured; no provider yet |

Adding one: a Go package here with a `New(sandbox.Options) (sandbox.Provider, error)`, an entry in
`backends.go`, an integration test that runs `sandboxtest.Run`, the escape tests it needs
([docs/escape-tests.md](../docs/escape-tests.md)), and a README like the others.

Integration tests start real sandboxes and only run when asked:

```bash
SANDBOX_IT=1 go test ./backends/<name>/ -run Conformance -v
```
