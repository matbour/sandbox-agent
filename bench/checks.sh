#!/bin/sh
# Measure the checks an agent runs after a change (lint, type check, tests) on the target
# repository in each sandbox (docs/benchmarks.md).
#
#   bench/checks.sh podman
#   bench/checks.sh firecracker
#
# Same input as bench/install.sh (run `bench/install.sh prepare` first), same resources (2 CPUs,
# 8 GiB: `tsc` on the front-end app needs more than 4), no swap, offline. The loop inside each sandbox is checks-guest.sh. Results are appended to
# build/bench/checks.tsv (date, sandbox, commit, check, run, milliseconds, exit code).
set -eu

. "$(dirname "$0")/../firecracker/lib.sh"

runs=${RUNS:-3}
cpus=2
mem_mib=${MEM_MIB:-8192}

root=$fc_root
bench=$root/build/bench
input=$bench/input
rootfs=localhost/sandbox-agent-rootfs:amd64
guest=$root/bench/checks-guest.sh

[ -d "$input/repo" ] || { echo "run bench/install.sh prepare first" >&2; exit 1; }

# A VM has no swap; without --memory-swap, Podman would let the container swap on the host.
record() { # sandbox, then "check <name> <n> <ms> <rc>" lines on stdin
	commit=$(cat "$input/COMMIT")
	while read -r _ name n ms rc; do
		printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(date -u +%FT%TZ)" "$1" "$commit" "$name" "$n" "$ms" "$rc" |
			tee -a "$bench/checks.tsv"
	done
}

case ${1:-} in
podman)
	podman run --rm --network none --user 0 --cpus "$cpus" --memory "${mem_mib}m" --memory-swap "${mem_mib}m" -e HOME=/tmp \
		--volume "$input:/w" --volume "$guest:/checks-guest.sh:ro" "$rootfs" sh /checks-guest.sh /w "$runs" |
		grep '^check ' | record podman
	;;
firecracker)
	vm=$(fc_new bench-checks)
	fc_kernel_fetch
	fc_disk "$vm/work.ext4" 8G repo="$input/repo" cache="$input/cache" run="$root/bench"
	fc_config "$vm" "$cpus" "$mem_mib" "sh /tmp/w/run/checks-guest.sh /tmp/w $runs; umount /tmp/w"
	fc_run "$vm" 1800
	grep -a '^check ' "$vm/console.log" | tr -d '\r' | record firecracker
	;;
*)
	echo "usage: $0 podman | firecracker" >&2
	exit 2
	;;
esac
