#!/bin/sh
# Inner loop of bench/checks.sh, run inside the sandbox being measured.
#
#   checks-guest.sh <workspace> <runs>
#
# <workspace> holds repo/ (the target repository, no node_modules) and cache/ (a filled Bun
# cache). Installs once, offline, then runs each check <runs> times. Prints one
# "check <name> <run> <milliseconds> <exit code>" line per run.
set -u

workspace=$1
runs=$2

# Busybox date has no %N; the kernel's uptime counter has 10 ms resolution.
now_ms() {
	awk '{ printf "%d", $1 * 1000 }' /proc/uptime
}

# Build caches that commands leave in the repository (`front` has `incremental: true`). Removed
# before each run, so that every run starts cold like an agent in a fresh sandbox, and at the end,
# so that the shared input stays as prepared.
clean_caches() {
	find "$workspace/repo" -name node_modules -prune -o -name '*.tsbuildinfo' -exec rm -f {} +
}

measure() { # name, then the command, run from the repository root
	name=$1
	shift
	i=1
	while [ "$i" -le "$runs" ]; do
		clean_caches
		start=$(now_ms)
		(cd "$workspace/repo" && sh -c "$*") >/dev/null 2>&1
		rc=$?
		end=$(now_ms)
		echo "check $name $i $((end - start)) $rc"
		i=$((i + 1))
	done
}

cd "$workspace/repo"
bun install --frozen-lockfile --offline --cache-dir "$workspace/cache" >/dev/null 2>&1 || echo "install failed"

# What an agent runs to check a change in the front-end app. A few tests fail without the app's
# environment variables (exit code 1): the time is what is measured.
measure lint 'bunx --bun biome check .'
measure tsc-front 'cd apps/front && bunx --bun tsc --noEmit'
measure test-front 'cd apps/front && bun test'

clean_caches
find "$workspace/repo" -name node_modules -prune -exec rm -rf {} +
sync
