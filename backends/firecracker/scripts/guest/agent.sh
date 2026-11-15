#!/bin/sh
# Guest side of backends/firecracker/scripts/run-agent.sh, run as the `agent` user: Claude Code on the task in
# run/prompt.md. The API is reached through loopback-to-vsock; the key is added on the host.
export HOME=/tmp/home PATH=/usr/local/bin:/usr/bin:/bin
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080/anthropic ANTHROPIC_API_KEY=placeholder
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_AUTOUPDATER=1 USE_BUILTIN_RIPGREP=0

cd /tmp/w/repo
claude -p "$(cat /tmp/w/run/prompt.md)" --model "$(cat /tmp/w/run/model)" \
	--dangerously-skip-permissions --output-format stream-json --verbose --no-session-persistence \
	--max-turns 80 --max-budget-usd 3 >/tmp/w/out/transcript.jsonl 2>/tmp/w/out/claude.stderr
echo "claude exit $?"
