# Open questions

Move an item to [decisions.md](decisions.md) once settled.

## Platform

- Does Firecracker boot on the RK3588 with vCPUs pinned to cores 4-7? With 2 vCPUs? (M1)
- GIC version and features exposed to guests on this board.
- Final service identity: dedicated `sandbox-agent` user with an AppArmor profile and delegated
  cgroup, or a root parent with heavy systemd hardening?
- Workspace size limit: tmpfs (counts against memory) or a loop-mounted ext4 image per job?

## Agents

- Exact non-interactive flags for the current Claude Code version, and its
  arm64 install method.
- Can both CLIs work with a placeholder API key and a custom base URL? Any other endpoint they call
  at startup (telemetry, update checks) that must be disabled or routed?
- Which models, and what typical cost per issue on a small repository?

## Dependencies

Agents need to install dependencies and run tests. Options, from safest to most convenient:

1. Pre-built rootfs per repository with dependencies baked in; no registry access.
2. Dependencies fetched on the host (in a separate sandbox with registry access only), then mounted
   read-only.
3. A caching mirror (Verdaccio, a PyPI mirror, a Go module proxy) reachable through the egress proxy,
   with an allowlist.

## GitHub and CI

- How to keep CI secrets away from agent branches: workflows that skip secrets for `agent/*`
  branches, required approval, or pushing to a fork.

## Linear

- Webhook headers and payload: `Linear-Signature` (HMAC-SHA256 of the raw body, hex) and the
  `webhookTimestamp` field; confirm against the current documentation, including whether Linear
  publishes the source IP ranges.
- Label trigger versus assignment to a bot user versus Linear's agent integration.
- Target workspace and repository: see the private notes. Develop against a sample repository and
  a dedicated label first.
