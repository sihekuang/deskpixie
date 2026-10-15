# Agent roster: what's running, beside Pixie (#91)

Status: design approved in conversation 2026-10-15; spec for review. Builds on `pix` (#89, PR #90).

## 1. Goal

You can glance at Pixie and know how many agents are running: the ones Pixie started in Herdr or
tmux (the coordinator and its workers) and the sessions you started yourself with `pix` that
Pixie can reach. Hovering shows the list; clicking a row takes you to that agent.

Today the only way to find out is to ask, and the coordinator answers from two tools
(`list_workers`, `list_sessions`); nothing on screen shows either number.

Success: while any worker or session of yours is running, a chip under Pixie shows the two
counts (never counting the coordinator), and they match what is actually running within about a second; hovering lists every agent with its
status; clicking a row brings its terminal forward.

## 2. Decisions (from the design discussion)

- A small **chip under Pixie**, shown while anything is counted; hover or click opens the
  list. Not an always-open list, not part of the updates bubble.
- **The coordinator is never counted.** It is listed first under Pixie's agents, with its
  status, but neither the chip nor the section header counts it. (Bit is Pixie's name, the
  character; the coordinator is the Claude Code or Codex agent behind it. The list says
  "Coordinator".)
- Clicking one of **Pixie's agents** brings its Herdr/tmux tab forward and opens the terminal,
  like clicking an update line (`FocusJob`).
- Clicking one of **your sessions** brings its terminal app forward (WebStorm, iTerm…). It can't
  pick the exact tab inside that app.
- The core pushes the list to the app on change (not polled by the app).

## 3. What it shows

**Chip:** `⚙ 3 · ⌨ 2`. ⚙ is Pixie's agents other than the coordinator (its workers and any
other pane in Pixie's Herdr/tmux session); ⌨ is your reachable `pix` sessions. A zero side is
left out (`⚙ 3`, `⌨ 2`). The chip hides when both are zero (only the coordinator running, or
the agent feature off).

**List** (opened from the chip), two sections:

```
Pixie's agents (3)
 ○ Coordinator            idle
 ● fix-upload             working   deskpixie
 ◐ docs-pass              blocked   deskpixie
 ○ api-tests              idle      backend
Your sessions (2)
 ● login bug              working   WebStorm
 ○ upload test            idle      iTerm
```

| Row | Name | Detail |
|---|---|---|
| Coordinator | "Coordinator" | — |
| Worker (a job owns the pane) | the worker's name | its repo |
| Other pane in Pixie's session (e.g. kept from before a restart, no job) | the pane's name | "herdr" / "tmux" |
| Your session | its description; else `<tool> in <repo>`; else the tool | where it runs |

Status dot and word, from the host's `PaneStatus`: ● working, ◐ blocked (waiting on a permission or an answer), ○ idle or done, · unknown. Sections are in
that order; rows keep the host's order (coordinator first, then by start). A session pix just
lost contact with (not `Reachable`, the few seconds before it is dropped or reconnects) shows
greyed and isn't counted. More than 8 rows in a section: the first 8 and "+N more" (not
clickable).

## 4. Behaviour

