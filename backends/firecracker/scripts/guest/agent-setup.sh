#!/bin/sh
# Guest side of backends/firecracker/scripts/run-agent.sh, run as root: start loopback-to-vsock, install the
# dependencies offline, record a baseline the agent cannot read, run the agent as the `agent`
# user (agent.sh), then write the diff to out/. Prints "mark <step> <ms since boot>" lines.
set -u
w=/tmp/w
now() { awk '{ printf "%d", $1 * 1000 }' /proc/uptime; }
mark() { echo "mark $1 $(now)"; }

mark start
mkdir -p $w/out
ip link set lo up
$w/run/loopback-to-vsock 2>$w/out/loopback-to-vsock.log &

cd $w/repo
bun install --frozen-lockfile --offline --cache-dir $w/cache >$w/out/install.log 2>&1 || echo "install failed"
mark installed

# Baseline for the diff, in a Git directory the agent cannot read.
export GIT_DIR=$w/base.git GIT_WORK_TREE=$w/repo
git init -q && git add -A && git -c user.name=base -c user.email=base@localhost commit -qm base
chmod 700 $w/base.git
unset GIT_DIR GIT_WORK_TREE
chown -R 1000:1000 $w/repo $w/out
mkdir -p /tmp/home && chown 1000:1000 /tmp/home
mark ready

su -s /bin/sh agent -c "sh $w/run/agent.sh"
mark agent-done

export GIT_DIR=$w/base.git GIT_WORK_TREE=$w/repo
git add -A && git diff --cached >$w/out/diff.patch && git diff --cached --stat | tail -1
sync
