# Session turn announcements: design (#114)

## 1. Goal

When a session you started yourself with `pix` (Claude Code or Codex, in WebStorm, iTerm,
Warp, …) finishes a turn, Pixie tells you, with what it said in one sentence, even when
Pixie didn't start that turn. You learn something is done without watching the terminal.
A setting turns this on or off.

Today a `pix` session's Stop only flips its roster status to idle (`core/pix/session.go`,
`case "Stop"`). The reply text never leaves `pix`, even for a prompt Pixie sent with
`message_session`.

## 2. Behavior

### When Pixie announces

A `pix` session finishes a turn (Claude: the Stop hook; Codex: its notify), and either:

- the turn ran at least `notifications.sessionTurnsMinSeconds`, from the session going
  *working* to its Stop (quick back-and-forth you are watching stays quiet), or
- Pixie typed that turn's prompt with `message_session` (always announced, however short:
  you weren't watching it).

A turn you interrupt (Esc) fires no Stop, so it is not announced. With
`notifications.sessionTurns` off, nothing changes from today.

### What it says

| Case | Line |
|---|---|
| Done | `deskpixie: fix login redirect is done: the redirect loop is fixed and all 412 tests pass.` |
| Ends in a question | `deskpixie: fix login redirect is asking: should I also update the docs?` |
| No usable text | `deskpixie: fix login redirect finished.` |

The name is the session's description. A session without one uses its folder name, as the
roster does.

### How it reaches you

The same route as worker reports (`deliverUpdate`):

- **Conversation open:** the voice model gets the capped reply and says it in one sentence.
- **Conversation closed:** a chime and a notification with the gist (§4), plus the updates
  bubble.
- It is held by the per-minute cap. A line that is asking bypasses the cap, as questions do
  today.
