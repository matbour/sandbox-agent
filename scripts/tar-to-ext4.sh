#!/bin/sh
# Write a root filesystem tar into a read-only ext4 image, owners included (docs/rootfs.md).
#
#   scripts/tar-to-ext4.sh <rootfs.tar> <rootfs.ext4>
#
# Needs e2fsprogs 1.47.1 or later; scripts/build-rootfs.sh runs it in images/tools.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: $0 <rootfs.tar> <rootfs.ext4>" >&2
	exit 2
fi
tar=$1
image=$2

# Start with room to spare: the tar size plus 20 % for file system metadata.
tar_bytes=$(stat -c %s "$tar")
size_mib=$((tar_bytes * 12 / 10 / 1048576 + 64))
files=$(tar -tf "$tar" | wc -l)

# Read-only, so no journal (-O ^has_journal), no blocks reserved for root (-m 0), and inodes for
# the files that are there plus 10 % (-N).
rm -f "$image"
mkfs.ext4 -q -L rootfs -m 0 -O ^has_journal -N $((files + files / 10 + 64)) \
	-d "$tar" "$image" "${size_mib}M"

# Shrink the file system to the smallest size that holds the files. resize2fs leaves the file at
# its old length: cut it to match.
e2fsck -fy "$image" >/dev/null
resize2fs -M "$image" 2>/dev/null

blocks=$(dumpe2fs -h "$image" 2>/dev/null | sed -n 's/^Block count: *//p')
block_size=$(dumpe2fs -h "$image" 2>/dev/null | sed -n 's/^Block size: *//p')
truncate -s $((blocks * block_size)) "$image"

echo "$image ($((blocks * block_size / 1048576)) MiB)"
