#!/bin/sh
# Install the latest published binaries on this machine, then restart agentd (D50). Run every
# minute by sandbox-agent-update.timer; also by hand.
#
#   deploy/update/update.sh [--force]
#
# It fetches the `builds` branch (.github/workflows/deploy.yml) with the checkout's own remote and
# deploy key, checks every file against SHA256SUMS, and replaces the binaries in bin/ one by one
# with a rename, so a running agentd keeps its old file and a new start gets the new one. The
# previous binaries stay in bin.prev/ (roll back: copy them back, restart). The checkout itself
# follows main with a fast-forward, for deploy/ and the docs; local changes there stop that part,
# not the binaries.
#
# The restart does not interrupt a job: agentd finishes it first (TimeoutStopSec in the unit).
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
state=${XDG_STATE_HOME:-$HOME/.local/state}/sandbox-agent
arch=linux-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
force=${1:-}

mkdir -p "$state"
cd "$root"

git fetch --quiet --depth=1 origin +refs/heads/builds:refs/remotes/origin/builds

src=$(git show origin/builds:COMMIT)
current=$(cat "$state/deployed" 2>/dev/null || true)

if [ "$src" = "$current" ] && [ "$force" != "--force" ]; then
	exit 0
fi

stage=$(mktemp -d "$state/update.XXXXXX")
trap 'rm -rf "$stage"' EXIT

git archive origin/builds SHA256SUMS "$arch" | tar -x -C "$stage"

# Only this architecture's lines: the others were not extracted.
(cd "$stage" && grep " $arch/" SHA256SUMS | sha256sum -c --quiet -)

# A binary that does not even start (wrong architecture, truncated) stops the update here.
"$stage/$arch/agentd" -h >/dev/null 2>&1 || [ $? -eq 2 ] || {
	echo "update: $arch/agentd of $src does not run" >&2
	exit 1
}

rm -rf bin.prev
mkdir -p bin bin.prev
cp -p bin/* bin.prev/ 2>/dev/null || true

for f in "$stage/$arch"/*; do
	name=$(basename "$f")
	cp -p "$f" "bin/.$name.new"
	mv -f "bin/.$name.new" "bin/$name"
done

echo "$src" >"$state/deployed"
echo "update: installed the binaries of $src (was ${current:-none})"

if ! git merge --quiet --ff-only "$src" 2>/dev/null; then
	git fetch --quiet origin main && git merge --quiet --ff-only "$src" ||
		echo "update: the checkout could not fast-forward to $src; binaries updated anyway" >&2
fi

systemctl --user restart --no-block sandbox-agent
