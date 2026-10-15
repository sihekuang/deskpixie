# pix: control agents you start yourself — #89

**Status:** design approved by the owner in conversation, 2026-10-15; this written spec awaits review.

## Goal

Let Pixie's coordinator (Bit) see, message and interrupt Claude Code and Codex sessions that **you** start
yourself — in WebStorm's terminal, iTerm, VS Code, anywhere with a shell — next to the workers Pixie
spawns in Herdr or tmux. You start a session with `pix claude …` (or `pix codex …`) instead of plain
`claude`, and it behaves exactly like plain `claude` in that terminal, while Pixie can also reach it.

Non-goals:
- Agents an IDE starts by itself (JetBrains AI chat / ACP mode) — no shell runs there, so `pix` can't.
- Replacing Herdr or tmux for Pixie's own workers.
- A2A. Nothing outside DeskPixie calls in yet; see "Why this design".
- Other machines. The design leaves room (see "Later"), but this spec is one Mac.

## Two kinds of sessions

| | Pixie-started (workers, the coordinator) | You-started (`pix`) |
|---|---|---|
| Started by | Bit, in Herdr or tmux | You, in any terminal |
| Lives in | The Herdr/tmux session (`agent.sessionHost`) | The terminal tab you ran `pix` in |
| Bit may prompt and interrupt | Yes | Yes |
| Bit may close | Yes | Only after asking you |
| Quitting DeskPixie | Kept or closed per `agent.keepPanesOnQuit` (today's behaviour, unchanged) | **Never** ends it; it just loses Pixie until the app is back |
| Closing its terminal / quitting the terminal's app | — | Ends `pix` and the agent, like plain `claude` |

Both kinds appear in one list for Bit, so you can say "tell the WebStorm one to stop" or "what's the
login-bug session doing?" whichever kind it is.

## Why this design

The documentation (below) carries this section for users; it records why the obvious alternatives lost.

1. **Every way into a running agent is local and vendor-specific.** Claude has an inbox socket and hooks;
   Codex has hooks and its app-server; both can be typed into. Nothing standard reaches a session that
   something else started. Research of the field (agent-deck, hcom, a2abridge, open-agent-bridge,
   agent-bridge, Claude Squad and others) found the tools that deliver reliably all **own the launch**:
   they start the agent in their own tmux pane or PTY.
2. **A wrapper owns the launch without owning the window.** `pix` puts a PTY between your terminal and
   the agent. That gives what Herdr gives Pixie's workers — typing, Escape, status via hooks, knowing
   when it exits — in any terminal, with no tmux. hcom proves the approach (`hcom claude`).
3. **Per-run, not global.** `pix` adds its hooks and MCP server on the agent's command line for that one
   run. It never edits `~/.claude.json`, `~/.claude/settings.json` or `~/.codex/config.toml`, so plain
   `claude` is untouched and uninstalling leaves nothing behind (the README's promise: DeskPixie never
   installs hooks for you).
4. **Not global MCP alone** (a2abridge's model): an MCP server can't wake an idle agent or interrupt it,
   and messages wait for your next prompt. Bit needs both.
5. **Not A2A.** Every link here is DeskPixie to DeskPixie, so a private protocol over a local socket is
   simpler. A2A doesn't help with the hard parts (idle delivery, interrupting, status); hcom, the most
   complete cross-vendor tool, doesn't use it either. A2A becomes useful only when something outside
   DeskPixie needs to call in — then it's an adapter on the daemon, not a change to `pix`.
6. **Not instead of Herdr.** A wrapper's PTY dies with its tab. Pixie's workers have no tab, need to run
   in the background, be watched and reattached, and have a real screen to read — a multiplexer's job.

## Components

```
WebStorm terminal tab (a normal PTY)
  └─ pix claude …                          core/cmd/pix
       ├─ PTY ── claude (per-run hooks → `pix hook …`, MCP → `pix mcp`)
       ├─ local socket $PIX_SOCK  ◀── `pix hook …` and `pix mcp` (children of the agent)
       └─ control connection ──▶ <app_data>/agent/pix.sock
                                     │
DeskPixie daemon (libdeskpixie)      ▼
  wrapper host (core/pixhost) ── CompositeHost ── primary host (herdr | tmux)
                                     │
                               controller / Bit (unchanged: one SessionHost)
```

### `pix` (new binary, `core/cmd/pix`)
One Go binary, subcommands:
- `pix <tool> [args…]` — run the agent (`claude`, `codex`) wrapped. Flags before the tool:
  `--description "…"`. Everything after the tool goes to it unchanged.
- `pix hook <event>` — hook command (per-run hooks call it); reads the hook JSON on stdin and
  forwards it to the parent `pix` over `$PIX_SOCK`; prints the hook's reply (e.g. queued messages).
- `pix mcp` — stdio MCP server with one tool, `set_description(text)`; forwards to `$PIX_SOCK`.
- `pix version`.

Wrapping:
- Opens a PTY (`creack/pty`), puts your terminal in raw mode (`golang.org/x/term`), copies both ways,
  forwards `SIGWINCH` resizes. No screen emulation: bytes pass through untouched.
- Starts the agent with per-run additions:
  - Claude: `--settings <temp file>` with `UserPromptSubmit`, `PostToolUse`, `Stop`, `Notification`
    hooks → `pix hook <event>`; `--mcp-config <temp file>` for `pix mcp`; `--append-system-prompt`
    with one line: "When your task changes, call set_description with a few words describing it."
  - Codex: `-c` overrides for the same hooks (where Codex supports them), `mcp_servers.pixie` and the
    instruction line.
  - Env: `PIX_SOCK` (its local socket), `PIX_SESSION` (marks a wrapped session).
- **Nested `pix`** (`PIX_SESSION` or a Pixie worker token already set): exec the tool directly, no
  registration, so nothing is listed twice.
- Exit: exits with the agent's code. A hangup (tab closed, terminal app quit) is passed to the agent,
  which gets ~2 s before it is killed; then `pix` reports `exited` and leaves. `pix` never daemonizes,
  never outlives its tab, and has no keep-running mode.

### Daemon side: wrapper host (`core/pixhost`, new) and CompositeHost
- Listens on `<app_data>/agent/pix.sock` (0600; the file permission is the auth). Newline-delimited
  JSON, versioned (`"v":1`); a `pix` with another version is refused with a message `pix` shows in its
  tab title. App and `pix` ship together, so this only happens with a stale symlink.
- Implements `agent.SessionHost` for registered sessions:

| Method | Wrapper host |
|---|---|
| `Prompt` | Sent to that `pix` (delivery below) |
| `SendKeys` | Sent to that `pix`, written to the PTY (`Escape`, `Enter`) |
| `Close`, `CloseExact` | Sent to that `pix`: hangup to the agent. Only reached after you confirm (rules below) |
| `List` | Registered sessions |
| `Subscribe` | Status (from hooks), description changes, exits |
| `Read` | `HostError` unsupported (no screen emulation) |
| `StartAgent`, `ReplaceAgent` | `HostError` unsupported — you start these |
| `EnsureSession` | No-op |
| `AttachCommand` | `nil` (nothing to attach to) |

- **CompositeHost** (`core/agent`, new) wraps the primary host (Herdr or tmux, as today) and the
  wrapper host, and is what `agentapp` hands the controller, so the controller still sees one
  `SessionHost`. Start/Replace/EnsureSession/AttachCommand go to the primary; per-agent calls go to
  whichever host owns the name; `List` and `Subscribe` merge both. Optional traits (`Focuser`,
  `Labeler`, `StatusReporter`) pass through to the owning host when it has them.
- `AgentInfo` gains `Origin` (`pixie` | `user`), `Description`, `PastDescriptions`, `Where`
  (terminal/IDE), `Repo`, `Cwd`, `Tool`.

## Naming

- **Address** (what routing uses; never changes for a session's life): assigned by the daemon at
  registration, valid per `IsWorkerName`, drawn from the **same taken-name pool as workers**, so the two
  kinds never collide. Built `<RepoSlug(git root of cwd)>-<tool>`, then `-2`, `-3`, … (e.g.
  `deskpixie-claude`). A re-registration with the same Claude session ID (resume, or reconnect) gets
  its old address back. The address isn't shown to people; the description is.
- **Description** (what you and Bit go by), from three sources, **you > agent > auto**:
  1. **Auto**: on the first `UserPromptSubmit` of new work, the daemon makes one from the prompt with
     `taskWords` (first three content words). Also taken from Claude's session `name` if it has one.
  2. **Agent**: `set_description` via `pix mcp`.
  3. **You**: `pix --description "…"`, or renaming through Pixie ("call the WebStorm one 'login bug'").
     Sticks until you clear it; the agent can't override it.
  - At most 60 characters, one line; the agent's updates are limited to one a minute. The daemon keeps the
    last 5 descriptions so "the login-bug one" still matches after the work moves on.
  - `pix` puts the description in the terminal tab title (OSC 2), like Herdr's tab labels.
- **Where**: detected from the environment — JetBrains (`TERMINAL_EMULATOR=JetBrains-JediTerm`,
  to verify), `TERM_PROGRAM` (`vscode`, `iTerm.app`, `Apple_Terminal`), else `terminal`.
- **How Bit picks a session**: its worker-list tool includes both kinds, with description, origin,
  repo, where, status, last activity and past descriptions. Bit maps your words to an address: one
  match → act; several → ask by description; none → say so. Pixie's voice speaks descriptions and
  repos, never addresses, branches or folders (as `delegation.md` already says for workers).

## Lifecycle

1. **Start.** `pix` starts the agent at once (never waits on the daemon), then registers: tool, pid,
   process start time, cwd, git root, where, Claude session ID when known, `--description`. The daemon
   answers with the address. One long-lived control connection carries commands down and events up.
2. **Hooks and `pix mcp` talk only to their own `pix`** (over `$PIX_SOCK`). `pix` holds the latest
   status and description and the message queue, so nothing depends on the daemon being up.
3. **Daemon down** (app not running, quit, crashed): the agent doesn't notice. `pix` passes through
   and reconnects with backoff (1 s → 30 s), then re-registers and resends status and description; the
   daemon gives back the same address. The daemon keeps no record of you-started sessions on disk —
   `pix` is the source of truth, so nothing goes stale.
4. **Exit.** Agent exits → `pix` reports `exited` (with code), unregisters, exits with that code. Tab
   closed or terminal app quit → as above, after the ~2 s grace.
5. **Connection lost without `exited`** → the daemon marks the session unreachable for ~10 s, then drops
   it; commands to it fail with `HostNoAgent`, which the controller already handles.
6. **Quitting DeskPixie**: the wrapper host just closes its socket. `pix` sessions keep running and
   reconnect when the app is back. Pixie-started sessions follow `agent.keepPanesOnQuit` as today.

## Delivery

`Prompt` to a `pix` session:
1. **Claude: inbox socket.** `pix` reads `~/.claude/sessions/<child pid>.json`, checks `procStart`
   matches the child and `peerProtocol == 1`, and posts the message to `messagingSocketPath` (format in
   the claude-session-registry note: one line, `priority: "next"`). Claude queues it itself; the PTY is
   untouched, so your typing is never disturbed. Works idle or busy.
2. **Otherwise (Codex, or the socket check fails): type into the PTY**, only when:
   - the agent is idle (from hooks), and
   - you haven't typed for ~1.5 s, and you've typed nothing since your last Enter (`pix` sees every
     key you press; any key it can't account for, such as editing keys, counts as "unsure", and it waits).
   Then a bracketed paste and Enter. Until then the message waits in `pix` (up to 20) and the tab title
   shows "📨 1 message from Pixie".
3. **Mid-turn**: the `PostToolUse` hook (`pix hook post-tool`) hands queued messages to the agent as
   added context between tool calls.

Interrupt is `SendKeys(["Escape"])`, written straight to the PTY — what pressing Esc does.

## Rules for you-started sessions (controller)

- `Prompt`, `SendKeys(Escape)`: allowed.
- **Close** (`EfStopWorker`, which today is Escape + close): for `Origin == user` it becomes **interrupt
  only**; closing goes through the existing confirm flow (`confirm.go`) and happens only if you say yes.
- **App shutdown** (`runtime.go` stop-all): skips `Origin == user`.
- Bit doesn't adopt them as job workers automatically; they're addressable agents. Starting work in
  one ("have the WebStorm session fix the tests") is a `Prompt`; its result comes back as status and
  whatever the agent says, not through `report_result` (it has no worker token).

## Errors and edge cases

- Socket post fails (e.g. Claude changed its protocol): fall back to typing, log once with the version.
- Queue full: the `Prompt` fails back to Bit with "busy, 20 messages waiting"; nothing is dropped
  silently.
- Control connection drops mid-command: `HostNoAgent` to the caller.
- Two `pix` on the same Claude session ID: the second registration is refused; that session runs
  without Pixie and its tab title says so.
- Prompt text: control characters stripped except newlines, which go inside the bracketed paste.
- `pix.sock` path over the Unix limit (104 bytes) for very long home paths: fall back to
  `$TMPDIR/deskpixie-<uid>/pix.sock`, which `pix` also tries.

## Bundling and install

- Built by a new `core/Makefile` target (`build-pix`), host arch only like the dylib
  (`mac/project.yml:15`). Plain Go, no cgo.
- `mac/project.yml` copies it into `Contents/Helpers/pix` and signs it like `libdeskpixie.dylib`
  (Developer ID, hardened runtime, timestamp). `scripts/package-mac.sh` (Mach-O check loop) and
  `mac/Makefile` (structural checks) include it. It's notarized with the app; it updates with the app,
  so `pix` and the daemon always match.
- **Settings → "Install pix command"** (opt-in): symlink `~/.local/bin/pix` →
  `<app>/Contents/Helpers/pix`. Shows installed / not installed / link broken (app moved) and
  "Uninstall pix command". If `~/.local/bin` isn't on `PATH`, shows the line to add to `.zshrc`.
  A copy button for the optional aliases `alias claude='pix claude'` / `alias codex='pix codex'`.
  Nothing else outside the app is touched.

## Documentation (user-facing)

- `docs/pix.md`: what `pix` is; Pixie-started vs you-started sessions (the table above); install
  and aliases; using it in WebStorm (built-in terminal; the Claude Code plugin if it lets you set its
  command); how naming and descriptions work and how to rename; what Bit may and may not do to your
  sessions; what happens on quit/close; troubleshooting (link broken, `Prompt` waiting, nested `pix`);
  and **why the design is a wrapper** (the "Why this design" section, in user terms).
- README: a short "Sessions you start yourself" section linking to it.
- Settings help text for the install row links to it.

## Testing

- Unit: address allocation and collisions with workers; resume keeps the address; description
  precedence, length, rate limit and history; message queue limits; idle/typing gate; nested-`pix`
  detection; protocol version refusal.
- CompositeHost: routing by owner, merged `List`/`Subscribe`, unsupported methods on the wrapper host.
- Controller: `EfStopWorker` on a user session interrupts only and asks; shutdown skips user sessions.
- The `SessionHost` conformance suite (`agenttest/conformance.go`) against the wrapper host with the
  fake agent, the start step replaced by launching `pix` on it.
- Live smoke test (like `core/tmux/smoketest`): real `pix claude` in a PTY — registers, prompt via the
  socket, Escape interrupts, daemon down and back keeps the address, exit code passes through.

## To verify during implementation

Findings (2026-10-15, Claude Code 2.1.300, Codex 0.162.1):

- **Claude, live** (`go test -tags live -run Live ./pixhost/`): registers; a prompt through the
  inbox socket starts a turn (Claude shows it as "Another Claude session sent a message");
  hooks report working → idle; `set_description` reaches Bit's list; Esc interrupts; the
  address survives a daemon restart; close ends the session.
- **Final review fixes:** close_session is confirmed by the app (a spoken confirm word) for
  Claude and Codex coordinators (a Codex coordinator approves pixie tools itself, and Claude's
  permission hook could not voice an MCP tool); a session id arriving after registering is
  checked for duplicates; only a lone Enter opens the typing gate, and only once the turn
  starts; scripted runs (`-p`, pipes, subcommands) pass straight through. In a real terminal
  Claude draws spaces as cursor moves, so screen markers are matched without spaces.
- **Claude fires no Stop hook on an interrupt.** pix watches the screen for "What should Claude
  do instead?" and reports idle (`TestClaudeInterruptOnScreenMeansIdle`).
- **MCP tools ask for permission by default**, so pix's per-run settings allow
  `mcp__pixie__set_description` (only that).
- **Claude's session record `name`** is a short folder id ("deskpixie-5f"), not a description of
  the work: not used.
- **Codex** accepts pix's `-c` overrides (`mcp_servers.pixie.*`, `developer_instructions`,
  `notify`; checked with `codex mcp list`). Codex status: working from the busy marker on
  screen, idle from `notify` at each turn's end. Run live (`TestLivePixWrapsCodex`): registers,
  a prompt typed through the gate starts a turn, `notify` reports idle, close ends it.
- Not checked here: JetBrains' terminal variables (`TERMINAL_EMULATOR=JetBrains-JediTerm`,
  `__CFBundleIdentifier`), whether the JetBrains Claude Code plugin's command is configurable,
  and `permissions.ask` beating `allow: mcp__pixie` for `close_session` (Claude's documented
  order is deny, then ask, then allow).

## Later (not in this spec)

- `list_agents` / `send` in `pix mcp`, so agents can message each other.
- An A2A adapter on the daemon when an outside caller exists.
- A headless `pix` node for other machines.
