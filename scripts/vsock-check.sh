#!/bin/sh
# Check the egress path of a Firecracker guest (docs/microvm.md): a guest with no network device
# reaches the Anthropic API through forwarder (guest, TCP to vsock) and egress-proxy (host, adds
# the key).
#
#   scripts/vsock-check.sh [key file]
#
# Without a key file, a placeholder key is used: the API then answers 401 "invalid x-api-key",
# which still proves the whole path. A path the proxy does not allow must get 403.
# Needs `make build`, the amd64 rootfs (scripts/build-rootfs.sh) and the kernel that
# scripts/bench-install.sh downloads.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
# Short on purpose: a Unix socket path is limited to 108 bytes.
vm=$root/build/vsock
tools=localhost/sandbox-agent-tools

rm -rf "$vm"
mkdir -p "$vm/disk"

key=${1:-}
if [ -z "$key" ]; then
	key=$vm/placeholder-key
	echo placeholder >"$key"
	chmod 600 "$key"
fi

# The guest's script and the forwarder go on a small second disk.
cp "$root/bin/forwarder" "$vm/disk/"
cat >"$vm/disk/check.sh" <<'EOF'
ip link set lo up
/tmp/w/forwarder 2>/tmp/forwarder.log &
sleep 0.3
cd /tmp
bun -e '
const base = "http://127.0.0.1:8080/anthropic";
const r = await fetch(base + "/v1/messages", {
  method: "POST",
  headers: {"content-type": "application/json", "anthropic-version": "2023-06-01", "x-api-key": "none"},
  body: JSON.stringify({model: "claude-haiku-4-5", max_tokens: 16, messages: [{role: "user", content: "Say hi"}]}),
});
console.log("messages", r.status, await r.text());
const f = await fetch(base + "/v1/files");
console.log("files", f.status, await f.text());
'
EOF
podman run --rm --network none --volume "$vm:/vm" "$tools" mkfs.ext4 -q -d /vm/disk /vm/disk.ext4 64M

init="mount -t proc proc /proc; mount -t tmpfs tmpfs /tmp; mkdir -p /tmp/w; mount /dev/vdb /tmp/w; export HOME=/tmp PATH=/usr/local/bin:/usr/bin:/bin:/sbin; sh /tmp/w/check.sh"

cat >"$vm/vm.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$root/build/amd64/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 ro quiet init=/bin/sh -- -c \"$init\""
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$root/build/amd64/rootfs.ext4", "is_root_device": true, "is_read_only": true},
    {"drive_id": "check", "path_on_host": "$vm/disk.ext4", "is_root_device": false, "is_read_only": true}
  ],
  "machine-config": {"vcpu_count": 1, "mem_size_mib": 512},
  "vsock": {"guest_cid": 3, "uds_path": "$vm/v.sock"}
}
EOF

# Guest connections to vsock port 8080 on the host arrive on <uds_path>_8080.
"$root/bin/egress-proxy" -listen "$vm/v.sock_8080" -key-file "$key" 2>"$vm/proxy.log" &
proxy=$!
trap 'kill $proxy 2>/dev/null || true' EXIT
sleep 0.3

timeout 60 firecracker --no-api --config-file "$vm/vm.json" >"$vm/console.log" 2>&1 || true

echo "guest:"
grep -a -E '^(messages|files) ' "$vm/console.log" | sed 's/\x1b\[[0-9;]*m//g; s/^/  /'
echo "proxy:"
sed 's/^/  /' "$vm/proxy.log"
