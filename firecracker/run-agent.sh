#!/bin/sh
# Run Claude Code on a task inside a Firecracker microVM with no network device: the model API is
# reached over vsock through forwarder and egress-proxy (docs/microvm.md), the key stays on the
# host.
#
#   firecracker/run-agent.sh <prompt file> [model]
#
# The work disk holds the input prepared by `bench/install.sh prepare` (the target repository at a
# fixed commit and a filled Bun cache). In the guest, guest/agent-setup.sh installs the
# dependencies offline, records a baseline the agent cannot read, and runs guest/agent.sh as the
# `agent` user. The transcript, the diff and the logs end up in build/firecracker/agent/out/.
# Needs `make build`, the amd64 rootfs and secrets/anthropic-api-key (or KEY_FILE).
set -eu

[ $# -ge 1 ] || { echo "usage: $0 <prompt file> [model]" >&2; exit 2; }

. "$(dirname "$0")/lib.sh"

prompt=$1
model=${2:-claude-sonnet-5-5}
key=${KEY_FILE:-$fc_root/secrets/anthropic-api-key}
input=$fc_root/build/bench/input

[ -f "$key" ] || { echo "missing $key" >&2; exit 1; }
[ -d "$input/repo" ] || { echo "run bench/install.sh prepare first" >&2; exit 1; }

vm=$(fc_new agent)
mkdir -p "$vm/run"
cp "$fc_root/bin/forwarder" "$fc_root/firecracker/guest/agent-setup.sh" "$fc_root/firecracker/guest/agent.sh" "$vm/run/"
cp "$prompt" "$vm/run/prompt.md"
echo "$model" >"$vm/run/model"

fc_kernel_fetch
fc_disk "$vm/work.ext4" 8G repo="$input/repo" cache="$input/cache" run="$vm/run"
fc_config "$vm" 2 4096 "sh /tmp/w/run/agent-setup.sh"
fc_proxy "$vm" "$key"

start=$(date +%s)
fc_run "$vm" 1800
echo "VM ran $(($(date +%s) - start)) s"

fc_copy_out "$vm" /out "$vm/guest"
mv "$vm/guest/out" "$vm/out"
rmdir "$vm/guest"
mv "$vm/proxy.log" "$vm/out/"

grep -a -E '^(mark|claude exit|install failed| [0-9]+ files? changed)' "$vm/console.log" | tr -d '\r'
tail -n 1 "$vm/out/transcript.jsonl" | jq -c '{subtype, is_error, num_turns, duration_ms, duration_api_ms, total_cost_usd}' || true
echo "proxy: $(grep -c 'msg=request' "$vm/out/proxy.log") requests, $(grep -c 'msg=rejected' "$vm/out/proxy.log") rejected"
echo "output in $vm/out"
