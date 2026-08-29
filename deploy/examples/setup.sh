# Runs inside the sandbox, as the agent user, in the workspace, before the agent starts.
# agentd runs it with `bash -euo pipefail -c`. The agent user has passwordless sudo in the VM.
# Example for a Bun repository.
#
# Bun is fetched from the npm registry rather than with the official installer, which downloads
# from github.com: this keeps working if github.com is denied in the sandbox (DENY_NETWORK).

if ! command -v bun >/dev/null; then
  case "$(uname -m)" in
    aarch64) pkg=bun-linux-aarch64 ;;
    x86_64) pkg=bun-linux-x64 ;;
    *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
  esac
  # Version: BUN_VERSION, else package.json "packageManager": "bun@x.y.z", else the latest.
  version="${BUN_VERSION:-$(python3 -c 'import json; pm=json.load(open("package.json")).get("packageManager",""); print(pm.split("@",1)[1] if pm.startswith("bun@") else "")' 2>/dev/null || true)}"
  version="${version:-$(curl -fsSL https://registry.npmjs.org/bun/latest |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')}"
  tmp="$(mktemp -d)"
  curl -fsSL "https://registry.npmjs.org/@oven/$pkg/-/$pkg-$version.tgz" | tar -xz -C "$tmp"
  sudo install -m 0755 "$tmp/package/bin/bun" /usr/local/bin/bun
  sudo ln -sf /usr/local/bin/bun /usr/local/bin/bunx
  rm -rf "$tmp"
fi
bun --version

if [ -f package.json ]; then
  bun install --frozen-lockfile || bun install
fi
