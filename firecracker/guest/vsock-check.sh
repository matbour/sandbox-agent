#!/bin/sh
# Guest side of firecracker/vsock-check.sh: start loopback-to-vsock, then call the egress proxy twice
# from the guest, once on an allowed path and once on a path it must refuse.
set -u
w=/tmp/w

ip link set lo up
$w/run/loopback-to-vsock 2>/tmp/loopback-to-vsock.log &
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
