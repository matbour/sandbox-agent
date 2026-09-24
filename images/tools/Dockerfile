# Disk image tools for scripts/build-rootfs.sh, so the build does not depend on the host's
# e2fsprogs: mkfs.ext4 reads a tar with -d only from 1.47.1 (newer than Ubuntu 24.04's) and only
# when built with libarchive (Alpine's is not). Debian 13 ships 1.47.2 with it.

FROM docker.io/library/debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

# libarchive13t64: mkfs.ext4 loads it at run time to read the tar.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends e2fsprogs libarchive13t64 \
 && rm -rf /var/lib/apt/lists/*

# libarchive decodes file names in the tar with the locale: it must be UTF-8.
ENV LANG=C.UTF-8
