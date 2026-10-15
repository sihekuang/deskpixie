# Setup checks — #80

Status: design approved in chat by the owner 2026-10-14; this spec awaits review.
Related: #79 (Open coordinator terminal fails silently), #71 (diagnostics log).

## Problem
Setup problems surface one at a time, only once the coordinator runs into them, as a greyed-out
tray line. The fix is left to a terminal command. On 2026-10-14 the owner's Mac needed three
rounds to get working (a stale Herdr 0.7.5 session server with its binary deleted → Claude
integration hook v7 < v10 → relaunch), and "Open coordinator terminal" did nothing meanwhile.

## Goal
One place, Settings ▸ Setup, that lists every requirement with its state, the reason, and a way to
fix it. It's shown on first launch and reachable from any setup line in the tray. Checks run at
launch, when the page opens, after any fix, and on "Check again".

## Non-goals
- Installing third-party software for the user (no running `brew install` ourselves). We give the
  command to copy.
- A diagnostics log (that's #71).
- Re-architecting how the coordinator disables itself. The existing disable reasons stay; they just
  also feed the check list.

## Checks
Each check has an id, a group, a state (`ok`, `warn`, `fail`, `skipped` — not applicable to the
current settings), a one-line summary, an optional detail, and at most one fix.

| id | group | fail / warn when | fix |
|---|---|---|---|
| `host.installed` | Session host | selected host (herdr / tmux) not found (same lookup as `herdr.FindHerdr` / `tmux.FindTmux`, honouring the configured path) | copy `brew install herdr` / `brew install tmux` |
| `host.version` | Session host | installed version not in the supported range (Herdr 0.7.5 / 0.9.x; tmux ≥ 3.2) | copy `brew upgrade …` |
| `host.server` | Session host | Herdr only: the session's running server speaks a different protocol than the installed client (`HostVersionMismatch`), or its binary is gone | **Restart session** (confirm: lists the session's tabs that will close) |
| `host.integration` | Session host | Herdr only: Claude integration hook missing or older than the client needs (the existing integration check) — warn, since only status detection degrades | **Install hook** (confirm: "runs `herdr integration install claude`, which edits ~/.claude/settings.json and adds ~/.claude/hooks/herdr-agent-state.sh") |
| `claude.installed` | Coordinator | `claude` not found on the app's PATH | copy the Claude Code install command |
| `claude.signedIn` | Coordinator | Claude Code reports not signed in (cheap, non-interactive check; if none exists, `skipped` with a note) | **Open terminal** running `claude` so the user can sign in |
| `coordinator.running` | Coordinator | delegation is disabled or the coordinator isn't up; detail = the existing status line | **Restart coordinator** (no confirm) |
| `codex.installed` | Workers | workers enabled and Codex kind allowed but `codex` not found — warn | copy `brew install codex` |
| `voice.key` | Voice | the selected provider's API key resolves nowhere; detail = where it would come from (Keychain → environment → .env, as #59) | **Open API keys** (Settings section) |
| `perm.mic` | Permissions | microphone not authorized | **Open System Settings** (Privacy ▸ Microphone) |
| `perm.documents` | Permissions | the Documents folder prompt hasn't been granted (the app's existing "Still starting…" state) | **Open System Settings** (Privacy ▸ Files and Folders) |

Checks that don't apply (tmux checks while Herdr is selected, Codex while workers are off, voice
key while voice is off) are `skipped` and collapsed.

## Fix rules
- Every fix is a named action the core runs (`setup.fix <id>`). The shell never builds commands.
- A fix that changes anything outside DeskPixie (the user's Claude settings, a Herdr session and its
  tabs) needs a confirmation that says exactly what it touches. "Restart session" lists the tab
  titles that will close. Fixes inside the app (restart coordinator, open a settings section, open
  System Settings) run directly.
- "Copy command" fixes copy to the clipboard and show the command; they never run it.
- After a fix, its check and every check after it in the same group re-run, then the coordinator
  retries setup if it was disabled (same path as Restart coordinator).
- A fix that fails shows its error on the row; nothing is retried automatically.

## Architecture
- **core/setup (new package):** `Check` (id, group, run func) and `Result` (state, summary,
  detail, fix). `Runner` runs all checks concurrently with a per-check timeout (3 s; a timed-out
  check is `warn` "couldn't check: timed out") and returns results in a fixed display order.
  Checks get their dependencies (command runner, path lookup, settings snapshot, key resolver,
  permission probe) injected, so every check is unit-testable with fakes.
- **Reuse:** `FindHerdr` / `FindTmux`, Herdr version/compat tables and the protocol probe the host
  already does, the existing integration check, the key resolution from #59, and the
  coordinator's disable reason. No second copy of any of them.
- **Bridge:** new calls `setup.check` (returns all results) and `setup.fix {id}` (returns the
  re-checked results or an error), plus a `setup.changed` event when a background run (launch,
  coordinator disable) changes any state.
- **Tray:** whenever any check is `fail`, the tray's status line is the first failing summary plus
  a new enabled item "Open Setup…" under it. This fixes #79 for setup failures: "Open coordinator
  terminal" and "Restart coordinator" stay enabled, and when the coordinator can't start they
  open Setup instead of failing silently.
- **Swift:** a "Setup" sidebar section (first in the list), using the Howl-style layout from #60:
  grouped rows with a state icon, summary, detail and fix button; a "Check again" button with the
  time of the last run. First launch opens Settings on Setup if anything fails.
- **Permissions:** the mic state comes from AVFoundation in the shell and is passed to the core
  with `setup.check`; the Documents state comes from the shell's existing startup probe.

## Error handling
- A check that panics or errors is `warn` "couldn't check: <error>". It never blocks other checks.
- Setup checks never change anything themselves; only an explicit fix does.
- A fix runs at most once at a time (others are disabled while it runs).

## Testing
- **Unit tests per check** with fakes: missing binary; unsupported version; stale Herdr server
  (`HostVersionMismatch`); server binary gone; integration hook old or missing; Claude missing or
  signed out; Codex missing with workers on/off; key missing with the source reported; checks
  skipped for the other host.
- **Runner:** concurrency, timeouts, fixed order, a failing check doesn't affect others.
- **Fixes:** each runs the right action; confirm-required fixes refuse without the confirm flag;
  re-check after a fix; coordinator retry after a fix.
- **Tray:** a failing check shows the "Open Setup…" item; Open coordinator terminal opens Setup
  when the coordinator can't start (#79).
- **Swift:** page rendering for each state, confirm dialog text, Check again.
- **Live** (`-tags live`, smoke session only): a deliberately stale or missing integration hook
  in a temp `HOME`, and Restart session on a smoke session. Never the user's sessions or
  `~/.claude`.
