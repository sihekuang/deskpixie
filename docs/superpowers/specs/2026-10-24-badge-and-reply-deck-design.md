# The badge for what needs you, a reply deck for everything else (#286)

Mockup (approved): https://claude.ai/artifact/SPnacFbSrxWTtYeFrwZYzd. Boards 1–4 show the
cases, board 5 is a clickable prototype of both decks.

## 1. Intent

Pixie has two places beside it: the **badge** (the blue circle with a count, which opens the
updates bubble) and the **reply card** under it. Today their jobs overlap. "…is done" lines,
coordinator replies and permission requests all go to the bubble, while the card holds only
what Pixie said aloud and the answer to a typed message. Worse, with
`agent.openConversationForUpdates` on, the bubble leaves out every update on the assumption
that Pixie will say it. In text mode Pixie never opens a conversation, so updates showed
nowhere next to Pixie, only as push notifications (#283, which this fixes).

The user's rules, in their words:

- "Alerts (circle with counter) should be used for something that requires user's attention such
  as permission requesting. Regular contents should go into response reply."
- "Text mode is just like voice mode experience minus voice capability."
- "If it's asking that means it needs my attention to take action so it belongs to badge."
- Updates that arrive at the same time are combined.
- Both places are decks: scroll left and right between cards.

## 2. Goals

1. **Two lanes, one job each.**
   - **Badge lane:** anything that needs the user to act. That covers a permission request
     (Allow/Deny), a session's menu (options), a coordinator question (a `status="question"`
     block or a question line), and a session that is asking (`Asking: true`, even without
     buttons).
   - **Reply lane:** everything else. That covers session and worker "done" or "failed" lines,
     coordinator replies (`LineReply`, `LineUnsolicited`), the app's own lines (`LineInfo`), and
     what Pixie says aloud.
2. **The same in voice and text mode.** Text mode is voice mode without audio:

   | Situation | Badge lane | Reply lane |
   |---|---|---|
   | Voice, conversation open | Pixie says it (as today) and it waits in the badge until answered | Pixie says it; the same words are a card in the reply deck |
   | Voice, not talking, open-for-updates **off** | badge + notification (as today) | a card in the reply deck, silent; notification as today |
   | Voice, not talking, open-for-updates **on** | badge, and Pixie opens a conversation and says it (as today) | Pixie opens a conversation and says it; a card in the reply deck |
   | Text mode (either setting) | badge + notification | a card in the reply deck; notification as today |

   In text mode, open-for-updates does nothing: there is nothing to open, so the card simply
   shows.
3. **The reply deck.**
   - Every reply-lane message is a card: typed answers, spoken captions and updates, in one
     history.
   - The newest card is on top, with the edges of up to two older cards peeking behind it.
   - A horizontal trackpad swipe (two-finger scroll, `deltaX`) moves one card per gesture, and
     the ‹ › arrows do the same.
   - The header shows the position as `n/m`.
   - Retention stays as it is today: `replyHistoryMins` drops older cards, and the card shows,
     fades and is put away by the existing caption rules (`alwaysShowCaption`,
     `captionDismissSecs`, the ✕).
   - Links, files, code and commits stay clickable (`RichMessage`, #277).
4. **Combining.**
   - A reply-lane update that arrives within **5 seconds** of the previous one, while that card
     is the newest and not yet put away, joins it as another row. Each row is led by its title
     (the session or job label, bold) followed by the brief or text.
   - The combined card is one step in the deck's history.
   - In voice mode a combined card is said as one turn ("Three updates: …"), as a burst Router
     merges today.
   - The per-job debounce (`notifications.debounceSecs`) still runs first. Combining only groups
     across sources.
5. **The badge deck.**
   - The badge count is the number of badge-lane items.
   - Hovering the badge opens them as a deck, "1 of n", one card each with its own buttons:
     Allow/Deny, menu options and "Chat about this" as today; for a session that asks without
     buttons, a way to bring up its terminal (as a click on that line does today).
   - Swiping or the arrows move between cards. Answering one removes it, and the next slides up.
   - With nothing waiting there is no badge.
6. **Told once.**
   - A reply-lane line shown as a card counts as delivered, so a later voice conversation
     doesn't replay it from the outbox.
   - A badge-lane line stays until it is answered or no longer pending, as today.

### Calls made while writing this (the user can override)

1. **Voice, conversation open:** the card *is* the caption of what Pixie said, the voice model's
   transcript, as today. Go sends no separate card, so nothing shows twice. Rows, titles and
   combining are for cards that aren't spoken. Router already merges a burst it says into one
   turn.
2. **Combining never delays speech.** The 5-second window applies to cards that aren't spoken.
   In voice, a line is said when it arrives, as today; only a burst Router already holds is said
   as one turn.
3. **A badge item leaves when the action is done, not when it's seen.**
   - A permission or menu leaves when it's answered or no longer pending.
   - A session's question leaves when that session takes its next turn (it moved on, whether you
     answered in its terminal or elsewhere) or ends.
   - A coordinator question leaves when its job leaves Question.
   - Hovering the deck or bringing up the terminal doesn't clear anything. Go keeps spoken but
     unanswered questions as badge items for this.
