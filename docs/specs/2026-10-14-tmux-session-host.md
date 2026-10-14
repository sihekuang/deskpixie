# tmux as a session host — #64

Status: approved by the owner 2026-10-14 (was a ruling in
docs/decisions.md); the owner can redirect. · 2026-10-14

## Goal
Run DeskPixie's agent session (coordinator + workers) in tmux as an alternative to Herdr,
chosen by `agent.sessionHost` = `tmux`. Herdr stays the default and is unchanged.

## Layout
- A private tmux server on its own socket: `tmux -L deskpixie` (socket name = `agent.session`),
  so the user's own tmux sessions are never touched. Started with the same scrubbed environment
  as Herdr's server (`ServerEnv` list: no ANTHROPIC_*/CLAUDE_CODE_*/HERDR_*/provider keys).
- One tmux session named `agent.session`; one **window per agent**, window name = agent name
  (stable across app restarts, so kept workers and the coordinator are adopted by name).
  `remain-on-exit on` so an exited agent's pane stays until DeskPixie reads it and closes it.
- Start: `new-window -d -n <name> -c <cwd> -e K=V … -- <kind> <args…>` (no shell typing, so no
  quoting problems). Pane ids (`%N`) are not reused within a server, so CloseExact is a plain
  id check.
- Prompt: `send-keys -l -- <text>` then `send-keys Enter`; text of several lines is pasted
  bracketed instead (`set-buffer` in chunks + `paste-buffer -p -d`), then Enter. Arguments
  ending in `;` are written `…\;` (tmux reads its argv as commands), and `#` is doubled where
  tmux expands formats (`-c`, `select-pane -T`). Keys: `send-keys Escape|Enter`.
- Read: `capture-pane -p -J -t <pane> -S -<lines>` (plain text; no `-e`, so no ANSI).
- Focus: `select-window`; Relabel: `rename-window` is NOT used (names are identities) — the
  label goes in the pane title (`select-pane -T`) and the window's `@deskpixie_label` option.
- Attach command: `[tmux, -L, <socket>, attach, -t, <session>]`.
- List: `list-panes -s -F '#{window_name}\t#{pane_id}\t#{pane_current_path}\t#{pane_dead}'`;
  dead panes are not listed (and are reported Exited once).

## Agent status (the part Herdr gave us for free)
- **Claude (coordinator and Claude workers): hooks.** Their settings file (already passed with
  `--settings`) gains `UserPromptSubmit` → working, `Stop` → idle, `Notification` (permission /
  idle prompt) → blocked, posted to DeskPixie's hook server with the agent's existing bearer
  token on a new `state` route. Only installed when the session host is tmux (Herdr has its own).
- **Codex workers: screen.** Poll `capture-pane` every 500 ms: busy markers (`BusyMarkers`,
  "esc to interrupt") → working; the ready marker / prompt with no busy marker → idle; approval
  prompt text → blocked. Same markers the controller already uses.
- **Exit: tmux.** Poll `list-panes` (1 s) for `pane_dead=1` / vanished windows → Exited once.
- `StartAgent` readiness: Claude — window up and the screen shows the input prompt (ready
  marker) within the budget; trust dialog visible counts as started (as Herdr 0.9.x).
  Codex — its `Ready` marker.

## Settings and wording
- `agent.sessionHost` choices: Herdr, tmux. `agent.tmuxPath` (empty = PATH, /opt/homebrew/bin,
  /usr/local/bin). Supported: **tmux 3.2 or newer** (`-e` on new-window, `-l` send-keys);
  checked with `tmux -V` → `HostVersionMismatch` naming the supported version.
- Herdr-specific strings become host-neutral: "Agent: <host> not found", "Agent: <host> version
  mismatch: …". `agent.session` help/validation: a-z, 0-9 and - (valid for both hosts).
- No integration hint for tmux (hooks are ours).

## Code
- New `core/tmux`: `Host` (agent.SessionHost + Focuser + Labeler + Shutdown), a `Runner`
  interface over the tmux CLI (fake in tests), a status `Detector` (hook states + screen poll +
  exit poll) emitting `PaneEvent`s with Herdr's dedup rules.
- `agentapp.SessionHosts()` gains `"tmux": newTmuxHost`; `app.go` host list and
  `settings` `hostNames` gain tmux. Move `ProcessRunner`/`PatientRunner` out of `herdr` into a
  neutral package so agentapp doesn't need herdr for git.
- Hook server: new `state` route → `HookState{Agent, Status}` → a PaneInput for that agent.

## Testing
- Unit: tmux `Host` against a fake `Runner` (command lines asserted, list parsing, dead panes,
  version check); Detector tables (hook states, screen samples for Codex, exit once).
- `agenttest.RunConformance` against tmux with a fake agent process (a small script that echoes
  a prompt and toggles a busy marker) on a throwaway socket — runs wherever tmux is installed;
  skipped (not failed) when tmux is missing.
- Live (`-tags live`): `TestLiveTmuxAgentSmoke`, `TestLiveTmuxWorkerSmoke`,
  `TestLiveTmuxDelegationEndToEnd` mirroring the Herdr smokes, on socket `deskpixie-smoke`.
- Requires `brew install tmux` on the owner's machine (owner's OK) for the tmux-backed tests.

## Out of scope
Changing Herdr behaviour; Windows/Linux specifics; tmux control mode (`-CC`).