| Event | Behaviour |
|---|---|
| Worker starts / exits, `pix` session connects / exits | Chip and open list update within ~1 s. No pop, no chime. |
| Status changes (working ↔ idle) | Row dot updates; counts don't change. |
| Pointer enters chip, or click | List opens under the chip (above, if there's no room below). |
| Pointer leaves chip and list | List closes. |
| Click a Pixie agent row | Its tab comes forward and the terminal opens (as `agent.focusJob`). |
| Click one of your sessions | Its terminal app is activated by bundle id. No bundle id known (older `pix`, or unknown terminal): nothing happens and the row has no pointer cursor. |
| Pixie dragged | Chip and list follow, centred under Pixie, clamped on screen. |
| Pixie faded (proximity) | Chip stays opaque, like the updates badge. |
| Pixie hidden | Chip and list hide. |
| Updates bubble showing | It stays at Pixie's side; the chip is under Pixie and no wider than Pixie, so they never overlap. The open list may cover the bubble while you hover; it is ordered above it and closes when the pointer leaves. |
| App launch / agent feature turned on | Chip appears once the first list arrives with a non-zero count. |
| Last worker and session end | Chip hides (the coordinator alone isn't counted); an open list closes. |
| Agent feature off | Chip hidden; no events. |

## 5. Architecture

### 5.1 Core (Go)

- **`agent.Roster`** (new, `core/agent/roster.go`): a pure builder.
  `BuildRoster(list []AgentInfo, workers []WorkerRow, hostName string) RosterView` turns the
  combined host list into rows and counts, using the jobs' worker rows to tell workers from
  other panes. `RosterView{Agents []RosterRow; Yours []RosterRow; AgentCount, YourCount int}`;
  `RosterRow{Name, Label, Detail, Status string; Kind string /* coordinator|worker|pane|session */;
  Reachable bool; App string /* bundle id, sessions only */}`. JSON field names in snake_case.
- **Runtime wiring** (`core/agent/runtime.go`): the runtime already consumes the merged
  `CompositeHost.Subscribe` stream. Each pane event (start, status, exit) and each job change
  schedules a rebuild, debounced to 250 ms; a 30 s ticker rebuilds as a backstop. A rebuild
  calls `Host.List`, takes the controller's worker rows (under `ctlMu`), builds the view, and
  emits only if it differs from the last one. `AgentHandle.Roster() RosterView` returns the last
  view for the app's first read.
- **App** (`core/app/app.go`): a new hook `Roster func(any)` emits `agents` with the view;
  `agent.roster` call returns the current view (empty view when the agent feature is off).
  `agent.focusAgent {name}` focuses a Pixie agent's pane and attaches the terminal (the same
  path as `FocusJob`, by pane name instead of job).
- **`pix` reports its app's bundle id.** `wire.Hello` gains `App string \`json:"app,omitempty"\``
  from `__CFBundleIdentifier` (set by macOS for apps and inherited by their terminals: e.g.
  `com.jetbrains.WebStorm`, `com.googlecode.iterm2`, `com.apple.Terminal`). `UserSession` gains
  `App`. Older `pix` binaries omit it; the row just isn't clickable.

### 5.2 App (Swift)

- **`EngineEvent.agents(RosterView)`** decoded from `agents`; `RosterView`/`RosterRow` Codable
  structs in DeskPixieCore.
- **`AgentRosterModel`** (DeskPixieCore, pure): holds the view and an `open` flag; derives the
  chip text, the sections (with the 8-row cap), and visibility. Unit-tested.
- **`ChipPlacement`** (DeskPixieCore, next to `BubblePlacement`): the chip's rect centred under
  Pixie with a gap, clamped into the screen; flips above Pixie when there is no room below; the
  list's rect hangs from the chip the same way. Unit-tested.
- **`AgentRosterController` + `AgentRosterView`** (`mac/DeskPixie/Pixie/`): a child panel of
  the pixie panel, built like `UpdatesBubbleController` (pointer polled at 0.1 s; opaque through
  proximity fade; hidden with Pixie). Reads `agent.roster` at start, applies `agents` events
  (counting events so a slow first read never overwrites a newer event).
- **Clicks:** a Pixie agent row calls `agent.focusAgent`; a session row with an `app` activates
  `NSRunningApplication.runningApplications(withBundleIdentifier:).first`.
- **`PixieController.onFrameChange`** becomes a list of observers (today the updates bubble
  owns the only slot); both controllers subscribe.
- **Updates bubble:** unchanged. The chip's width is capped at Pixie's width (smaller text if
  needed), so it never reaches the bubble at Pixie's side; the list panel is ordered above the
  bubble's panel.

## 6. Error handling

- `Host.List` fails: keep the last view and log; the next event or tick retries.
- The secondary (`pix`) host fails: `CompositeHost.List` already returns the primary's agents;
  "yours" shows what it last had only if the list succeeds, otherwise 0.
- Activating an app that isn't running (it quit since): nothing happens.
- `agent.focusAgent` for a name that is gone: no-op, like `focusJob`.

## 7. Testing

- **Go:** `BuildRoster` table tests: coordinator only (listed, counts 0/0); coordinator + workers; an unowned pane;
  reachable and unreachable sessions; labels for described / undescribed / no-repo sessions;
  status mapping. Runtime: a fake host whose events trigger one debounced emit; an unchanged
  list emits nothing; the backstop tick picks up a change without an event. App: `agent.roster`
  and the `agents` event reach `Emit`. `pix`: the hello carries `App` from `__CFBundleIdentifier`
  (extend the e2e test's WebStorm environment: `App == "com.jetbrains.WebStorm"`).
- **Swift:** `AgentRosterModel` (chip text, `⌨ 0` omitted, hidden when empty, section order, row
  cap, greyed unreachable rows, clickable only with an app); `ChipPlacement` (centred, clamped,
  flips above at the bottom edge; width capped at Pixie's); `EngineEvent` decodes
  `agents`.
- **Live:** extend `TestLivePixSessionThroughTheHerdrCoordinator` to read `agent.roster` while
  `pix claude` runs: the coordinator listed but not counted (agent count 0), one of yours, with `App` set.

## 8. Out of scope

- Picking the exact tab inside your terminal app.
- Controls in the list (stop, message): ask Pixie, as today.
- Sessions you started without `pix`.
