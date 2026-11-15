# Shared steps for running something in a Firecracker microVM (docs/microvm.md). Sourced by the
# scripts in backends/firecracker/scripts/ and bench/; not run on its own.
#
# Every VM has the same shape: the agent rootfs as a read-only root disk, one writable work disk
# mounted at /tmp/w in the guest, a vsock device whose port 8080 leads to vsock-to-anthropic, and
# no network device unless FC_NET=<N> is set: then the guest gets tap fc-tap<N> on the host bridge
# (deploy/network/, D44) and the address 10.230.0.(10+N). The guest's init is a shell command (no init system yet): it mounts
# /proc, a tmpfs on /tmp and the work disk, then runs the given command.
#
# Run directories live in build/firecracker/<name>/. Unix socket paths are limited to 108 bytes,
# so keep the repository path and run names short.

# The repository's root: the nearest directory above the calling script that holds go.mod.
fc_root=$(cd "$(dirname "$0")" && pwd)
while [ ! -f "$fc_root/go.mod" ]; do
	[ "$fc_root" != / ] || { echo "lib.sh: repository root not found" >&2; exit 1; }
	fc_root=$(dirname "$fc_root")
done
fc_tools=localhost/sandbox-agent-tools
fc_arch=amd64

# Firecracker's CI guest kernel until we build our own; md5 is the S3 ETag (no checksum file).
fc_kernel_url=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155
fc_kernel_md5=44d1aea84475e5bed0dd4f27ddfca550
fc_kernel=$fc_root/build/$fc_arch/vmlinux
fc_rootfs=$fc_root/build/$fc_arch/rootfs.ext4

# fc_new <name>: start a fresh run directory and print its path.
fc_new() {
	dir=$fc_root/build/firecracker/$1
	rm -rf "$dir"
	mkdir -p "$dir"
	echo "$dir"
}

# fc_kernel_fetch: download the guest kernel once and check it.
fc_kernel_fetch() {
	mkdir -p "$(dirname "$fc_kernel")"
	if [ ! -f "$fc_kernel" ]; then
		curl -fsSLo "$fc_kernel" "$fc_kernel_url"
	fi
	echo "$fc_kernel_md5  $fc_kernel" | md5sum -c --quiet -
}

# fc_disk <image> <size> <name>=<host dir>...: build the work disk. Each host directory appears as
# /tmp/w/<name> in the guest. The image is sparse, so free space costs nothing on the host.
fc_disk() {
	image=$1
	size=$2
	shift 2

	vols=""
	for m in "$@"; do
		vols="$vols --volume ${m#*=}:/stage/${m%%=*}:ro"
	done

	# shellcheck disable=SC2086 # $vols is a list of options
	podman run --rm --network none $vols --volume "$(dirname "$image"):/out" "$fc_tools" \
		mkfs.ext4 -q -L work -d /stage "/out/$(basename "$image")" "$size"
}

# fc_config <run dir> <vcpus> <mem MiB> <command>: write <run dir>/vm.json. The work disk is
# <run dir>/work.ext4 and the vsock device <run dir>/v.sock.
fc_config() {
	dir=$1
	init="mkdir -p /tmp/w; mount /dev/vdb /tmp/w; export HOME=/tmp PATH=/usr/local/bin:/usr/bin:/bin:/sbin; $4"

	ipargs=""
	net=""
	if [ -n "${FC_NET:-}" ]; then
		gw=10.230.0.1
		addr=10.230.0.$((10 + FC_NET))
		ipargs=" ip=$addr::$gw:255.255.255.0::eth0:off"

		# The rootfs has no /etc/resolv.conf and is read-only: copy /etc to a tmpfs and add one.
		init="mkdir /tmp/etc; cp -a /etc/. /tmp/etc/; echo nameserver 1.1.1.1 >/tmp/etc/resolv.conf; mount --bind /tmp/etc /etc; $init"
		net=$(printf ',\n  "network-interfaces": [{"iface_id": "eth0", "host_dev_name": "fc-tap%s", "guest_mac": "06:00:0a:e6:00:%02x"}]' "$FC_NET" "$((10 + FC_NET))")
	fi
	init="mount -t proc proc /proc; mount -t tmpfs tmpfs /tmp; $init"

	cat >"$dir/vm.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$fc_kernel",
    "boot_args": "console=ttyS0 reboot=k panic=1 ro quiet$ipargs init=/bin/sh -- -c \"$init\""
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$fc_rootfs", "is_root_device": true, "is_read_only": true},
    {"drive_id": "work", "path_on_host": "$dir/work.ext4", "is_root_device": false, "is_read_only": false}
  ],
  "machine-config": {"vcpu_count": $2, "mem_size_mib": $3},
  "vsock": {"guest_cid": 3, "uds_path": "$dir/v.sock"}$net
}
EOF
}

# fc_proxy <run dir> <key file>: start vsock-to-anthropic where guest vsock port 8080 arrives
# (<uds_path>_8080). It is stopped when the calling script exits.
fc_proxy() {
	"$fc_root/bin/vsock-to-anthropic" -listen "$1/v.sock_8080" -key-file "$2" 2>"$1/proxy.log" &
	fc_proxy_pid=$!
	trap 'kill $fc_proxy_pid 2>/dev/null || true' EXIT
	sleep 0.3
}

# fc_run <run dir> <timeout seconds>: boot the VM; the guest console goes to <run dir>/console.log.
# The VM stops when the guest's command ends.
fc_run() {
	timeout "$2" firecracker --no-api --config-file "$1/vm.json" >"$1/console.log" 2>&1 || true
}

# fc_copy_out <run dir> <path on the work disk> <host dir>: copy a directory off the work disk
# once the VM has stopped. debugfs keeps the guest's owners; chown makes the files ours (root in a
# rootless container is this user).
fc_copy_out() {
	mkdir -p "$3"
	podman run --rm --network none --volume "$1:/run" --volume "$3:/dest" "$fc_tools" \
		sh -c "debugfs -R 'rdump $2 /dest' /run/work.ext4 >/dev/null 2>&1; chown -R 0:0 /dest"
}
