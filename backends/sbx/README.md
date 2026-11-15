# Docker Sandboxes (sbx)

Docker Sandboxes run each sandbox as a microVM managed by Docker's own daemon (`sbx`). It was the
first backend (D11) and still runs on the board. Docker manages the VM, the network policy and the
model credentials; the provider drives the `sbx` command line.

- Go package: this directory. Integration test: `sbx_it_test.go` (`SANDBOX_IT=1`).
- Setup and findings: [docs/docker-sandboxes.md](../../docs/docker-sandboxes.md).
