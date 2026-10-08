#!/bin/sh
# Measure `bun install --frozen-lockfile --offline` on the target repository in each sandbox
# (docs/benchmarks.md).
#
#   scripts/bench-install.sh prepare <path to the target repository>
#   scripts/bench-install.sh podman
#   scripts/bench-install.sh firecracker
#
# prepare exports the repository's HEAD (no .git) and fills a Bun cache once, with network.
# The runs are offline and get the same resources: 2 CPUs, 4 GiB of memory. Results are appended
# to build/bench/results.tsv. Needs the amd64 rootfs (scripts/build-rootfs.sh).
set -eu

runs=${RUNS:-5}
cpus=2
mem_mib=4096

root=$(cd "$(dirname "$0")/.." && pwd)
bench=$root/build/bench
input=$bench/input
rootfs=localhost/sandbox-agent-rootfs:amd64
tools=localhost/sandbox-agent-tools

# Firecracker's CI guest kernel until we build our own; md5 is the S3 ETag (no checksum file).
kernel_url=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155
kernel_md5=44d1aea84475e5bed0dd4f27ddfca550
kernel=$root/build/amd64/vmlinux

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
	cp "$root/scripts/bench-install-guest.sh" "$input/"

	# Fill the cache: the only step with network.
	podman run --rm --user 0 --volume "$input:/w" --workdir /w/repo "$rootfs" \
		bun install --frozen-lockfile --cache-dir /w/cache
	find "$input/repo" -name node_modules -prune -exec rm -rf {} +

	echo "prepared $(cat "$input/COMMIT"): $(du -sh "$input/repo" | cut -f1) repo, $(du -sh "$input/cache" | cut -f1) cache"
}

run_podman() {
	podman run --rm --network none --user 0 --cpus "$cpus" --memory "${mem_mib}m" \
		--volume "$input:/w" "$rootfs" sh /w/bench-install-guest.sh /w "$runs" |
		record podman
}

run_firecracker() {
	if [ ! -f "$kernel" ]; then
		curl -fsSLo "$kernel" "$kernel_url"
	fi
	echo "$kernel_md5  $kernel" | md5sum -c --quiet -

	# The workspace disk: a writable ext4 image built from the prepared input, with room for
	# node_modules. The file is sparse, so the free space costs nothing on the host.
	vm=$bench/firecracker
	mkdir -p "$vm"
	podman run --rm --network none --volume "$input:/in:ro" --volume "$vm:/out" "$tools" \
		sh -c 'rm -f /out/workspace.ext4 && mkfs.ext4 -q -L workspace -d /in /out/workspace.ext4 8G'

	init="mount -t proc proc /proc; mount -t tmpfs tmpfs /tmp; mkdir -p /tmp/w; mount /dev/vdb /tmp/w; export HOME=/tmp PATH=/usr/local/bin:/usr/bin:/bin; sh /tmp/w/bench-install-guest.sh /tmp/w $runs; umount /tmp/w"

	cat >"$vm/vm.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$kernel",
    "boot_args": "console=ttyS0 reboot=k panic=1 ro quiet init=/bin/sh -- -c \"$init\""
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$root/build/amd64/rootfs.ext4", "is_root_device": true, "is_read_only": true},
    {"drive_id": "workspace", "path_on_host": "$vm/workspace.ext4", "is_root_device": false, "is_read_only": false}
  ],
  "machine-config": {"vcpu_count": $cpus, "mem_size_mib": $mem_mib}
}
EOF

	firecracker --no-api --config-file "$vm/vm.json" >"$vm/console.log" 2>&1 || true
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
