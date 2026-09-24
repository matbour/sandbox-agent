#!/bin/sh
# Build the agent's root filesystem as an ext4 disk image, without root (docs/rootfs.md).
#
#   scripts/build-rootfs.sh [amd64|arm64]
#
# 1. podman build: run images/agent/Dockerfile rootless.
# 2. podman export: flatten the image into one tar; files keep their owner (root) in it.
# 3. mkfs.ext4 -d: write the tar into a new ext4 image, owners included.
#
# Output in build/<arch>/: rootfs.tar and rootfs.ext4.
set -eu

arch=${1:-}
if [ -z "$arch" ]; then
	case $(uname -m) in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
	*) echo "unknown host architecture: $(uname -m)" >&2; exit 1 ;;
	esac
fi
case $arch in
amd64 | arm64) ;;
*) echo "usage: $0 [amd64|arm64]" >&2; exit 2 ;;
esac

root=$(cd "$(dirname "$0")/.." && pwd)
out=$root/build/$arch
image=localhost/sandbox-agent-rootfs:$arch
mkdir -p "$out"

podman build --platform "linux/$arch" --tag "$image" "$root/images/agent"

container=$(podman create --platform "linux/$arch" "$image")
trap 'podman rm --force "$container" >/dev/null' EXIT
podman export --output "$out/rootfs.tar" "$container"

# Create the file system with room to spare, then shrink it to the smallest size that holds the
# files: the image is read-only in the guest, so free space would be wasted.
tar_bytes=$(stat -c %s "$out/rootfs.tar")
size_mib=$((tar_bytes * 12 / 10 / 1048576 + 64))

files=$(tar -tf "$out/rootfs.tar" | wc -l)

# Read-only, so no journal (-O ^has_journal), no blocks reserved for root (-m 0), and inodes for
# the files that are there plus 10 % (-N).
rm -f "$out/rootfs.ext4"
mkfs.ext4 -q -L rootfs -m 0 -O ^has_journal -N $((files + files / 10 + 64)) \
	-d "$out/rootfs.tar" "$out/rootfs.ext4" "${size_mib}M"

e2fsck -fy "$out/rootfs.ext4" >/dev/null
resize2fs -M "$out/rootfs.ext4" 2>/dev/null

# resize2fs shrinks the file system but leaves the file at its old length: cut it to match.
blocks=$(dumpe2fs -h "$out/rootfs.ext4" 2>/dev/null | sed -n 's/^Block count: *//p')
block_size=$(dumpe2fs -h "$out/rootfs.ext4" 2>/dev/null | sed -n 's/^Block size: *//p')
truncate -s $((blocks * block_size)) "$out/rootfs.ext4"

echo "$out/rootfs.ext4 ($((blocks * block_size / 1048576)) MiB)"
