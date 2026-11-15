#!/bin/sh
# Measure `bun install --frozen-lockfile --offline` on the target repository in each sandbox
# (docs/benchmarks.md).
#
#   bench/install.sh prepare <path to the target repository>
#   bench/install.sh podman
#   bench/install.sh firecracker
#
# prepare exports the repository's HEAD (no .git) and fills a Bun cache once, with network.
# The runs are offline and get the same resources: 2 CPUs, 4 GiB of memory. Results are appended
# to build/bench/results.tsv. Needs the amd64 rootfs (scripts/build-rootfs.sh). The loop that runs
# inside each sandbox is install-guest.sh.
set -eu

runs=${RUNS:-5}
cpus=2
mem_mib=4096

. "$(dirname "$0")/../backends/firecracker/scripts/lib.sh"

root=$fc_root
bench=$root/build/bench
input=$bench/input
rootfs=localhost/sandbox-agent-rootfs:amd64
guest=$root/bench/install-guest.sh

# build/ can hold Go files from downloaded packages (the Bun cache); a go.mod makes it a separate
# module, so `go test ./...` and the linters skip it.
mkdir -p "$root/build"
[ -f "$root/build/go.mod" ] || echo "module build" >"$root/build/go.mod"

record() { # runtime, then "run <n> <ms>" lines on stdin
	commit=$(cat "$input/COMMIT")
	while read -r _ n ms; do
		printf '%s\t%s\t%s\t%s\t%s\n' "$(date -u +%FT%TZ)" "$1" "$commit" "$n" "$ms" |
			tee -a "$bench/results.tsv"
	done
}

prepare() {
	repo=$1

	rm -rf "$input"
	mkdir -p "$input/repo" "$input/cache"
	git -C "$repo" rev-parse HEAD >"$input/COMMIT"
	git -C "$repo" archive HEAD | tar -x -C "$input/repo"

	# Fill the cache: the only step with network.
	podman run --rm --user 0 --volume "$input:/w" --workdir /w/repo "$rootfs" \
		bun install --frozen-lockfile --cache-dir /w/cache
	find "$input/repo" -name node_modules -prune -exec rm -rf {} +

	echo "prepared $(cat "$input/COMMIT"): $(du -sh "$input/repo" | cut -f1) repo, $(du -sh "$input/cache" | cut -f1) cache"
}

run_podman() {
	podman run --rm --network none --user 0 --cpus "$cpus" --memory "${mem_mib}m" \
		--volume "$input:/w" --volume "$guest:/install-guest.sh:ro" "$rootfs" sh /install-guest.sh /w "$runs" |
		record podman
}

run_firecracker() {
	vm=$(fc_new bench-install)

	# The work disk: the prepared input plus this directory (for install-guest.sh), with room for
	# node_modules.
	fc_kernel_fetch
	fc_disk "$vm/work.ext4" 8G repo="$input/repo" cache="$input/cache" run="$root/bench"
	fc_config "$vm" "$cpus" "$mem_mib" "sh /tmp/w/run/install-guest.sh /tmp/w $runs; umount /tmp/w"
	fc_run "$vm" 600
	grep -a '^run ' "$vm/console.log" | tr -d '\r' | record firecracker
}

case ${1:-} in
prepare)
	[ $# -eq 2 ] || { echo "usage: $0 prepare <repository>" >&2; exit 2; }
	prepare "$2"
	;;
podman) run_podman ;;
firecracker) run_firecracker ;;
*)
	echo "usage: $0 prepare <repository> | podman | firecracker" >&2
	exit 2
	;;
esac
