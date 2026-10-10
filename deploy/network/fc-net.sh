#!/usr/bin/env bash
# Host network for Firecracker guests, laid out like Docker's default bridge (D44): one bridge with
# the gateway address, one tap per concurrent microVM attached to it, and NAT out of the host. The
# taps are persistent and owned by the account that runs the microVMs, so Firecracker attaches to
# them without any privilege. Run as root by sandbox-agent-net.service; idempotent.
#
#   fc-net.sh up | down | status
#
# Settings (environment, from /etc/sandbox-agent/network.env in the unit):
#   FC_OWNER   account that runs Firecracker (required for up)
#   FC_BRIDGE  bridge name, default fc0
#   FC_SUBNET  first three octets of the /24, default 10.230.0 (the gateway is .1). Outside
#              Docker's default pools (172.17-172.31 and 192.168), which it may take for new networks
#   FC_TAPS    number of taps, default 4 (fc-tap0 … fc-tap3)
#
# Guest N uses fc-tapN, address $FC_SUBNET.(10+N)/24, gateway $FC_SUBNET.1. Taps are isolated
# bridge ports, so guests reach the internet but not each other. No filtering otherwise: guests
# reach the LAN and the host too, which D44 accepts for the development phase.
set -euo pipefail

bridge=${FC_BRIDGE:-fc0}
subnet=${FC_SUBNET:-10.230.0}
taps=${FC_TAPS:-4}

# Docker sets the FORWARD policy to DROP and jumps to DOCKER-USER first, so our accept rules go
# there when Docker is present (ufw's chains come after it too). Without Docker, FORWARD itself.
forward_chain() {
	if iptables -n -L DOCKER-USER >/dev/null 2>&1; then echo DOCKER-USER; else echo FORWARD; fi
}

# rule <add|del> <table> <chain> <rule…>: add a rule once, or delete it if present.
rule() {
	local op=$1 table=$2 chain=$3
	shift 3

	if iptables -t "$table" -C "$chain" "$@" 2>/dev/null; then
		[ "$op" = del ] && iptables -t "$table" -D "$chain" "$@"
	else
		[ "$op" = add ] && iptables -t "$table" -I "$chain" "$@"
	fi
	return 0
}

rules() {
	local op=$1 chain
	chain=$(forward_chain)

	rule "$op" nat POSTROUTING -s "$subnet.0/24" ! -o "$bridge" -j MASQUERADE
	rule "$op" filter "$chain" -i "$bridge" ! -o "$bridge" -j ACCEPT
	rule "$op" filter "$chain" -o "$bridge" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
}

up() {
	: "${FC_OWNER:?FC_OWNER: the account that runs Firecracker}"

	ip link show "$bridge" >/dev/null 2>&1 || ip link add "$bridge" type bridge
	ip addr replace "$subnet.1/24" dev "$bridge"
	ip link set "$bridge" up

	for i in $(seq 0 $((taps - 1))); do
		tap=fc-tap$i
		ip link show "$tap" >/dev/null 2>&1 || ip tuntap add dev "$tap" mode tap user "$FC_OWNER"
		ip link set "$tap" master "$bridge"
		bridge link set dev "$tap" isolated on
		ip link set "$tap" up
	done

	sysctl -qw net.ipv4.ip_forward=1
	rules add
}

down() {
	rules del

	for tap in $(ip -o link show | awk -F': ' '{print $2}' | grep -E '^fc-tap[0-9]+$' || true); do
		ip link del "$tap"
	done
	ip link show "$bridge" >/dev/null 2>&1 && ip link del "$bridge"

	# ip_forward is left as it is: Docker needs it too.
	return 0
}

status() {
	ip -br addr show "$bridge" || true
	bridge link show | grep -E 'fc-tap[0-9]+' || true
	iptables -t nat -S POSTROUTING | grep -F "$subnet.0/24" || true
	iptables -S "$(forward_chain)" | grep -F -- "$bridge" || true
}

case "${1:-}" in
up | down | status) "$1" ;;
*)
	echo "usage: $0 up|down|status" >&2
	exit 2
	;;
esac
