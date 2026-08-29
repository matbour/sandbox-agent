# Target host

Generic description of the board. LAN addresses, accounts, the other services it runs and access
details are in the private notes (`docs/private/host.md`, not committed).

Checked on 2026-11-11. Re-check values that may drift (kernel version, free memory).

## Hardware and OS

| Item | Value |
| --- | --- |
| Board | Orange Pi 5 Plus |
| SoC | Rockchip RK3588 |
| CPU | 8 cores, big.LITTLE: cores 0-3 Cortex-A55 (`CPU part 0xd05`), cores 4-7 Cortex-A76 (`0xd0b`) |
| RAM | 16 GB, no swap |
| Disk | NVMe, ext4 |
| OS | Ubuntu 26.04.1 LTS, aarch64 |
| Kernel | `7.0.0-42-generic` (Ubuntu stock `linux-generic`), `PREEMPT_DYNAMIC` |
| systemd | 259 |

A55 cores are in-order and slower (about 1.8 GHz, 128 KB L2); A76 cores are out-of-order and about
2 to 3 times faster per core (about 2.4 GHz, 512 KB L2). Both implement ARMv8.2-A.

## Kernel features relevant to isolation

| Feature | State |
| --- | --- |
| KVM | `CONFIG_KVM=y`, `/dev/kvm` present (`root:kvm`, `0660`) |
| cgroups | v2 only (`cgroup2fs`); root controllers: `cpuset cpu io memory hugetlb pids rdma misc dmem` |
| User namespaces | `CONFIG_USER_NS=y` |
| LSMs | `lockdown,capability,landlock,yama,apparmor,bpf,ima,evm` |
| Landlock | `CONFIG_SECURITY_LANDLOCK=y`, active |
| seccomp | `CONFIG_SECCOMP_FILTER=y` |
| BPF LSM / cgroup BPF | `CONFIG_BPF_LSM=y`, `CONFIG_CGROUP_BPF=y` |
| nftables | `CONFIG_NF_TABLES=m` |
| vsock | `CONFIG_VHOST_VSOCK=m` |
| `kernel.dmesg_restrict` | 1 |
| `kernel.kptr_restrict` | 1 |
| `kernel.perf_event_paranoid` | 4 |
| `kernel.io_uring_disabled` | 0 (io_uring allowed host-wide: block it with seccomp) |

### AppArmor blocks unprivileged user namespaces

`kernel.apparmor_restrict_unprivileged_userns = 1` (Ubuntu default). An unprivileged process can only
create a user namespace if its AppArmor profile has the `userns,` rule. Verified:

```console
$ unshare -Ur id
unshare: unshare failed: Permission denied
```

Ubuntu's fix for other applications is a named profile, for example `/etc/apparmor.d/1password`:

```
abi <abi/5.0>,
include <tunables/global>

profile 1password /opt/1Password/1password flags=(unconfined) {
  userns,
  @{exec_path} mr,
  include if exists <local/1password>
}
```

Ours will live in `deploy/apparmor/` (see [roadmap](roadmap.md), M2). Installing it needs root once.

## Accounts

- A development account in the `kvm` group (so `/dev/kvm` opens without root), **not** in the
  `docker` group (root-equivalent). Its sudo requires a password typed by a human.
- Later, a dedicated `sandbox-agent` system account for the service.

## The board is not a lab machine

It also runs other self-hosted services in Docker, and their administration credentials live on it.
A sandbox escape, or even LAN access from a sandbox, would expose them. See
[threat-model.md](threat-model.md). This project never modifies Docker or the other services.

## Ingress

- No inbound port is open. Public traffic arrives through an outbound tunnel (cloudflared).
- The webhook receiver gets a dedicated hostname rule in the tunnel, pointing to
  `http://localhost:<port>`.
- TLS ends at the tunnel provider's edge; the last hop is plain HTTP on localhost.

## Toolchain

Not installed on the board: gVisor (`runsc`), Firecracker, Cloud Hypervisor, bubblewrap, nsjail.
Installed system-wide: `git`, `nft`, `make`, `rsync`.

Go is installed in the development account's home (`~/.local/go`, official tarball, checksum
verified), not system-wide, so the toolchain does not touch the rest of the host. The repository is
checked out in `~/sandbox-agent` and built there with `make build` (`GOOS=linux GOARCH=arm64
CGO_ENABLED=0`, output `bin/sandbox`). See decision D10.
