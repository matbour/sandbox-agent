#!/bin/sh
# Publish the binaries `make cross` built as the assets of the `edge` pre-release, replacing the
# previous one (D56). Run by .github/workflows/deploy.yml from the repository's checkout, with
# GH_TOKEN set.
#
#   scripts/publish-release.sh <source commit>
#
# Release assets are flat, so a binary is named <name>-linux-<arch> (agentd-linux-arm64). Next to
# them: SHA256SUMS over them, and COMMIT, the source commit. deploy/update/update.sh reads them on
# the board. Only the last build is kept: the release and its tag are deleted, then created again
# on the source commit, so no asset of an older build stays behind.
set -eu

src=${1:?usage: publish-release.sh <source commit>}
tag=edge

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

host=$(go env GOHOSTARCH)

for arch in amd64 arm64; do
	from=bin
	[ "$arch" = "$host" ] || from=bin/linux-$arch

	for f in $(find "$from" -maxdepth 1 -type f -perm -u+x); do
		cp "$f" "$stage/$(basename "$f")-linux-$arch"
	done
done

(cd "$stage" && sha256sum -- *-linux-* >SHA256SUMS)
echo "$src" >"$stage/COMMIT"

gh release delete "$tag" --yes --cleanup-tag 2>/dev/null || true

gh release create "$tag" "$stage"/* \
	--target "$src" \
	--prerelease --latest=false \
	--title "edge: $(echo "$src" | cut -c1-7)" \
	--notes "Binaries of $src, the last commit of main that passed CI. Replaced on every push; no versioned release yet."

echo "published $tag (source $src):"
cat "$stage/SHA256SUMS"
