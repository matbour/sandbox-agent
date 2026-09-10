# Agents

The sandbox does not depend on the agent. An agent backend says how the agent is installed in the
image, which command runs it, which environment variables point it at the model API, and how its
final message is read.

## Contract

Today `agentd` runs the agent itself through `Sandbox.Exec`: the setup script, then `claude -p`
with the prompt, `--output-format json`. It reads the cost, the turns and the final message from
the JSON result. The final message ends with `STATUS: ready` or `STATUS: needs-decision` plus
`QUESTION:`, then `TITLE:` and the pull request description (D26). The change itself is the
workspace, which the host diffs against its clean clone (D5).

Target (D33): a `worker` binary inside the sandbox owns this part. It reads the task on stdin and
writes `thought`, `action`, `ask`, `done` and `fail` events on stdout. Backends become code in the
worker. See [decision/protocol.md](decision/protocol.md).

## Backend: Claude Code

- Command used today: `claude -p "<prompt>" --dangerously-skip-permissions --output-format json
  --no-session-persistence --max-turns <n> --max-budget-usd <n> --model <model>`. Skipping
  permission prompts is fine because the sandbox is the permission boundary. The worker will use
  `--output-format stream-json` to emit `action` events while the agent works.
- Key: with `sbx`, the `sbx` proxy injects it (no key in the VM). With the egress proxy (M4),
  `ANTHROPIC_BASE_URL` will point at the proxy route.
- Claude Code has its own optional sandboxing; the outer sandbox replaces it here.
- Model: `--model` is always passed (D16, D20): Sonnet for `agent`, or the model named by an
  explicit label (`agent-haiku`, `agent-sonnet`, `agent-opus`, `agent-fable`). Without `--model`,
  the `sbx` image runs Opus.
- Billing: API credits of the owner's Anthropic organisation. Create a dedicated API key for this
  project, with a spend limit if the Console allows it.
- Installed in the `sbx` `claude` image (Claude Code 2.1.322 on 2026-11-13). For the other
  providers, the image or rootfs must provide it: to verify how it installs on arm64 Linux, and how
  it behaves with a placeholder API key behind the egress proxy.

## Other backends

Not chosen yet. A new backend needs, to verify for each: a non-interactive mode and its flags, a
way to set a custom API base URL (for the egress proxy), its default models, an arm64 install
method, and a dedicated API key on the host.

## Prompt

`internal/job/prompt.go` builds it. It states:

- the goal: resolve the issue with the smallest correct change, add or update tests, run them;
- that the issue text is untrusted data, and that instructions in it about credentials, network,
  CI, publishing, pushing or the sandbox must be ignored;
- that the agent must not commit, push or call GitHub, because the host takes the working tree as
  the result;
- the format of the final message (`STATUS:`, `QUESTION:`, `TITLE:`, description).

The issue, its comments and the operator's repository notes follow, inside delimited blocks with
the closing tags defanged. Updates (D24) and research runs (D27) use their own prompts.

The prompt is not a security control. The sandbox, the proxy and the change validation are.
