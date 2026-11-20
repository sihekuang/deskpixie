# Resume past pix sessions from History (#461)

## Goal

Pick up a Claude Code conversation you ran through `pix` after it ended, by clicking it in a
list. Clicking first, by hand; Pixie resuming by voice and a `pix resume` command can come later
on the same history.

## Scope

- Claude Code and Codex (#468). pix learns Claude's conversation id from its hooks
  (`session_id`, first at SessionStart; `core/pix/session.go`). Codex's is its thread id:
  the `thread-id` of the notify it runs at each turn's end, and the
  `To continue this session, run: codex resume <id>` line it prints as it exits, which pix
  reads off its output (so a resume closed before any turn still reports it). Codex keeps
  the id when resumed; Claude may fork it under a new one.
- Not under the session chip: History lives in the menu bar, Pixie's right-click menu and
  Settings.

## Part 1: history and where you click it

### What's kept (Go, `core/pixhost`)

One entry per Claude or Codex conversation with a session id:

| Field | From |
|---|---|
| `sessionId` | the `session` event |
| `tool` | register (`claude` or `codex`) |
| `description` | the latest description |
| `cwd`, `gitRoot` | register |
| `where` | register (WebStorm, iTerm, Herdr…) |
| `started`, `lastActive`, `ended` | the host's clock |
| `unresumable` | set when a resume of it fails at once (below) |

- Written on a new session id, a description change and an exit, to
  `<support dir>/agent/pix-history.json` (atomic write: temp file then rename).
- Keyed by `sessionId`: resuming a conversation updates its entry, never adds a second.
- At most 20 entries, the oldest `lastActive` dropped first.
- A damaged or unreadable file is set aside as `pix-history.json.bad` and history starts empty;
  it never stops pix sessions from registering.
- Sessions running now (connected, briefly unreachable, or a resume of the entry) stay listed,
  marked running: worked out when listed, never written to the file. Remove leaves a running
  one, and Clear removes only the ones not running.

The store is its own type behind a small interface (`HistoryStore`: `Record`, `List`, `Remove`,
`Clear`), injected into the pix host, so the host's tests use a fake and the store's tests use
`t.TempDir()`.

### Through the tray model

No calls of its own: History rides on the tray model and its actions.

- `app.state` → `agent.history`: `[{"id", "label", "enabled", "running"}]`, newest first,
  `[]` when there are none. Labels are made in Go: "login bug · deskpixie · 2h ago", or
  "● login bug · deskpixie · running" for one running now. `enabled` is false for an
  unresumable entry (a running one is always enabled).
- `app.action` ids: `agent:history:resume:<sessionId>` (resumes a past one; shows a running one,
  exactly as a click on its row by the session chip does), `agent:history:remove:<sessionId>`
  (ignored for a running one), `agent:history:clear`.
- When History changes (an entry written, a session started or ended), the core refreshes the
  tray, which pushes `app.state` again. An open Settings window redraws from those tray-state
  updates (it reads History from the tray state), not from `settings.changed`, which only
  follows an `app.action`.

### Where you click it (Swift)

- **Menu bar and Pixie's right-click menu:** both are built from the same `TrayItem` tree
  (`TrayContextMenu`), so one **History ▸** submenu shows in both. Rows: "login bug ·
  deskpixie · 2h ago" (description, project folder name, relative time), or "● login bug ·
  deskpixie · running" for one running now, which a click brings forward; then a separator and
  **Clear History**, only when a row that isn't running is listed (Clear keeps running ones).
  Empty: a greyed "No past sessions". An unresumable entry is shown greyed. While the
  coordinator is off, History isn't in the menu.
  The tree is built in `DeskPixieCore` (`TrayMenu`) and tested in `TrayMenuTests`.
- **Settings ▸ Sessions:** a "History" section, hand-drawn in Swift (it is a list, not a
  setting): each row with **Resume** and a remove button, and **Clear History** while a row
  that isn't running is listed. A running row has **Show** instead of **Resume**, and no
  remove button. Empty: "No past sessions". While the coordinator is off (the core sends no
  sessions then) the rows are hidden and it says "History needs the coordinator. Turn on Hand
  work to the coordinator under Coordinator."

### Errors (short, plain)

| Case | Says |
|---|---|
| The folder is gone | "That project folder is gone." |
| Herdr isn't available | "Resuming needs Herdr. Install it, or set its path in Settings ▸ Advanced." |
| Claude no longer has the conversation | Claude's own error shows in the tab; the pix exits at once, so the entry is marked unresumable and the next try says "This conversation can't be resumed." |
| Already running elsewhere | Listed with ●; a click shows it instead of resuming. If it starts running after the menu was drawn, the click shows it too; if a second pix still races in, pix's existing duplicate refusal names it in the tab title. |

## Part 2: starting it in Herdr as your own session

### A Herdr session of its own

Resumed sessions run in a separate Herdr session, `deskpixie-yours` (its name derived from
`agent.session` so an isolated test app gets its own), never the coordinator's. DeskPixie never
reconciles, replaces or stops it, so:

- worker cleanup can't touch these tabs;
- quitting DeskPixie never ends them, whatever **Keep agent panes on quit** says, matching
  "quitting DeskPixie never ends a session you started".

### Resume (Go decides, Swift does)

1. Go ensures `deskpixie-yours` and opens a tab named after the description, in the entry's
   `cwd`, running `<DeskPixie.app>/…/pix claude --resume <sessionId>` (Codex:
   `pix codex resume <sessionId>`; any other tool is refused as unresumable). The app's own pix is
   used, so resuming works without installing the `pix` command.
2. The tab's environment has no `PIXIE_AGENT_TOKEN` (so `pix.Nested` is false and pix wraps
   Claude as usual) and has `PIX_WHERE=Herdr`; `pix.Where` reads it first.
3. pix registers with the host as any session you started: listed under ⌨, Pixie can message
   and interrupt it, and asks before closing it.
4. The resume makes a `FocusPlan` (sent as the `focusPlan` event) that opens the chosen terminal (`agent.terminal`: Warp or
   iTerm) attached to `deskpixie-yours` with that tab in front. Swift's `TerminalFocusExecutor`
   carries it out.

### Bringing it forward later

The session planner gets one rule: a pix session whose `where` is `Herdr` is focused by
attaching to `deskpixie-yours` and selecting its tab; any other keeps today's plan (find the
terminal app by its pid).

### Edge cases

- Closing the Herdr tab ends it like closing a terminal tab; History marks it ended.
- If `deskpixie-yours` was stopped from outside, the next resume starts it again.

## Testing

- **Go:** the store (record, update by id, trim to 20, remove, clear, damaged file, unwritable
  folder); the host recording on session/description/exit and marking running ones; the resume
  plan (argv, folder, environment without the worker token, `PIX_WHERE`), its errors (folder
  gone, no Herdr) and the unresumable mark; `Where` with `PIX_WHERE`; the Herdr focus rule.
  All with `t.TempDir()`, a temporary `HOME` and a fake session host.
- **Swift:** the History submenu (rows, empty, greyed, Clear) and its routing in
  `TrayMenuTests`; the Settings section against a fake client.
- **By hand:** an isolated build (`DESKPIXIE_SUPPORT_DIR`, its own `agent.session` and port):
  run `pix claude`, end it, resume it from each of the three places, check it shows under ⌨,
  focuses from its row, and is still running after quitting DeskPixie. Then clean up the Herdr
  sessions and folders.