4. **A new card that isn't spoken pops up on its own** for `captionDismissSecs`, whatever
   `alwaysShowCaption` says, like the late typed answer (#259). With the setting off, cards
   otherwise show while Pixie talks or on hover, as today.
5. **Delivered = shown.** The shell reports a card as shown, as `updatesSeen` does today, and only
   then does the line count as delivered. At launch, reply-lane lines still undelivered and
   younger than `replyHistoryMins` come back as cards (combined if they fall within 5 s of each
   other). Older ones stay in the outbox for the next conversation, as today.
6. **Every reply-lane line gets a card**, including one over the notification cap and
   `LineUnsolicited`, which get no notification. In voice-open mode, a line Router holds back gets
   its card when it is said (calls 1 and 2).

### Decided with the user after review

7. **A question about a message typed in the box shows in both places.** It shows in the reply
   card, where the next message typed answers it as today (`boxAsker`, `takeBoxAnswer`). It also
   counts on the badge until it's answered, just as a question Pixie says aloud waits in the
   badge. This is the one deliberate exception to "one line, one lane". The anti-goal below is
   about the lanes' content, not about this answer path.

## 3. Non-goals and anti-goals

- No change to push notifications, the per-minute caps (`notifyPerMinute`) or the per-job
  debounce.
- No change to what is spoken, or when, beyond saying a combined card as one turn.
- The settings revamp, search and "take me to this setting" are a separate piece of work.
- **Anti-goal:** news must never sit in the badge. If something doesn't need an action, it never
  shows in the badge count.
- **Anti-goal:** an update must never show nowhere. Whatever the mode or settings, a delivered
  line is visible in one of the two places.
- **Anti-goal:** no duplicates. One line goes to one lane (except call 7). The text-mode spec's rule that "a
  permission request also shows in the card as text" (2026-10-21-text-mode-design.md §3.10) is
  replaced: it shows in the badge only.

## 4. How the work splits (Go decides, Swift does)

- **Go (`core/agent`):**
  - Decides each line's lane. A single function of the line (and its pending state) answers
    alert or reply.
  - Does the combining: it owns the 5-second window and produces one reply card with rows.
  - Decides what counts as delivered.
  - `bubbleItems` keeps only badge-lane lines, regardless of `OpenConversation`.
  - The reply lane reaches Swift as a reply-card event. Extending `chat.line` / `EfChat` or
    adding a sibling event is the builder's call. The event carries rows (title, text, the
    sessions it's about, for rich-text resolution) and an id, so a later row can join an
    existing card.
- **Swift package (`DeskPixieCore`):** the deck models, kept free of AppKit where possible:
  - Reply deck: history of cards with rows, position, swipe stepping and the gesture threshold.
  - Badge deck: current index; answering removes the card and clamps the index.
  - These extend `QuickChatModel` (its history already holds the reply cards) and
    `UpdatesBubbleModel`.
- **App target:** the stacked look and the swipe gesture.
  - `scrollWheel` with `hasPreciseScrollingDeltas`, one card per gesture phase. A mouse wheel's
    vertical scroll does nothing.
  - The card and bubble views reuse `RichMessage`.

## 5. Look (from the mockup)

- Cards keep today's material, radius and type. Behind the top card, up to two cards show as
  edges, each inset 8 pt per side and raised 7 pt, at lower opacity.
- Reply card header: "Pixie · <ago>", plus "· N updates" when combined, then ‹ n/m › and ✕.
- Badge card header: the kind and source, for example "Permission · coordinator" in amber with a
  lock glyph, or "<session> is asking". Then ‹ i of n ›, the text, and the buttons.

## 6. Left to the builder

- The event shape, and whether the reply lane reuses `ChatLine` or adds a type.
- Where the 5-second window lives in Go (Router, Updates or a small new type), as long as it's
  testable with the controller's clock.
- The swipe threshold and animation details, within "one card per gesture".
- How a combined card's spoken turn is worded, within "N updates: a, b and c".

## 7. Testing

- **Go:**
  - Lane classification for every line kind, including `Asking` with no buttons and a permission
    that is no longer pending.
  - `bubbleItems` holds only badge-lane lines, with `OpenConversation` on or off and in text
    mode (#283's case).
  - Reply-lane lines produce reply cards in voice-closed, voice-open and text modes.
  - Combining joins within 5 s, starts a new card after the gap or once the card is put away,
    and keeps one history entry.
  - Delivered marking: a later conversation doesn't replay a line shown as a card.
- **Swift:** deck models (stepping, bounds, clamping after answering, combining rows into one
  history entry, retention) and the gesture-to-step mapping.
- **End to end:** an isolated build with the user's own settings (text mode,
  open-for-updates on), the setup that reproduced #283.
  - A pix session's "done" shows as a reply card, and the badge stays empty.
  - Two sessions finishing close together combine into one card.
  - A session asking raises the badge, and its deck card brings up the terminal.
  - Then voice mode with the setting off: the "done" shows silently as a card.
