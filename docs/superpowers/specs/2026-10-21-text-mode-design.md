# Text mode: type to the coordinator, read its answer

Status: approved direction, 2026-10-21 · issue #221

## 1. Intent

Sometimes you can't or don't want to talk out loud: a meeting, a shared office, late at night.
Text mode lets you type a quick message straight to the coordinator and read its short answer
under Pixie, with no sound. Keep it simple: no voice model is involved, so it works the same
whatever voice provider is set.

Success: with text mode on, you can ask the coordinator anything you could ask through Pixie
by voice, without a sound, read its short answer, and stop it if you change your mind.

## 2. Decisions (from the design discussion with the user)

- **A mode toggle, not automatic switching.** A two-way toggle, 🔊 voice / 💬 text, sits next to the
  session count under Pixie. It stays where you put it.
- **Voice mode is today's behaviour, unchanged.** It is the default.
- **The text box talks to the coordinator directly.** No voice model in between, no model
  choice, no personality concerns: the coordinator answers.
- **Quick message only.** One line to type in, under the row. No history, no expanding, no chat
  window, no "new chat".
- **Only the spoken answer is shown.** The coordinator already writes a short
  `<spoken ref="job_…" status="…">…</spoken>` block for Pixie to say (`core/agent/spoken.go`);
  the card shows that text for your message, never the coordinator's whole output, and doesn't
  repeat your message.
- **Stop** shows only while the coordinator is working on your message (thinking or answering)
  and stops it. It goes away when the answer is done.
- **The hotkey in text mode** opens the box if hidden and puts the cursor in it; it doesn't start
  a voice conversation.
- **Look:** the design language of the session count (`AgentRosterView`): the toggle is a
  capsule like the count's; the card is built like the session list (260 wide, regular material,
  radius 12, separator stroke, callout/caption type). Reference: the approved HTML prototype
  `.superpowers/brainstorm/69760-1792569148/content/quick-chat-v6.html` (git-ignored); §5 has
  its look.

## 3. Calls made while writing this (the user can override)

1. The finished answer stays until you close the card; your next message replaces it.
2. Closing the card (Esc, a click elsewhere, 💬 again) doesn't stop anything; only Stop does.
   Reopening shows the answer as it stands.
3. Switching to 🔊 doesn't stop a typed message's job; its answer still goes to the card (shown
   next time you switch back) and is not spoken.
4. The text card and the session list never show together: opening one closes the other.
5. The mode is a setting (`replyMode`: `voice` | `text`, Settings ▸ Pixie, live), so it survives
   a restart. Default `voice`.
6. In text mode DeskPixie never opens a voice conversation on its own: with
   `agent.openConversationForUpdates` on, those updates go to the bubble as with the flag off.
7. Switching to 💬 while a voice conversation is open ends that conversation.
8. Only the answer to your typed message shows in the card. Other updates (worker reports,
   other jobs, jobs started by voice) keep today's route: spoken in an open voice conversation,
   else a notification and the updates bubble.
9. Stop stops only the job your typed message started, never your own sessions.
10. Questions: a coordinator question about your typed message shows in the card, and the next
    message you type answers it (as `answer_job` does). A permission request (Allow/Deny) also
    shows in the card as text, and is answered as today, from the updates bubble or the
    notification, which it still goes to.
11. "Streaming": the coordinator hands over its spoken block when its turn ends, so the card
    types the text out word by word as it arrives, at reading speed. A progress line
    (`status="working"`) shows as it comes and is replaced by the next line.

## 4. How it works

### 4.1 Sending

`chat.send {text}` (new `dp_call`) delegates the text as a new job, exactly as Pixie's
`delegate` tool does (`AgentHandle.Delegate`), marked as typed. The job's context says the user
typed it in DeskPixie's text box. If delegation is off (no coordinator, workers or agent
disabled), the call returns the reason and the card shows it as the answer.

If a question from the previous typed job is still waiting, the text answers it instead
(`AgentHandle.AnswerAsked` for that job) rather than starting a new job.

### 4.2 Answers

Every line the controller would deliver for a typed job (reply, progress, question, failure,
"the coordinator stopped before finishing") goes to the card instead of the usual route: a new
effect that the app turns into a `chat.line {job, status, text}` UI event (`status`: `working` |
`question` | `done` | `failed`). The line is recorded as delivered, so it is never spoken or
replayed in a later voice conversation, and it isn't added to the updates bubble. A permission
request for a typed job keeps its usual route as well (call 10).

### 4.3 The card's phases (Swift)

| Phase | When | Shows | Stop |
|---|---|---|---|
| ready | opened, nothing sent | the input, focused | no |
| thinking | sent, no line yet; or after a `working` line | "Thinking…" (and the working line's text, if any) | yes |
| answering | a `done`/`failed`/`question` line is typing out | "Answering…" and the text | yes |
| done | typed out | the text; input ready | no |
| stopped | after Stop | the text so far, then "Stopped." | no |

Sending is disabled while thinking or answering.

### 4.4 Stop

`chat.stop` cancels the current typed job (`AgentHandle.Cancel`), which interrupts the
coordinator if that job is in its current turn and tells it to stop. Stop while only typing
out a finished answer just finishes it at once (nothing is running).

### 4.5 The hotkey and the mode

The hotkey decision is in Go: in text mode, a hotkey press emits `chat.focus` instead of
starting a voice conversation; Swift opens the card and focuses the input. Switching to text
ends an open voice conversation (call 7).

### 4.6 Where things go (Go or Swift)

- **Go:** the `replyMode` setting; `chat.send`, `chat.stop`; marking jobs typed; routing typed
  jobs' lines to the card (`chat.line`); the hotkey decision (`chat.focus`); no
  conversation-opening in text mode.
- **Swift:** the toggle capsule (sets `replyMode` through `settings.set`); the card (a
  key-capable child panel of Pixie's panel, placed like the session list); phases, typing out,
  Stop, Esc/click-away; card/list exclusivity. The card's state machine lives in `DeskPixieCore`
  with tests.

## 5. Look (from the approved prototype)

- Row under Pixie: `[⚙ 2 ⌨ 1]  [🔊|💬]`. The toggle is one capsule with two segments (SF
  Symbols `speaker.wave.2.fill`, `bubble.left.fill`), the selected one on a light pill; same
  material, stroke, height and weight as the count chip.
- Card: "Pixie" caption; the answer in callout type; a caption row "Thinking…" / "Answering…"
  with a small "■ Stop" text button on the right; a separator; the input ("Message Pixie"
  placeholder, `arrow.up.circle.fill` send, disabled until there is text).

## 6. Non-goals and anti-goals

- No history, threads or chat window; no coordinator raw output; no voice model in text mode.
- No change to voice mode.
- No sound from Pixie for typed messages.
- Never a card stuck on "Thinking…" with no way out: Stop works while busy, and a failure or a
  coordinator that stops shows as the answer.

## 7. Testing

- Go: the setting; `chat.send` (new job marked typed; answers a waiting question; delegation-off
  reason); typed jobs' lines go to `chat.line` and are marked delivered, not injected or bubbled;
  other jobs unchanged; `chat.stop` cancels; the hotkey emits `chat.focus` in text mode; no
  conversation opened for updates in text mode.
- Swift: the card model (phases from events, Stop visibility, send enabled, typing out, stop),
  the toggle, card/list exclusivity.
- End to end: an isolated Release build with its own herdr session; text mode on; send a
  message, see the coordinator's spoken answer appear in the card with no sound; Stop a slow one.
