#!/bin/sh
# Check the egress path of a Firecracker guest (docs/microvm.md): a guest with no network device
# reaches the Anthropic API through loopback-to-vsock (guest) and vsock-to-anthropic (host, adds
# the key). The guest side is guest/vsock-check.sh.
#
#   backends/firecracker/scripts/vsock-check.sh [key file]
#
# Without a key file, a placeholder key is used: the API then answers 401 "invalid x-api-key",
# which still proves the whole path. A path the proxy does not allow must get 403.
# Needs `make build` and the amd64 rootfs (scripts/build-rootfs.sh).
set -eu

. "$(dirname "$0")/lib.sh"

vm=$(fc_new vsock-check)

key=${1:-}
if [ -z "$key" ]; then
	key=$vm/placeholder-key
	echo placeholder >"$key"
	chmod 600 "$key"
fi

mkdir -p "$vm/run"
cp "$fc_root/bin/loopback-to-vsock" "$fc_root/backends/firecracker/scripts/guest/vsock-check.sh" "$vm/run/"

fc_kernel_fetch
fc_disk "$vm/work.ext4" 64M run="$vm/run"
fc_config "$vm" 1 512 "sh /tmp/w/run/vsock-check.sh"
fc_proxy "$vm" "$key"
fc_run "$vm" 60

echo "guest:"
grep -a -E '^(messages|files) ' "$vm/console.log" | sed 's/\x1b\[[0-9;]*m//g; s/^/  /'
echo "proxy:"
sed 's/^/  /' "$vm/proxy.log"