- The "away" rule (`notifications.awayAfterMins`) applies unchanged.
- Everything also goes to the outbox.
- Clicking the notification or the bubble line brings that session's terminal forward
  (the session's address is the line's target, so #98's reuse applies).

### Settings

Both settings sit in the Notifications tab and apply on restart (`ApplyRestart`), like
`agent.workers.speakResults`: the controller's settings are read once at startup
(`agent.Settings`, built in `core/app/agentapp/agentapp.go`), and no setting reaches a
running controller today.

| Key | Kind | Default | Label | Help |
|---|---|---|---|---|
| `notifications.sessionTurns` | bool | `false` | Announce finished sessions | Pixie tells you when a session you started with pix finishes a turn, even one Pixie didn't start. |
| `notifications.sessionTurnsMinSeconds` | int 0–3600 | `30` | Only turns longer than (seconds) | Shorter turns stay quiet; you were probably watching. Turns Pixie started are always announced. |

It defaults to off because it is a new source of interruptions, from sessions that today
never notify anyone.

## 3. Data flow

### 3.1 `pix` reports the turn (`core/pix/session.go`)

- On the status going *working* after idle, `pix` records `turnStart` (wall clock).
- It records whether Pixie typed this turn's prompt: `fromPixie(prompt)` on
  UserPromptSubmit (Claude), or on the first of `input-messages` in the notify (Codex).
- On Stop it sends one event, in addition to today's idle status:
  `wire.Msg{Kind: KindEvent, Event: "turn", Text: reply, DurationMs: d, Source: "pixie"|"you"}`.
  - `reply` is `last_assistant_message` from Claude's Stop payload, or
    `last-assistant-message` from Codex's notify payload. It is cut to 4 KB on a UTF-8
    boundary. A missing field gives an empty reply.
  - `d` is now minus `turnStart`, or 0 when no *working* status was seen (a fast Codex turn
    can show no busy marker; such a turn of yours counts as short).
- `pix` always sends `turn`. The app decides whether to announce, because the settings
  live in the app.

### 3.2 Wire (`core/pix/wire/wire.go`)

`Msg` gains `DurationMs int64 \`json:"durationMs,omitempty"\``. `Version` stays 1: both ends
ship in the same app, and the host's event switch ignores events it doesn't know. A new
`pix` with an old app is ignored; an old `pix` link sends nothing.

### 3.3 The host (`core/pixhost/host.go`)

The event switch gains `case "turn"`. It broadcasts
`agent.PaneEvent{Kind: agent.PaneEventTurn, Agent: s.addr, Turn: &agent.SessionTurn{…}}`,
where `SessionTurn` is `{Label, Reply string; Duration Millis; FromPixie bool}`.
`Label` is the session's current description, else its folder's base name.

### 3.4 The controller (`core/agent/controller.go`)

- The pane-event switch gains an explicit `case ev.Kind == PaneEventTurn`, placed before
  `default:`. It must be explicit: `default:` calls `workerGone`, so an unhandled kind would
  be taken as a closed pane.
- The case calls `onSessionTurn(ev.Agent, *ev.Turn, now, &fx)`:
  1. Setting off: return.
  2. `!FromPixie` and duration under the minimum: return.
  3. `text, asking := SessionGist(maskPendingTokens(reply, c.pendingTokens()))`.
  4. Build the line (§2 table) and send it through `deliverUpdate`.
- The line is `Line{Kind: LineSession, Text: <gist line>, Asking: asking, Worker: addr,
  Detail: <cleaned reply, capped at 2 KB>}`.
  - `LineSession` is a new kind, serialized `"session"`, with `Prefix()`
    `"Session update: "`. The voice model never hears "Coordinator…" for it.
  - `Spoken()` for a line with `Detail` is
    `Prefix + Text + " Its reply, to sum up in one sentence: " + Detail`.
    Notifications, the bubble and the outbox keep using `Text`.
  - The router treats `Asking` like `LineQuestion`: never held by the cap.
- `agent.Settings` gains `SessionTurns bool` and `SessionTurnsMinMs Millis`, filled from
  the two settings in `core/app/agentapp/agentapp.go` next to `SpeakWorkerReports`.

## 4. The gist (`core/agent/sessiongist.go`)

`SessionGist(reply string) (text string, asking bool)` is a pure function:

1. Remove fenced code blocks, table rows, headings' `#` marks, list markers and `*`/`_`
   emphasis. Inline code keeps its text without the backticks.
2. Split into paragraphs on blank lines and take the last non-empty one.
3. If it ends with `?`, `asking` is true and the text is its last sentence (the question).
4. Otherwise the text is its first sentence. If that sentence has fewer than 3 words (e.g.
   "Done."), use the first sentence of the paragraph before it, if there is one.
5. Collapse whitespace and cap at 120 characters on a word boundary, ending in `…`.
6. Nothing left: `""` (the caller then says "finished").

Confirm words are masked before the gist is made (§3.4 step 3), so a session's reply can
never speak a word that approves a pending permission. The same masked text is the
`Detail` the voice model hears.

## 5. Out of scope

- Sessions not run through `pix` (nothing reports their turns).
- Pixie's own coordinator and workers: their replies already route as today.
- Summaries made with a separate AI call, or by asking the session to write its own (both
  considered; text rules chosen for zero cost, privacy and no change to your sessions).
- Answering a session's question from Pixie (#105).

## 6. Testing

- **`core/pix`:**
  - A Claude Stop sends `turn` with `last_assistant_message`, the duration since *working*
    and `source: "you"`.
  - A Codex notify sends `turn` with `last-assistant-message`.
  - A turn whose prompt Pixie typed is `source: "pixie"`.
  - A reply over 4 KB is cut on a UTF-8 boundary.
  - A Stop with no *working* status before it has duration 0.
- **`core/pix/wire`:** `durationMs` round-trips.
- **`core/pixhost`:** `turn` becomes a `PaneEventTurn` carrying the description as the
  label, or the folder name when there is none. The session is not removed.
- **`core/agent` (controller):**
  - Setting off: no effects.
  - Your own turn under the minimum: no effects.
  - Your own turn over the minimum: chime, notification with the gist line, outbox.
  - A turn Pixie started, under the minimum: announced.
  - An asking line with the cap used up: still announced.
  - Conversation open: the inject carries `"Session update: "` and the reply.
  - No `workerGone` effects for the session.
  - A confirm word in the reply is masked in both the notification and the inject.
- **`SessionGist`:** table tests from real replies: plain done; question last; "Done." then
  details; code-only reply (empty); markdown list; over 120 characters; confirm word masked.
- **`core/settings`:** both keys exist, with their defaults, ranges, section and
  `ApplyRestart`. The Swift settings tests pass (`swift test` in
  `mac/Packages/DeskPixieCore`).
