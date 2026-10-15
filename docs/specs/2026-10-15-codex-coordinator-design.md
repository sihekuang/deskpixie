# Codex as the coordinator (#87)

Status: approved by the owner (build it); this spec records the probe findings and the design.
Claude Code stays the default coordinator, and its launch, files and behaviour do not change.

## 1. Probe findings (Codex 0.162.1, 2026-10-15)

Probed in a private tmux server (`tmux -L dpx-probe`), a temp `CODEX_HOME` and temp folders,
against a local mock Responses API (`model_provider` with `base_url` and
`requires_openai_auth = false`), so no login was needed and nothing of `~/.codex` was used.
Codex's source at tag `rust-v0.162.1` (`codex-rs/hooks`, `codex-rs/config`) was read to explain
what was seen.

Hooks

- Hooks are a stable feature, on by default. Handler types are `command` and `mcp_tool` only;
  there is no `http` type (Claude Code's). A command runs through the shell with the session
  cwd, inherits the Codex process's environment (`$PIXIE_AGENT_TOKEN` expanded), gets the
  payload as JSON on stdin and answers JSON on stdout. So a hook is `curl … --data-binary @-
  http://127.0.0.1:<port>/hooks/<route>`, posting to the existing hook server.
- In the interactive TUI these fired: `SessionStart` (source `startup`), `UserPromptSubmit`,
  `PreToolUse`, `PermissionRequest`, `PostToolUse`, `Stop`. Payloads carry `session_id`,
  `turn_id`, `transcript_path`, `cwd`, `hook_event_name`, `model`, `permission_mode`, plus:
  - `Stop`: `last_assistant_message` (the full reply, `<spoken>` blocks included) and
    `stop_hook_active`. Same field as Claude Code's: `/hooks/stop` works unchanged.
  - `PermissionRequest`: `tool_name`, `tool_input`. A shell command is `tool_name: "Bash"`,
    `tool_input: {command, description}` (description = the model's justification). Same shape
    as Claude Code's Bash.
  - `PostToolUse`: `tool_name`, `tool_input`, `tool_response`, `tool_use_id`. Bash's input is
    `{command}` (no description).
  - Tool names (source, `core/src/tools/hook_names.rs`): shell is `Bash`; edits are
    `apply_patch` (its matcher also answers to `Edit` and `Write`); MCP tools are
    `mcp__<server>__<tool>`, so ours are `mcp__pixie__start_worker` etc.
- The PermissionRequest answer is exactly Claude Code's:
  `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`,
  or `"deny"` with a `"message"`. Verified both ways: allow ran an escalated command with no
  approval UI; deny showed "Blocked by hook" with our message and the model went on. With no
  decision (the hook fails or the server is down) Codex shows its own approval prompt.
- No `Notification` event: Codex has no "waiting on the user" hook.

Hook trust

- Non-managed hooks (user, project, plugin, session flags) run only once trusted. Untrusted
  ones put a "Hooks need review / N hooks are new or changed" screen up at start (Review /
  Trust all / Continue without trusting). Trust is `[hooks.state."<key>"] trusted_hash =
  "sha256:…"` with key `<source file>:<event label>:<group>:<handler>`.
- The hash (source: `hook_hash` + `version_for_toml`) is SHA-256 of the compact,
  key-sorted JSON of `{event_name: <label>, matcher?: …, hooks: [<normalized handler>]}`;
  the normalized command handler is `{type: "command", command, timeout: <secs, default 600>,
  async: false[, statusMessage]}`. Reproduced exactly for the probe's hooks.
- Hook state is read only from the user layer and the **session-flags** layer
  (`hook_states_from_stack`). Hooks given by `-c hooks.<Event>=[…]` live in the session-flags
  layer, whose source file is the synthetic `/<session-flags>/config.toml`.
- So the app can hand Codex its hooks **and** their trust on the command line:
  `-c hooks.Stop=[{hooks=[{type="command",command='…',timeout=5}]}]` plus
  `-c hooks.state={"/<session-flags>/config.toml:stop:0:0"={trusted_hash="sha256:…"}}`.
  Verified: no review screen, the hooks ran, nothing was written to `config.toml`. This trusts
  exactly the app's own hooks for that one process: the user's hooks keep needing their own
  trust, nothing persists, no `--dangerously-bypass-hook-trust` (which would run every hook
  from every layer untrusted), and no managed/requirements file (those need root or MDM).
- The dotted form `-c 'hooks.state."<key>".trusted_hash=…'` does **not** work (the key's dots
  and slashes break the path); the inline-table value does.

Other

- `codex resume --last` resumes the newest interactive session **whose cwd is the current
  folder**; with none it starts a new session (it does not exit). It accepts the same options
  as `codex` (`-c`, `-s`, `-a`, `--add-dir`, `-m`, `--no-daemon`).
- `AGENTS.md` in the cwd is loaded (as "AGENTS.md instructions for <cwd>") even in a folder
  that is neither trusted nor a git repo.
- The folder-trust screen ("Trust this folder?") came only for a git repo; plain folders (the
  app's workspace is one) got none. `-c projects."<dir>".trust_level="trusted"` did not
  suppress it.
- The TUI can attach to the shared app-server daemon (`daemon_auto_start`, `--no-daemon`
  opts out). Hooks and `bearer_token_env_var` need the TUI process's own environment (the
  per-pane token), so the coordinator runs with `--no-daemon`.
- curl 8.7 (macOS) has `--variable %ENV` / `--expand-header`, so the bearer is read by curl from
  the environment and never appears on a command line (`ps`).
- Herdr 0.9 types an agent's command line into the new tab's shell and refuses a line over
  1024 bytes while the terminal is still in canonical mode (before the shell's prompt):
  `agent_start_input_failed: input contains a 1782-byte line but the terminal is in canonical
  mode …; wait for the shell prompt and retry`. The Codex coordinator's line (hooks + trust) is
  ~1.8 KB, so the Herdr host retries that error like `agent_pane_busy` (within
  `PaneBusyLimit`); once the shell's line editor is up it takes the line. tmux runs the
  command directly (no limit). Found by the Herdr live smoke.
- Codex lists MCP tools to the model as a namespace (`{"name":"mcp__pixie","tools":[…]}`); a
  call is a `function_call` with `namespace: "mcp__pixie"`, `name: "start_worker"`.
- Codex models offered (its model list): gpt-6.1-sol, gpt-6-astra, gpt-6-sol, gpt-6-luna,
  gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna.

## 2. Settings

- `agent.coordinatorKind`: `claude` (default) | `codex`, choice "Coordinator agent" on the
  Agent (Coordinator) page, ApplyRestart. Validation: one of the two.
- `agent.model` (Claude Code's `--model`, unchanged) shows only while the kind is `claude`; it
  still applies to Claude workers whatever the coordinator is (hidden fields keep their value).
- `agent.codexModel` (new, nullable): Codex's `-m` for the coordinator, shown only while the
  kind is `codex`, an open choice of "Default (Codex decides)" plus the GPT/Codex list above.
  Codex workers keep using Codex's own default model (unchanged).
- Both use the #85/#86 `visibleWhen` rules; the Mac Settings window needs no new code.

## 3. Launch recipe

`CoordinatorRecipe` gains `Kind` and optional start checks (`Refusal`, `Ready`) and
`ResumeArgs` (how the kind resumes). For Codex, `AgentHost` launches kind `codex` with

    [resume --last]  --no-daemon -s workspace-write -a on-request --add-dir <canvas>
    -c sandbox_workspace_write.network_access=true
    -c check_for_update_on_startup=false
    -c mcp_servers.pixie.url=http://127.0.0.1:<port>/mcp
    -c mcp_servers.pixie.bearer_token_env_var=PIXIE_AGENT_TOKEN
    -c mcp_servers.pixie.default_tools_approval_mode="approve"
    -c hooks.<Event>=[…] (one per hook)   -c hooks.state={…}   [-m <model>]

cwd is the app's workspace, env `PIXIE_AGENT_TOKEN` as for Claude. The sandbox keeps writes to
the workspace and canvas; network is on in the sandbox for parity with the Claude coordinator
(research, `curl`). Anything else needs an escalation, which is a PermissionRequest (§5).
`resume --last` replaces `--continue` (cwd-scoped, so only the coordinator's own sessions).
Since it never exits for want of a session, the "exited at start → start fresh" fallback is
Claude-only in practice (kept as is).

The start then waits like a Codex worker's: Codex's input box (`CodexIdleMarker`) must show for
`CodexReadySettle` with no refusal screen. Refusal screens: Codex's folder trust (should not
happen: the workspace is no repo) and the hooks review screen (it would mean Codex changed its
trust format, or the user's own hooks are untrusted). Both fail the start with a fixed reason
that becomes the tray status ("Coordinator: …"); nothing is typed into them.

## 4. Hooks

Generated in Go (no template file), port and route per hook:

| Event | Route | Timeout |
|---|---|---|
| Stop | /hooks/stop | 5 |
| PermissionRequest | /hooks/permission | 125 |
| PostToolUse (matcher `Bash\|Edit\|Write\|mcp__pixie__start_worker\|mcp__mascot__start_worker`) | /hooks/post-tool | 5 |
| tmux only: UserPromptSubmit | /hooks/state | 5 |
| tmux only: Stop (second group) | /hooks/state | 5 |

Command: `/usr/bin/curl -sS --variable %PIXIE_AGENT_TOKEN --expand-header 'Authorization: Bearer
{{PIXIE_AGENT_TOKEN}}' -H 'Content-Type: application/json' --data-binary @-
http://127.0.0.1:<port>/hooks/<route>`. The hook server's routes and token identity are
unchanged: the token is the coordinator's, so the server knows the caller. The trust hashes
are computed by the same Go code that renders the hooks (a unit test pins one against the
hash Codex itself wrote in the probe).

Narration: `apply_patch` (Codex's edit tool) narrates "editing <file>" from the patch's first
`*** Add/Update/Delete File:` line; Bash without a description says "running a command".

## 5. Status and permissions

- Herdr detects Codex's status itself (as for Codex workers): nothing new.
- tmux: the coordinator reports through the state hooks (UserPromptSubmit → working, Stop →
  idle). The detector treats a hooked Codex agent like a hooked Claude agent (its screen only
  ends a turn the hooks left open); an unhooked Codex agent (workers) is still read off its
  screen. Codex has no Notification hook, so "blocked" comes only from the screen of an
  unhooked agent.
- Turn end: the Stop hook carries the reply (`last_assistant_message`) to `/hooks/stop`, as for
  Claude: `<spoken>` and `<memory>` parsing are unchanged.
- Permissions: Codex's PermissionRequest goes to `/hooks/permission` and through the existing
  permission / confirm-word flow; the answer format is identical. If the app cannot answer
  (server down, e.g. a kept pane after the app quit) Codex falls back to its own approval UI in
  the pane, which the person sees when they open the terminal.

## 6. Prompt (AGENTS.md)

Codex reads `AGENTS.md`, so the workspace also gets `AGENTS.md`: the same coordinator.md text,
with the one Claude-specific rule swapped (`{{ASK_RULE}}`): "Do not ask with
request_user_input; ask with status="question" instead." Claude's `CLAUDE.md` is rendered from
the same template with its current sentence, byte for byte. Both files are always written (the
kind can change at the next start).

## 7. MCP

The same pixie MCP server and token: Pixie's local tools (#78) always, worker tools when workers
are on. Tools are approved for this server only (`default_tools_approval_mode="approve"`), as
for Codex workers. No `--mcp-config` file is written for a Codex coordinator (the bearer comes
from the env).

## 8. Keep panes on quit / adopt

`kept.json` records `coordinatorKind` (omitted for Claude, so old files and Claude runs are
unchanged). A kept coordinator of another kind than the one configured now is not adopted: its
token is dropped and the pane found is replaced by a start of the configured kind (resuming that
kind's last conversation in the workspace, if any).

## 9. Errors and setup statuses

- `Coordinator: codex did not start` (the kind's name) instead of the fixed "claude did not
  start" for a failed start; Claude's text is unchanged.
- A refusal screen (§3) becomes `Coordinator: <reason>` (fixed texts, no screen text).
- Codex not installed: herdr/tmux report the agent exiting at start → "codex did not start".

## 10. Workers' turn end (bonus): not done

Codex workers keep their current screen-based turn detection. Giving them the Stop hook means
adding `--no-daemon` and the hook flags to `CodexWorkerArgs` and reworking the worker state
machine's Codex paths (held Idle/Done, screen reads after the no-report grace,
`SilentReadScreen`), and a PermissionRequest hook would move their approvals from the pane to
the voice flow. Not cheap or safe enough for this change; left as a follow-up.

## 11. Out of scope

- Writing anything into `~/.codex` (folder or hook trust): never needed.
- A setup-wizard step (#80) for Codex login: the coordinator uses the user's normal Codex login.
- Per-kind memory or job history: shared, as today.
- Codex's `--approve-for-me` / guardian auto-review.

## 12. Verification

Unit tests per piece (recipe args, hook rendering and hashes, AGENTS.md, settings, kept kind,
controller status text, tmux detector). Live smokes on herdr and tmux with a Codex coordinator
in a temp `CODEX_HOME`, temp data dir and temp projects folder. A real OpenAI login would mean
copying credentials into that temp home, which the run rules forbid, so the Codex coordinator
runs against a local mock Responses API that scripts its turns (reply, escalated command,
`start_worker` call). That covers launch, hooks, trust, permissions, resume, MCP and delegation
end to end through a real Claude worker; it does not cover a real GPT model's behaviour as a
coordinator.
