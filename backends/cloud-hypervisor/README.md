# Cloud Hypervisor

Not a provider yet: an experiment. `snapshot-check.sh` boots the agent rootfs in Cloud Hypervisor,
snapshots the running guest, restores it in a new process and times each step (cold boot about
90 ms, restore 14 to 17 ms with copy-on-write memory).

What a provider needs, from the experiment: no `pci=off` on the kernel command line (its devices
are virtio-pci), `image_type=raw` for the disks, relative API socket paths, and the same hybrid
vsock as Firecracker, so the guest agent should work as is (to verify).

Results and plan: [docs/microvm.md](../../docs/microvm.md), Option A2.
