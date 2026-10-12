# Open questions

Move an item to [decisions.md](decisions.md) once settled.

## Platform

- Does Firecracker boot on the RK3588 with vCPUs pinned to cores 4-7? With 2 vCPUs? (M1)
- GIC version and features exposed to guests on this board.
- Final service identity: today a systemd user unit of the development account. Later: a dedicated
  `sandbox-agent` user with an AppArmor profile and delegated cgroup, or a root parent with heavy
  systemd hardening? On hold (2026-11-15): good practice, but it complicates the setup. It is the
  only measure that keeps the development account (Claude Code, VS Code) from reading or decrypting
  `agentd`'s secrets.
- Secrets at rest, on hold (2026-11-15). Today `secrets/`, the cached Linear token and the `gh`
  token are plaintext files, mode 0600. Options, cheapest first:
  - encrypt `agentd`'s secrets with systemd credentials (`LoadCredentialEncrypted=`, works without a
    TPM: [host.md](host.md#secrets-at-rest-no-tpm-checked-2026-11-15)), and stop caching the Linear
    token on disk (does minting at every start hit a Linear limit? to verify);
  - replace the `gh` token with a fine-grained one limited to this repository, read-only;
  - add a TPM module (LetsTrust TPM, Infineon SLB 9672, SPI) and a hand-written device tree
    overlay. Power pins, logic level and fit to verify first. Protects only against a disk taken out
    of the board.
- Workspace size limit: tmpfs (counts against memory) or a loop-mounted ext4 image per job?

## Agents

- Claude Code's flags are settled ([agents.md](agents.md)), and so are the models and their cost
  (D16, D20: $0.20 for a first real issue with Sonnet). Still open for any second backend:
  non-interactive flags and arm64 install.
- Can both CLIs work with a placeholder API key and a custom base URL (egress proxy, next version)? Any other
  endpoint they call at startup (telemetry, update checks) that must be disabled or routed?

## Dependencies

Agents need to install dependencies and run tests. Today the `sbx` policy is open (D14) and the
setup script installs from the public registries. Options for a closed policy, from safest to
most convenient:

1. Pre-built rootfs per repository with dependencies baked in; no registry access.
2. Dependencies fetched on the host (in a separate sandbox with registry access only), then mounted
   read-only.
3. A caching mirror (Verdaccio, a PyPI mirror, a Go module proxy) reachable through the egress proxy,
   with an allowlist.

## GitHub and CI

- How to keep CI secrets away from agent branches: workflows that skip secrets for `agent/*`
  branches, required approval, or pushing to a fork.

## Linear

- Signatures and timestamps are settled and checked from outside (D22). Still open: whether
  Linear publishes its webhook source IP ranges.
- Triggers are settled: label (D15, D20), mention and delegation through agent sessions (D25, D27).
- Which session status Linear shows after each activity type. The guide only says it follows the
  last activity ([decision/protocol.md](decision/protocol.md)).

## Runners (D42 to D45)

Details and the proposed answers are in [decision/runners.md](decision/runners.md#open).

- The manager's store, now that `state.json` (D23) must hold leases and concurrent runs.
- Firecracker on the board, and whether the board accepts a tap and NAT (D44).
- How a runner on another machine reaches the manager (LAN, VPN, tunnel with a service token).
- Time to serve a base tree of the target monorepo as a tar (to measure).
- How to revoke a `claude setup-token` token (D43).
- Playwright's arm64 Chromium on the board; GitHub rendering of screenshots linked from a branch
  (D45).

## Protocol (D33)

- Does `sbx exec` stream stdin and stdout without a TTY?
- Can the cost be measured outside the sandbox while `sbx` is the provider, or does it stay
  self-reported until the egress proxy (M4)?
