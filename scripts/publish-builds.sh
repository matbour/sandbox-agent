#!/bin/sh
# Publish the binaries `make cross` built to the `builds` branch, as one orphan commit replacing
# the previous one (D50). Run by .github/workflows/deploy.yml from the repository's checkout.
#
#   scripts/publish-builds.sh <source commit>
#
# The commit holds linux-amd64/ and linux-arm64/ (every binary of bin/ and bin/linux-arm64/),
# SHA256SUMS over them, and COMMIT, the source commit. deploy/update/update.sh reads it on the
# board. Only the last build is kept: the branch is force-pushed.
set -eu

src=${1:?usage: publish-builds.sh <source commit>}

stage=$(mktemp -d)
index=$(mktemp)
trap 'rm -rf "$stage" "$index"' EXIT

host=$(go env GOHOSTARCH)

for arch in amd64 arm64; do
	from=bin
	[ "$arch" = "$host" ] || from=bin/linux-$arch

	mkdir "$stage/linux-$arch"
	find "$from" -maxdepth 1 -type f -perm -u+x -exec cp {} "$stage/linux-$arch/" \;
done

echo "$src" >"$stage/COMMIT"
(cd "$stage" && find linux-* -type f | sort | xargs sha256sum >SHA256SUMS)

# Build the commit from the staging directory without touching the checkout: its own index, the
# staging directory as work tree, no parent.
rm -f "$index"
GIT_INDEX_FILE=$index git --work-tree="$stage" add -A .
tree=$(GIT_INDEX_FILE=$index git write-tree)

commit=$(GIT_AUTHOR_NAME="github-actions[bot]" GIT_AUTHOR_EMAIL="41898282+github-actions[bot]@users.noreply.github.com" \
	GIT_COMMITTER_NAME="github-actions[bot]" GIT_COMMITTER_EMAIL="41898282+github-actions[bot]@users.noreply.github.com" \
	git commit-tree "$tree" -m "build: binaries of $src")

git push --force origin "$commit:refs/heads/builds"

echo "published $commit (source $src):"
cat "$stage/SHA256SUMS"
