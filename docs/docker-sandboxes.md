# Docker Sandboxes (`sbx`)

First runtime used to run the agent (decision D11). Each sandbox is a microVM on KVM, with its own
kernel and its own Docker daemon, behind a host-side proxy. Read this before installing, upgrading or
scripting `sbx`.

Sources: Docker's [install](https://docs.docker.com/ai/sandboxes/install/),
[architecture](https://docs.docker.com/ai/sandboxes/architecture/),
[security](https://docs.docker.com/ai/sandboxes/security) and
[Claude Code](https://docs.docker.com/ai/sandboxes/agents/claude-code.md) pages, Arm's
[install guide](https://learn.arm.com/install-guides/sbx/), and inspection of the package on the
board (2026-11-13).

## Requirements

- Ubuntu 24.04 or later, arm64 supported (Linux arm64 since `sbx` 0.33).
- KVM, and the user in the `kvm` group. The development account already is.
- **No Docker Engine and no `docker` group**: per the install page, "you don't need Docker Desktop
  or Docker Engine to use `sbx`". The host daemon is never used.
- A Docker account: `sbx login` (OAuth device flow in a browser). A free account is enough.
- An API key for the agent, stored with `sbx secret set anthropic`.

## Package contents (`docker-sbx` 0.51.0, from the Docker apt repository)

Checked by extracting the `.deb` without installing it:

- `/usr/bin/sbx`, the CLI. It runs as the user; there is no system daemon and no systemd unit.
- `/usr/libexec/`: `containerd-shim-nerdbox-v1`, `docker-docklet`, `libsailor.so`, a guest kernel
  (`nerdbox-kernel-arm64_4k`), two EROFS root filesystems, `mkfs.ext4` and `mkfs.erofs`.
- `/etc/apparmor.d/docker-sbx-nerdbox-shim`: an unconfined profile for the shim and the docklet,
  only to allow unprivileged user namespaces. This is the same technique as D7.
- Maintainer scripts: only the AppArmor reload (`dh_apparmor`), plus `killall sbx` on removal.
- No setuid or setgid file.

Dependencies: `ca-certificates`, `dbus-daemon`, `e2fsprogs` and **`gnome-keyring`**. The keyring
pulls in GTK, Mesa and GStreamer libraries: 111 new packages with `--no-install-recommends`, and 137
without it. Without that flag, `libpam-gnome-keyring` is also installed, and it changes the PAM
configuration. **Always install with `--no-install-recommends`.** The simulation upgrades nothing and
removes nothing, and leaves the installed Docker packages untouched.

Install command, run by the owner:

```bash
sudo apt-get install --no-install-recommends --no-upgrade docker-sbx
```

## Security model, as documented

- Isolation boundary: the microVM, with a separate kernel per sandbox. Inside it, the agent is root
  and has its own Docker Engine.
- Credentials: a host-side forward proxy replaces a sentinel value with the real key after the
  request leaves the VM. "The raw credential values never enter the VM." This is compatible with D4.
- Network: all outbound TCP goes through the host proxy, deny by default. UDP and ICMP are blocked.
  Policy presets are `balanced` and `locked down`; rules are added with
  `sbx policy allow network <host>`. The agent "cannot access your host Docker daemon, your host
  network directly".
- Workspace: a direct mount is a read-write filesystem passthrough at the same absolute path.
  Clone mode (`--clone`) mounts the repository read-only and gives the agent a private clone. The
  docs warn that Git hooks written by the agent do not show in `git diff`.

## How we use it

- The host prepares a scratch copy of the repository **without `.git`** and mounts that, never the
  real clone (D5). The host computes the diff against the original and validates it.
- Headless run: `sbx run --name <job> claude -- "$(cat task.md)"`. Everything after `--` goes to
  Claude Code, which `sbx` starts with `--dangerously-skip-permissions`.
- Network policy: `allow-all` with the host, loopback and private ranges denied (D14). The exact
  commands are in the "Network policy in use" section.

## Observed on the board (2026-11-13, `sbx` 0.51.0)

Installed with `--no-install-recommends`; `sbx diagnose` passes (KVM, storage, auth, policy).

Headless secrets: `sbx secret set anthropic` works over SSH with no desktop session and no
`~/.local/share/keyrings`. Where the secret is stored is still to verify.

Processes, all as the development account, none as root:

- `sbx daemon start` (sandboxd): started in the background by the first CLI call, detached (parent
  PID 1). Socket and logs in `~/.local/state/sandboxes/sandboxes/sandboxd/`.
- One `docker-docklet` per sandbox, also detached; it holds the VM (about 1.4 GB RSS for a 2 GiB
  sandbox). Its command line names `/run/containerd/containerd.sock`, but that socket is
  `root:root 0660` and no connection to it was seen: it uses its own containerd socket under
  `/run/user/<uid>/containerd/`. The system Docker and containerd are not touched.

Guest: kernel 7.0.14, root filesystem EROFS read-only, `init=/sbin/vminitd`, vsock for control.
User `agent` (UID 1000) in `sudo` and `docker`. `--cpus` and `--memory` are honoured.

Credentials inside the VM: only placeholders. `GH_TOKEN` is set to a sentinel
(`gho_sbxproxymanaged…`) even when no GitHub secret is stored; there is no Anthropic key, only
`SBX_CRED_ANTHROPIC_MODE=apikey`. All HTTP(S) goes through `HTTPS_PROXY`, and the guest trusts the
proxy's CA (`PROXY_CA_CERT_B64`): the proxy terminates TLS to inject keys. **Never store a GitHub
secret in `sbx`**: the proxy would then inject a real token for `github.com` (D5).

Network, checked with `sbx policy check network` under the `balanced` preset: LAN (a `192.168.x.x` gateway,
`10.0.0.1`), loopback and `169.254.169.254` are denied; unknown hosts are denied; but `github.com`
and the package registries (npm, PyPI, crates, Maven…) are allowed. With an attacker-supplied token
in the issue text, that is an exfiltration path (push to a foreign repository, publish a package).
Per-sandbox `--deny-network` can only narrow the global policy.

CPU pinning: there is no option, and the VM does not inherit the CLI's affinity. It does inherit the
daemon's: start the daemon with `setsid taskset -c 4-7 sbx daemon start` (after `sbx daemon stop`)
and every `docker-docklet` thread lands on 4-7. Changing a running VM with `taskset -ap` also works.
If the daemon is auto-started by a plain `sbx` call (after a reboot, for example), it is unpinned:
`agentd` checks the daemon's affinity before each job.

Timings:

| Step | Time |
| --- | --- |
| `sbx create`, first time (pulls `docker.io/sbx/claude`) | 28.5 s |
| `sbx create`, image cached | 8.2 s |
| `sbx exec` of a trivial command | 0.4 s |
| `claude -p` on a two-file task (fix plus tests, pytest via `uv`) | 17.6 s |
| `sbx rm --force` | 0.5 s |

Headless run that works:

```bash
sbx create --name <job> --cpus 2 -m 2g --skills off --static-mcp none --no-progress --no-input \
  claude <workspace>
sbx exec -w <workspace> <job> claude -p "<task>" --dangerously-skip-permissions --max-turns 15
sbx rm --force <job>
```

`sbx run` refuses to run without a TTY unless `--detach`; `sbx rm` asks for confirmation unless
`--force`. The agent leaves `__pycache__` and `.pytest_cache` in the workspace: the patch validator
must drop build and cache artifacts.

## Network policy in use (D14)

```bash
sbx policy reset --force
sbx policy init allow-all
for r in 127.0.0.0/8 ::1/128 localhost 0.0.0.0/8 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 \
         169.254.0.0/16 100.64.0.0/10 fc00::/7 fe80::/10; do
  sbx policy deny network "$r"
done
```

`allow-all` alone allows `127.0.0.1` and `localhost`, which the proxy would forward to the host's own
loopback: the loopback rules are not optional. `::1` must be written `::1/128`. Checked from inside a
VM with `curl`: internet 200, the board's LAN address 403 (also with `--noproxy '*'`), a LAN gateway
and `169.254.169.254` 403.

## Toolchains

The `claude` image has Python, `uv` and Git, but no Node.js or Bun. `agentd` runs `SETUP_SCRIPT` as
the agent user before the agent; `deploy/examples/setup.sh` installs Bun from the npm registry
(`@oven/bun-linux-aarch64`) and runs `bun install`. A custom template image (`sbx create -t`) would
save that time on every job; to try later.

## Lessons from the first runs on a real monorepo (2026-11-13)

- **Never put the job directory on a tmpfs.** The workspace is shared with the host through
  virtiofs, so `node_modules` (3.6 GB for a 3,400-package Bun monorepo) lands on the host
  filesystem. On `/tmp` (tmpfs, RAM) plus a 6 GiB VM, the board ran out of memory and the VM died
  mid `bun install` (shim disconnected, `ttrpc: closed`, exit 255). With the default
  `~/.local/state/sandbox-agent` on NVMe, host available memory stayed above 3.8 GB.
- After such a crash, `sbx rm --force` fails ("volume is still referenced"). Restarting the daemon
  (pinned again) and removing again works; `agentd` does this automatically.
- Timing on that monorepo: about 2 min of setup (Bun install, `bun install`, Socket scanner 50 s),
  then the agent.

## To verify

- Where `sbx secret set` stores the key on a host without a GNOME keyring.
- A locked-down global policy that keeps the model API and read-only registry access, and whether
  rules can be restricted by method (the policy table has METHOD and PATH columns).
- E-NET tests from inside the VM (not only `policy check`), including DNS and raw IP connections.
- What `sbx` sends to Docker (telemetry, see the FAQ), and whether it works offline once logged in.
- Whether the agent can see host paths outside the workspace (stdio MCP servers and shared skill
  stores run on the host, according to the security page).
- Cleanup: `sbx rm` and where the VM disks live on the host.

## Shared Bun cache: measured, not adopted (2026-11-13)

On the target monorepo (3,021 packages), with a 4-CPU, 4 GiB VM:

| Install | Time |
| --- | --- |
| No cache (current setup) | 123 s |
| Shared cache, read-only mount | fails: Bun needs its temporary directory on the cache's filesystem (`EROFS … set $BUN_TMPDIR`), even on cache hits |
| Shared cache through an overlay in the VM (root install, then `chown` to `agent`) | 181 s (143 s + 38 s) |

The cost is writing about 2 GB of packages into the workspace, a virtiofs mount shared with the
host, not downloading them. Other findings: the extra mount `PATH:TARGET:ro` works and refuses
writes; the guest kernel has overlayfs but the image has no `mount` binary (a `mount(2)` call from
Python works); in such an overlay `agent` cannot create files inside lower directories, `root`
can. Next idea to measure: `node_modules` on the VM's own disk (a bind mount over
`workspace/node_modules`), where a shared cache could hard-link instead of copying.
