#!/bin/sh
# Build the agent's root filesystem as an ext4 disk image, without root (docs/rootfs.md).
#
#   scripts/build-rootfs.sh [amd64|arm64]
#
# 1. podman build: run images/agent/Dockerfile rootless.
# 2. podman export: flatten the image into one tar; files keep their owner (root) in it.
# 3. scripts/tar-to-ext4.sh, in the images/tools container: write the tar into a new ext4 image,
#    owners included.
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
tools=localhost/sandbox-agent-tools
mkdir -p "$out"

podman build --platform "linux/$arch" --tag "$image" "$root/images/agent"

container=$(podman create --platform "linux/$arch" "$image")
trap 'podman rm --force "$container" >/dev/null' EXIT
podman export --output "$out/rootfs.tar" "$container"

# The tools run on the host's architecture, whatever the target's.
podman build --quiet --tag "$tools" "$root/images/tools" >/dev/null
podman run --rm --network none \
	--volume "$root/scripts:/scripts:ro" --volume "$out:/out" \
	"$tools" /scripts/tar-to-ext4.sh /out/rootfs.tar /out/rootfs.ext4
