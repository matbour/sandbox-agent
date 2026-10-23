# Root filesystem of the agent's sandbox: the programs the agent runs, without any secret.
#
# Built rootless with Podman and turned into a disk image by scripts/build-rootfs.sh
# (docs/rootfs.md). Alpine (musl) keeps the base small; Claude Code and Bun both publish musl
# builds. Every download is pinned to a version and a checksum, for amd64 and arm64.

ARG ALPINE=docker.io/library/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Stage 1: download and check. Its tools (curl, unzip) never reach the final image.
FROM $ALPINE AS fetch

# Set by podman build --platform.
ARG TARGETARCH

ARG BUN_VERSION=1.4.3
ARG BUN_SHA256_AMD64=6e9ea98554ea0d05efda5840ae292618aaa3adefd66b0853520c5c86a37eac9c
ARG BUN_SHA256_ARM64=8de8a0003cc2707bb555887e3d450e24e1edf3f605f491393e89ed61a8853420

# The npm package @anthropic-ai/claude-code only wraps one native binary per platform; take the
# platform package directly. Checksums are the registry's sha512 integrity, in hex.
ARG CLAUDE_CODE_VERSION=2.1.322
ARG CLAUDE_SHA512_AMD64=4e66fb0b4c401552dee29581988c6501fba5cac5df76a800775186a740d6ac6c58f58679ba221aab6e4679d741c41f487cd7d6ad905c594da0f5291be2989acd
ARG CLAUDE_SHA512_ARM64=95c265e681f846d515a7bb914a98ff8eb5ee6d70eb2b5bfada5b79d25c806882c7e7024cfe9b1a4d3945f04f9a14615dd0c4fb9c68a7ca98d86a9801f3c661be

RUN apk add --no-cache curl unzip

RUN case "$TARGETARCH" in \
      amd64) bun=x64;     bun_sum="$BUN_SHA256_AMD64"; claude=x64;   claude_sum="$CLAUDE_SHA512_AMD64" ;; \
      arm64) bun=aarch64; bun_sum="$BUN_SHA256_ARM64"; claude=arm64; claude_sum="$CLAUDE_SHA512_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /out/usr/local/bin \
 && curl -fsSLo /tmp/bun.zip "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${bun}-musl.zip" \
 && echo "$bun_sum  /tmp/bun.zip" | sha256sum -c - \
 && unzip -q /tmp/bun.zip -d /tmp/bun \
 && install -m 0755 "/tmp/bun/bun-linux-${bun}-musl/bun" /out/usr/local/bin/bun \
 && ln -s bun /out/usr/local/bin/bunx \
 && curl -fsSLo /tmp/claude.tgz "https://registry.npmjs.org/@anthropic-ai/claude-code-linux-${claude}-musl/-/claude-code-linux-${claude}-musl-${CLAUDE_CODE_VERSION}.tgz" \
 && echo "$claude_sum  /tmp/claude.tgz" | sha512sum -c - \
 && tar -xzf /tmp/claude.tgz -C /tmp \
 && install -m 0755 /tmp/package/claude /out/usr/local/bin/claude

# Stage 2: the root filesystem.
FROM $ALPINE

# bash for the agent's shell tool; libstdc++ and libgcc for the Bun-built binaries (Bun, Claude
# Code); ripgrep because Claude Code's bundled one is not used on musl; curl for the agent and the
# conformance suite's reachability checks.
RUN apk add --no-cache bash ca-certificates curl git libgcc libstdc++ ripgrep

COPY --from=fetch /out/ /

# The agent runs as an unprivileged user, in /workspace.
RUN adduser -D -u 1000 -s /bin/bash agent \
 && mkdir /workspace && chown agent:agent /workspace

ENV DISABLE_AUTOUPDATER=1 USE_BUILTIN_RIPGREP=0
WORKDIR /workspace
USER agent
