#!/usr/bin/env bash
# Install, check or remove the network for Firecracker guests (D44) on this machine. Run it as root
# from the repository, in a terminal of your own (sudo asks for a password):
#
#   sudo deploy/network/install.sh            install or update, start, print the state
#   sudo deploy/network/install.sh status     print the state only
#   sudo deploy/network/install.sh uninstall  stop, remove the unit, the script and the settings
#
# The taps are owned by the account that ran sudo (SUDO_USER), or by FC_OWNER if set. An existing
# /etc/sandbox-agent/network.env is kept, except for FC_OWNER when it is empty.
#
# Not on the board without the owner's agreement: it adds firewall rules next to Docker's.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
libexec=/usr/local/libexec/sandbox-agent/fc-net
envfile=/etc/sandbox-agent/network.env
unit=/etc/systemd/system/sandbox-agent-net.service

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root: sudo $0 ${1:-}" >&2
	exit 1
fi

cmd_status() {
	echo "== unit"
	systemctl --no-pager --lines=5 status sandbox-agent-net || true

	echo
	echo "== bridge, taps, rules"
	"$libexec" status || true

	echo
	echo "== ufw"
	if command -v ufw >/dev/null; then ufw status verbose || true; else echo "ufw not installed"; fi

	echo
	echo "== forwarding"
	sysctl net.ipv4.ip_forward
}

cmd_install() {
	owner=${FC_OWNER:-${SUDO_USER:-}}
	if [ -z "$owner" ] || [ "$owner" = root ]; then
		echo "cannot tell which account owns the taps: run through sudo, or set FC_OWNER" >&2
		exit 1
	fi
	id "$owner" >/dev/null

	install -D -m 0755 "$here/fc-net.sh" "$libexec"

	if [ ! -f "$envfile" ]; then
		install -D -m 0644 "$here/network.env" "$envfile"
	fi
	if grep -q '^FC_OWNER=$' "$envfile"; then
		sed -i "s/^FC_OWNER=\$/FC_OWNER=$owner/" "$envfile"
	fi

	install -m 0644 "$here/sandbox-agent-net.service" "$unit"
	systemctl daemon-reload
	systemctl enable sandbox-agent-net >/dev/null

	# restart, not start: picks up a newer fc-net or settings on a second run.
	systemctl restart sandbox-agent-net

	echo "installed; taps owned by $(grep '^FC_OWNER=' "$envfile" | cut -d= -f2)"
	echo
	cmd_status
}

cmd_uninstall() {
	systemctl disable --now sandbox-agent-net 2>/dev/null || true
	rm -f "$unit" "$libexec" "$envfile"
	rmdir --ignore-fail-on-non-empty "$(dirname "$libexec")" "$(dirname "$envfile")" 2>/dev/null || true
	systemctl daemon-reload
	echo "removed"
}

case "${1:-install}" in
# Functions are prefixed: one named "install" would call itself instead of install(1).
install | status | uninstall) "cmd_${1:-install}" ;;
*)
	echo "usage: sudo $0 [install|status|uninstall]" >&2
	exit 2
	;;
esac
