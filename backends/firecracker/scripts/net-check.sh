#!/bin/sh
# Check the network of a Firecracker guest (D44, docs/microvm.md): the guest gets tap fc-tap<N> on
# the host bridge, reaches the internet through NAT, and resolves names. The guest side is
# guest/net-check.sh.
#
#   backends/firecracker/scripts/net-check.sh [N]    (default 0: fc-tap0, 10.230.0.10)
#
# Needs the network unit (sudo deploy/network/install.sh) and the amd64 rootfs
# (scripts/build-rootfs.sh).
set -eu

. "$(dirname "$0")/lib.sh"

FC_NET=${1:-0}
export FC_NET

vm=$(fc_new net-check)
mkdir -p "$vm/run"
cp "$fc_root/backends/firecracker/scripts/guest/net-check.sh" "$vm/run/"

fc_kernel_fetch
fc_disk "$vm/work.ext4" 16M run="$vm/run"
fc_config "$vm" 1 512 "sh /tmp/w/run/net-check.sh"
fc_run "$vm" 60

grep -a -E '^(net|addr|route|dns|https|ping)[: ]' "$vm/console.log" | sed 's/\x1b\[[0-9;]*m//g; s/^/  /'
