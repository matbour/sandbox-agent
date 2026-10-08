#!/bin/sh
# Inner loop of scripts/bench-install.sh, run inside the sandbox being measured.
#
#   bench-install-guest.sh <workspace> <runs>
#
# <workspace> holds repo/ (the target repository, no node_modules) and cache/ (a filled Bun
# cache). One warm-up install, then <runs> measured ones, each from no node_modules, offline.
# Prints one "run <n> <milliseconds>" line per measured run.
set -eu

workspace=$1
runs=$2

clean() {
	find "$workspace/repo" -name node_modules -prune -exec rm -rf {} +
	sync
}

install() {
	cd "$workspace/repo"
	bun install --frozen-lockfile --offline --cache-dir "$workspace/cache" >/dev/null 2>&1
}

# Busybox date has no %N; the kernel's uptime counter has 10 ms resolution, enough for runs of
# several seconds.
now_ms() {
	awk '{ printf "%d", $1 * 1000 }' /proc/uptime
}

clean
install

i=1
while [ "$i" -le "$runs" ]; do
	clean
	start=$(now_ms)
	install
	end=$(now_ms)
	echo "run $i $((end - start))"
	i=$((i + 1))
done

clean
