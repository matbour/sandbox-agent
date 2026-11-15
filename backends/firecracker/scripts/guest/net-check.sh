#!/bin/sh
# Guest side of backends/firecracker/scripts/net-check.sh. Prints one line per check, prefixed for the host to grep.
set -u

ip link set lo up

# ip= on the kernel command line needs CONFIG_IP_PNP; say so if the kernel ignored it.
if ip -4 addr show eth0 | grep -q 'inet '; then
	echo "net: address from the kernel command line (ip=)"
else
	echo "net: ip= ignored by the kernel, configuring eth0 by hand"
	ip link set eth0 up
	ip addr add "$(sed -n 's/.* ip=\([^:]*\):.*/\1/p' /proc/cmdline)/24" dev eth0
	ip route add default via 10.230.0.1
fi

echo "addr: $(ip -4 -o addr show eth0 | awk '{print $4}')"
echo "route: $(ip route | grep default)"
echo "ping: $(ping -c 1 -W 2 10.230.0.1 >/dev/null 2>&1 && echo gateway ok || echo gateway unreachable)"
echo "dns: $(nslookup api.anthropic.com 2>&1 | awk '/^Address/ && !/:53$/ {print $2; exit}')"

cd /tmp
bun -e '
try {
  const r = await fetch("https://api.anthropic.com/v1/messages", {method: "POST"});
  console.log("https: api.anthropic.com answered", r.status);
} catch (e) {
  console.log("https: failed", e.message);
}
'
