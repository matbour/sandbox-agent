#!/bin/sh
# Install the latest published binaries on this machine, then restart agentd (D50, D56). Run every
# minute by sandbox-agent-update.timer; also by hand.
#
#   deploy/update/update.sh [--force]
#
# It reads COMMIT from the `edge` pre-release (.github/workflows/deploy.yml), and when it names a
# commit not installed yet, downloads this architecture's binaries and SHA256SUMS, checks every
# file, and replaces the binaries in bin/ one by one with a rename, so a running agentd keeps its
# old file and a new start gets the new one. The previous binaries stay in bin.prev/ (roll back:
# copy them back, restart). The repository is public: no credential is needed. The checkout
# itself follows main with a fast-forward, for deploy/ and the docs; local changes there stop that
# part, not the binaries.
#
# SANDBOX_AGENT_REPO (owner/name) overrides the repository read from the checkout's origin.
#
# The restart does not interrupt a job: agentd finishes it first (TimeoutStopSec in the unit).
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
state=${XDG_STATE_HOME:-$HOME/.local/state}/sandbox-agent
arch=linux-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
force=${1:-}

mkdir -p "$state"
cd "$root"

# git@github.com:owner/name.git or https://github.com/owner/name(.git)
repo=${SANDBOX_AGENT_REPO:-$(git remote get-url origin | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')}
base=https://github.com/$repo/releases/download/edge

fetch() {
	curl --fail --silent --show-error --location --retry 2 --max-time 120 -o "$2" "$base/$1"
}

stage=$(mktemp -d "$state/update.XXXXXX")
trap 'rm -rf "$stage"' EXIT

fetch COMMIT "$stage/COMMIT"
src=$(cat "$stage/COMMIT")
current=$(cat "$state/deployed" 2>/dev/null || true)

if [ "$src" = "$current" ] && [ "$force" != "--force" ]; then
	exit 0
fi

# Only this architecture's lines. A release being replaced shows up here as a missing asset or a
# checksum that does not match: the update stops, and the next run tries again.
fetch SHA256SUMS "$stage/SHA256SUMS"
grep -- "-$arch\$" "$stage/SHA256SUMS" >"$stage/sums" || {
	echo "update: no $arch binaries in the release of $src" >&2
	exit 1
}

for asset in $(awk '{print $2}' "$stage/sums"); do
	fetch "$asset" "$stage/$asset"
done

(cd "$stage" && sha256sum -c --quiet sums)

# A binary that does not even start (wrong architecture, truncated) stops the update here.
chmod +x "$stage"/*-"$arch"
"$stage/agentd-$arch" -h >/dev/null 2>&1 || [ $? -eq 2 ] || {
	echo "update: agentd-$arch of $src does not run" >&2
	exit 1
}

rm -rf bin.prev
mkdir -p bin bin.prev
cp -p bin/* bin.prev/ 2>/dev/null || true

for asset in $(awk '{print $2}' "$stage/sums"); do
	name=${asset%-"$arch"}
	cp -p "$stage/$asset" "bin/.$name.new"
	mv -f "bin/.$name.new" "bin/$name"
done

echo "$src" >"$state/deployed"
echo "update: installed the binaries of $src (was ${current:-none})"

if ! git merge --quiet --ff-only "$src" 2>/dev/null; then
	git fetch --quiet origin main && git merge --quiet --ff-only "$src" ||
		echo "update: the checkout could not fast-forward to $src; binaries updated anyway" >&2
fi

systemctl --user restart --no-block sandbox-agent
