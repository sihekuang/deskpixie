# Updates bubble: what happened since you last talked to Bit

Status: draft for review · 2026-10-14

## 1. Goal

When Bit isn't set to start conversations by itself, agent updates that arrive while you're not
talking to Bit pile up silently in the outbox until your next conversation. The updates bubble
shows them beside Bit: a short pop when something arrives, then a count badge you can open.
Clicking a line takes you to the herdr pane it came from.

Success: you can glance at Bit and know whether anything happened, read it without starting a
conversation, and jump to the job's pane in one click; nothing pops or speaks while you're away.

## 2. Decisions (from the design discussion)

- Separate floating window beside Bit (not drawn inside Bit's panel, not macOS notifications).
- Lifecycle: brief pop (~6 s), then a badge; hover/click expands the bubble.
- Viewing the bubble counts as interacting: it marks the shown updates seen.
- Seen = heard: Bit doesn't replay seen updates at the next conversation. Exception: a question
  still waiting for an answer is still raised.
- Only active while `agent.openConversationForUpdates` is off (Bit isn't speaking updates itself).
- Away from the Mac: nothing pops, nothing is announced, no conversation is started; the pop is
  held and shown once when you come back.
- Clicking a line focuses that job's herdr pane.
- The bubble adds to the macOS notifications; it doesn't replace them (no chime while away).
- The timings are settings, in a new Notifications section, and the existing update debounce
  becomes one of them: one notification debounce timer for every channel (§5.3).

## 3. What the bubble shows

The outbox's undelivered lines (`core/agent/outbox.go`): worker reports, coordinator replies,
questions, and the app's own info lines that arrived with no conversation open. Questions whose
permission token is no longer pending are left out (the same filter `Replay` uses).

- Newest first, up to 5 lines; each line shows its text (two lines max, truncated) and a relative
  time ("2m ago"). More than 5: a last row "+N more", which starts a conversation (Bit replays
  everything, as today).
- The badge shows the total count of unseen lines (9+ above nine).

## 4. Lifecycle

```
           new update (present)            ~6 s, pointer not over it
 Hidden ─────────────────▶ Popped ──────────────────────────▶ Badge
   ▲                         │  ▲                               │
   │                         │  └── another update: line added, timer restarts
   │       hover / click     ▼                                  │ hover / click
   │                     Expanded ◀─────────────────────────────┘
   │                         │  open ≥ 1 s → shown lines marked seen
   └─────────────────────────┘  pointer leaves → Hidden (nothing unseen left)
```

| Event | Behaviour |
|---|---|
| Update arrives, you're present | Pop with the latest lines; badge count updates. |
| Update arrives, you're away | No pop, no chime; badge only. One pop on return if anything arrived while away. |
| Update while popped / expanded | Line added on top; pop timer restarts; expanded stays open. |
| Conversation starts | Bubble and badge hide at once; Bit replays undelivered lines (existing behaviour). |
| Update during a conversation | No bubble; it is spoken as today. |
| A question is decided elsewhere | It drops from the list; badge count goes down. |
| App restart | Undelivered lines persist (outbox.jsonl); badge restored at launch, no pop. |
| Bit faded (proximity) or hidden | The bubble stays opaque through proximity fade (else the badge vanishes as you reach for it); it hides when Bit is hidden, and a pop while hidden becomes badge-only. |
| Bit dragged / repositioned | Bubble follows; it sits on the side of Bit with room, flipping near a screen edge. |
| Fullscreen app frontmost, or Focus / Do Not Disturb on | Badge only, no pop. |
| `openConversationForUpdates` on | Takes effect on restart (ApplyRestart): the core sends no bubble updates; Bit speaks them. |

**Away** means any of: screen locked, displays asleep, or no keyboard/mouse input for 5 minutes.
**Back** is the first of: unlock, display wake, or input after an idle stretch.

## 5. Architecture

### 5.1 Core (Go)

- **Lines carry their job.** `Line` gains `Job JobID` (zero when the line isn't about one job,
  e.g. app info lines). `OutboxLine` gains an optional `job` field (`omitempty`; older records
  without it restore fine). The places that build lines for a job (worker reports, coordinator
  replies with a `ref`, questions) set it.
- **Outbox.MarkSeen(ids, tokenPending) []string** marks those undelivered lines delivered, except
  questions whose token is still pending, and returns the jsonl records to append. `Replay`
  is unchanged.
- **Presence.** New input `InPresence{Away bool}`. While away, `tryOpen` doesn't open a
  conversation (it keeps `retry` set and tries again on back), so `openConversationForUpdates`
  also respects being away. Chime effects for updates are skipped while away.
- **Bubble feed.** When `openConversationForUpdates` is off, every change to the undelivered set
  (push, replay, mark seen, question decided, restore) emits `EfUpdates` with the current list:
  `[{id, text, kind, at, job}]`. The runtime turns it into an engine event `updates` with that
  JSON. Deduplicated: identical consecutive lists are not re-sent.
- **Engine calls** (shell → core):
  - `agent.updatesSeen {ids: [..]}` → `InUpdatesSeen` → `MarkSeen`, then a fresh `EfUpdates`.
  - `agent.focusJob {job}` → focuses the job's live worker pane if it has one, otherwise the
    coordinator (`focusQuietly`, which also brings herdr to the front). Unknown or closed job:
    the coordinator.
  - `presence.set {away: bool}` → `InPresence`.

### 5.2 Shell (Swift)

- **`UpdatesBubble`** (new, `mac/DeskPixie/Mascot/`): an `NSPanel` like `MascotPanel`
  (borderless, non-activating, floating, all Spaces) holding the badge and the bubble view
  (SwiftUI in an `NSHostingView`). It is a child window of the mascot panel so it moves with it;
  `MascotController` tells it Bit's frame, alpha and visibility.
- **State machine** (`hidden / popped / badge / expanded`) in a small pure type
  (`UpdatesBubbleModel`) so the transitions are unit-testable without AppKit.
- **`Presence`** (new): watches `com.apple.screenIsLocked` / `screenIsUnlocked` distributed
  notifications, `NSWorkspace.screensDidSleep/Wake`, and idle time
  (`CGEventSource.secondsSinceLastEventType`, polled every 30 s, 5 min threshold). Sends
  `presence.set` on change and gates pops locally.
- **Focus / fullscreen check:** before popping, skip the pop if the frontmost app's window is
  fullscreen or a Focus mode is on (best-effort; badge still updates).
- `EngineCoordinator.handle` gains `case .updates(let json)` → `UpdatesBubble.apply(lines)`;
  the core's `.state` events hide the bubble while a conversation is open.

### 5.3 Settings: a new Notifications section

The update debounce in `core/agent/debounce.go` (`UpdateQuietMs` 20 s, `UpdateMaxHoldMs` 30 s)
decides when a job's worker reports and progress are let out at all, so it already paces every
channel downstream: spoken lines, macOS notifications, the outbox and therefore the bubble's pop.
It becomes the setting; the constants become defaults.

| Path | Label | Default | Range | Applies |
|---|---|---|---|---|
| `notifications.debounceSecs` | Notification debounce (seconds) | 20 | 0–300 | restart |
| `notifications.bubble.enabled` | Updates bubble | on | | live |
| `notifications.bubble.popSecs` | Pop for (seconds) | 6 | 1–60 | live |
| `notifications.bubble.lines` | Updates shown | 5 | 1–10 | live |
| `notifications.awayAfterMins` | Away after (minutes idle) | 5 | 1–60 | live |

- `debounceSecs` is the quiet time; the max hold follows it at 1.5× (20 → 30 s, today's values).
  0 turns coalescing off (every report goes out as it comes; dedupe still applies). Questions are
  never debounced, as today.
- `awayAfterMins` is read by the shell's `Presence`; it also gates Bit starting conversations.
- The 1 s "open long enough to count as seen" stays fixed.
- `agent.notifyPerMinute` keeps its path (moving it would break saved settings) but is shown in
  the Notifications section next to the others.

## 6. Error handling

- `agent.updatesSeen` with unknown or already-delivered ids: ignored (idempotent).
- `agent.focusJob` when herdr isn't running or focus fails: logged; nothing else happens.
- Malformed `updates` JSON in the shell: logged and ignored; the previous list stays.
- Presence signals that never arrive (e.g. no lock notification): idle time alone still flips
  away/back.

## 7. Testing

- Go unit tests:
  - the debounce follows `notifications.debounceSecs` (quiet and 1.5× max hold; 0 = no
    coalescing); the new settings validate their ranges.
  - `MarkSeen` marks lines, keeps pending questions, returns records; restore with and without
    the `job` field.
  - `EfUpdates` emitted on push / replay / seen / decided, not emitted with
    `openConversationForUpdates` on, not re-sent when unchanged.
  - Presence: `tryOpen` holds while away and opens on back; no chime while away.
  - `focusJob` picks worker pane, falls back to the coordinator.
- Swift unit tests for `UpdatesBubbleModel` transitions (pop timer, hover, seen after 1 s,
  away → badge only → pop on return, conversation hides).
- Manual check in the running app: start a worker, let it report with no conversation open,
  watch pop → badge → expand → click → herdr pane focused; lock the screen, let an update
  arrive, unlock → one pop.

## 8. Out of scope

- Replying to or approving questions from the bubble.
- Per-line dismiss, history beyond the undelivered list, settings to tune timings.
- Changing what macOS notifications do, beyond no chime while away.

