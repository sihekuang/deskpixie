# Chat window (#302)

Status: design, for review. Supersedes the look and the "where things show" parts of
`2026-10-24-badge-and-reply-deck-design.md` (#286) and `2026-10-21-text-mode-design.md` (#221);
their rules for what a reply is, combining and "delivered = shown" carry over.

## 1. Why

Today what DeskPixie says lands in two decks floating around Pixie:

- **Replies** (the coordinator's answers, session updates, Pixie's captions) are cards under
  Pixie. Several make a swipe deck, and a card goes away after two minutes.
- **What needs you** (a permission, a session asking a question) is a badge beside Pixie. It
  opens a second swipe deck.

With several sessions and workers going at once, that's hard to follow. You swipe through cards
one at a time and can't scroll back. Your own words aren't shown at all, so a reply has no
question above it. Nothing is kept past a few minutes, and the same session's update and its
question sit in different decks.

The fix is an ordinary chat window: one thread, laid out like ChatGPT or Claude, which people
already know how to use. DeskPixie talks to several agents at once, so every message in that one
thread says which agent it's from. Requests from any of them are gathered at the top, the way
multi-agent tools gather what needs attention.

## 2. What ordinary chat looks like (prior art)

What the products agree on, and so what this design copies:

| Pattern | Seen in |
|---|---|
| A sidebar listing conversations, newest work first, with a New button; it can be hidden for a compact window | ChatGPT (and its companion window), Claude, Codex app, Claude Code desktop |
| The conversation top to bottom, newest at the bottom; it opens scrolled to the bottom, with a "jump to latest" button when you've scrolled up | all of them |
| Your messages as bubbles on the right; the assistant's as plain text across the width, not bubbles | ChatGPT, Claude |
| A message box pinned at the bottom: grows to a few lines, Return sends, Shift-Return adds a line, + attaches, and Send turns into Stop while it works | all of them |
| Permission requests as cards inside the conversation, with Allow / Deny right there | Claude Code desktop, Claude |
| Several sessions at once: each is its own conversation in a sidebar, with a status. This design does *not* copy that: the maintainer asked for a single thread, so here a session is a label on its messages instead | Codex app, Claude Code desktop, Cursor |
| A "Needs attention" group gathering every session blocked on an approval or a question, plus finished work you haven't looked at | Cursor's Agents window |
| A request from a session you aren't looking at says which session it's from, and you can jump to it | Codex |
| A notification when a session you aren't looking at finishes | Claude Code desktop |

Sources: OpenAI's macOS app release notes (help.openai.com/en/articles/9703738); Claude Code
desktop docs (code.claude.com/docs/en/desktop); Codex docs on subagents and approvals
(developers.openai.com/codex/subagents) and app features (learn.chatgpt.com/docs/features);
Cursor 3.0 changelog and forum (cursor.com/changelog/3-0, forum.cursor.com "Agents Approval
Window").

## 3. Goals, non-goals, anti-goals

**Goals**
- One window where you read everything DeskPixie and your agents said, and answer it.
- Your own messages, typed or spoken, appear with what they got back.
- One thread for everything. Every message from an agent session (your `pix` sessions and
  Pixie's workers) says which session it's from and its status.
- What needs you is never lost, and it's always one click away from wherever you are.
- It looks and behaves like the chat apps people already use, so it needs no explaining.

**Non-goals (not now)**
- A conversation per session, a sidebar, or several chats with Pixie: there is one thread.
- Search, editing or re-sending a message, regenerating an answer, branching.
- A session's full terminal transcript. DeskPixie shows what it knows about a session (its
  turns' summaries, questions, permissions); the terminal stays the place for everything else,
  one click away.
- Sync across Macs; a Windows or Linux window (the Go side is shared, as always).

**Anti-goals: these would make it a failure even if it works**
- The window opening or taking focus by itself. Only you open it.
- A permission or question that gets missed because the window was closed.
- The same reply shown twice: once in the window and again under Pixie.
- Losing what Pixie under the desk is for: you still hear Pixie, see what it's saying while it
  speaks, and see at a glance that something arrived.

## 4. The window

The mockup is a clickable HTML page in the artifact "DeskPixie chat window". It has two boards:
the window, and the desk around Pixie.

```
┌──────────────────────────────────────────────────────────────┐
│ ● ● ●                     Pixie                ● 2 working   │
├──────────────────────────────────────────────────────────────┤
│ 2 need you  (● api-server wants to run git push) (● docs asks)│
├──────────────────────────────────────────────────────────────┤
│                      Today 2:10 PM                           │
│                          [ fix the failing login test in api ]│
│ Pixie                                                        │
│ On it. I asked a worker to look at the login test in api.    │
│ ● login-fix · Pixie's agent · working                        │
│ Started on the login test. Reading auth/token.go.            │
│ ● deskpixie · your session · finished a turn                 │
│ The Settings search now finds the About page.  Open in term ›│
│ ┌ ● api-server · your session · needs you ─────────────────┐ │
│ │ 🔒 Run git push origin fix-login?   [Allow] [Deny]  Open ›│ │
│ └───────────────────────────────────────────────────────────┘ │
│                                   [ what's docs asking? ] 🎙  │
│ ┌ ● docs · Pixie's agent · asks ───────────────────────────┐ │
│ │ Which changelog format for 0.20?  [Keep a…] [Plain] [Reply…]│
│ └───────────────────────────────────────────────────────────┘ │
│ Coordinator                                                  │
│ The login test failed because… Fixed in auth/token.go.       │
│ (● login-fix ›)                                              │
│ · Thinking…                                                  │
├──────────────────────────────────────────────────────────────┤
│ (● Replying to docs ×)                                       │
│ [+]  Message Pixie…                                     [■]  │
└──────────────────────────────────────────────────────────────┘
```

### 4.1 One thread

It runs top to bottom, newest at the bottom, in a centred column about 680 pt wide. It opens
scrolled to the newest message, or to the oldest unread one. When you're scrolled up and
something new arrives, a **↓ Latest** button shows instead of the view jumping. Messages more
than 5 minutes apart get a small time divider.

**Who said it.** Every message says who it's from, so one thread can hold several agents:
- **Your messages**, typed or spoken, are bubbles on the right. A spoken one has a small mic
  mark. Attachments show as chips under the bubble.
- **Pixie** is labelled "Pixie": what it said aloud (captions) and its quick answers.
- **The coordinator** is labelled "Coordinator". Its replies get the rich text that exists today
  (#277): links, paths and commits you can click. Under a reply sit chips for the sessions it
  mentions; clicking one opens that session's terminal. Lines that arrive within 5 s still
  combine into one message, as now (#286).
- **A session** has its own header line: a coloured dot, its name, "your session" or "Pixie's
  agent", and its status (working, finished a turn, needs you). Its updates are DeskPixie's view
  of its turns: the brief from when a turn finished (#114). Each one has **Open in terminal ›**
  (`agent.focusSession`), because the terminal holds everything else. A session keeps the same
  colour all day, so its messages are easy to follow by eye.

**What needs you, in the thread.**
- **Permission requests** are cards with an orange edge, under their session's header, with
  **Allow / Deny** and their options. They're the same choices as today's badge cards
  (`agent.decide`, `agent.choose`).
- **Questions** are cards with their options and **Reply…**.
- **Answered cards** shrink to one line saying what happened: "api-server · You allowed git push
  origin fix-login · 2:14 PM". So the thread keeps a record of what you decided.

**While it works**, a "Thinking…" line shows at the bottom, and Send turns into **Stop** (#221's
Stop).

### 4.2 Needs you, pinned at the top

When anything is waiting, an orange strip sits under the title bar: "2 need you", then a pill
per request ("api-server wants to run git push", "docs asks a question"). Clicking a pill scrolls
to its card. Answering a card removes its pill, and the strip goes when nothing's waiting. This
does the job of Cursor's "Needs attention" group without a sidebar. Requests are never only far
up the thread.

### 4.3 The message box

It is pinned to the bottom, like the text box under Pixie today (#221, #240), moved here:
- 1–5 lines; Return sends, Shift-Return adds a line;
- **+** and dropping files attach them;
- Send turns into **Stop** while a typed message is at work.

A message goes to Pixie (the coordinator, `chat.send`) unless you're replying:
- **Reply…** on a question, or on any session message, puts a "Replying to docs ×" chip above
  the box, and the next message goes to that session (`agent.typedAnswer` for a question, the
  session message otherwise).
- × or Esc goes back to messaging Pixie.

This is how a single thread talks to several agents without a picker.

### 4.4 Opening it, and what stays around Pixie

The window is a normal macOS window: titled, resizable, with its size and place remembered and a
Dock icon while open, like Settings. It never opens by itself. You open it by:
- clicking Pixie's badge or the preview (below), or the 💬 toggle;
- the voice hotkey in text mode (today that focuses the box under Pixie);
- **Chat…** in the menu bar menu (⌘⇧C while DeskPixie is active);
- a notification's click, which opens the thread at that message.

Around Pixie, the two decks go. What stays:
- **Live captions while Pixie or you speak** (the "earlier" bubble, #254, and the caption).
  You're mid-conversation, and looking at a window would be wrong. Once a turn ends, its words
  are in the chat.
- **One badge** on Pixie's corner, merging the updates badge and the reply cards:
  - **orange with a count** when something needs you;
  - **a blue dot** when there's something unread and nothing waiting;
  - none when you're all caught up.
- **A one-line preview** that pops beside Pixie for a few seconds when something arrives
  ("api-server · needs you · Wants to run git push origin fix-login"). This is today's pop
  (`notifications.bubble.popSecs`), on the same settings. Clicking it opens the chat at that
  message.
- The roster chip under Pixie (#91). Clicking a row scrolls the chat to that session's latest
  message; ⌥-click still goes to the terminal.

## 5. Delivered, read and spoken

The rules from #286 still hold, with "shown" now meaning in the window:

- A line is **delivered** when it was on screen in the open chat window, or when Pixie said it
  aloud. Delivered lines aren't replayed or announced again (today's outbox flag,
  `agent.repliesShown`).
- **Unread** is everything that arrived since you last had the window open, scrolled to the
  bottom. It drives Pixie's badge dot and where the window opens.
- **In voice mode**, Pixie still says replies aloud as now (holding a card while it opens a
  conversation, #286). What it says goes into the chat as Pixie's words; it isn't added again as
  an unread card.
- **In text mode**, nothing is spoken: replies arrive in the chat, the badge dot and preview show
  them, and opening the window reads them.
- **A permission or question** stays in the Needs you strip until it's answered in the window,
  in the terminal, or by voice. Whichever comes first wins, and the others update to the one-line
  record.

## 6. History

Today nothing is kept past `replyHistoryMins` (5 minutes, in memory), and your own words aren't
kept at all. The chat keeps a real transcript:

- **Go owns it** (decide in Go, show in Swift): one append-only file under the app's data
  folder (`chat/thread.jsonl`), written as entries happen. One entry is one
  thing on screen: your message, Pixie's words, a coordinator reply (with its rows and
  sessions), a session update, a request and its outcome. Combining and "answered" updates
  rewrite the entry with the same id.
- **How long:** kept for `chatKeepDays` days (new setting, default 7). Older entries are
  dropped at launch. Clear History in the window's menu empties it.
- **Gone sessions:** a session's messages stay in the thread after it ends. Its header reads
  "ended", and Open in terminal is gone.
- **What the shell asks for:** the thread's entries a page at a time (newest first, for
  scrolling back), what needs you, and events for each new or changed entry. §8 has the calls.

Privacy: this is everything said to and by Pixie, on disk. It stays in the app's data folder.
It isn't added to Export Logs… unless a separate tick asks for it, and the Settings help for
`chatKeepDays` says what's kept.

## 7. Settings

| Setting | Change |
|---|---|
| `chatKeepDays` (new) | How long the chat keeps history; 1–90, default 7. Live. |
| `replyMode` | Unchanged: voice or text decides whether Pixie speaks replies. |
| `alwaysShowCaption`, `captionDismissSecs` | Only governed the card under Pixie: removed. settings.json keeps unknown keys harmlessly. |
| `captionFadeSecs` | Still the live "earlier" caption's fade. |
| `replyHistoryMins` | Replaced by `chatKeepDays` for the window. Go keeps using `ReplyHistoryMs` internally only to decide which undelivered lines Pixie may still announce after launch. |
| `notifications.bubble.*` | Now governs the badge's one-line preview (pop, lines). |

## 8. How it splits (Go decides, Swift does)

**Go (`core/agent`, `core/app`):**
- A `chat` store holds the thread's entries and writes the file. It's fed from what
  already flows through the controller: typed jobs and their `ChatLine`s, voice
  heard/transcript lines, outbox lines (`cardFor`), bubble items (permissions, questions), and
  roster changes for the session labels (name, kind, status).
- New dp_calls:
  - `chat.entries {before?, limit}`: entries carry who (you, Pixie, coordinator or a session with
    its name, kind, status and colour) and, for a request, its choices and outcome;
  - `chat.needs`: the pinned strip's requests;
  - `chat.read {upTo}`: marks entries read and their lines delivered;
  - `chat.clear`.
- Existing calls are reused: `chat.send`, `chat.stop` and session messaging, plus the request
  calls `agent.decide`, `agent.choose`, `agent.typedAnswer` and `agent.focusSession`.
- New events: `chat.entry {entry}` for a new or changed entry, and `chat.needs` when the strip
  changes. `reply.card` and `updates` give way to these. The badge state (count,
  unread, preview) comes from Go too, as a `badge` event.

**Swift (`mac/`):**
- `ChatWindowController`: a normal `NSWindow` hosting SwiftUI, like Onboarding. It never orders
  itself front without a user action.
- A testable package model, `ChatThreadModel`: entries, paging, unread, the needs strip, the
  reply target and scroll-to-latest.
- Views: the needs strip, a `ScrollView` thread reusing `RichMessage`, `SessionButtons`,
  `AttachmentChips` and today's permission card contents, and the message box from
  `QuickChatView`.
- Around Pixie: `QuickChatCard` (the reply deck) and the badge deck go. The badge, the preview
  pop and the live captions stay.

## 9. Building it in steps

Each step ships on its own:

1. **Go transcript and calls** (§6, §8), with tests: entries from every source, combining,
   answered records, unread, paging, keepDays. Nothing visible changes yet.
2. **The window**: the thread, the needs strip and the message box with Reply, opened from the
   menu. The decks still exist, so the two can be
   compared.
3. **The switch**: the badge and preview replace both decks, the roster rows and notifications
   open the chat, and the settings change (§7). The deck code is deleted.

## 10. Calls made

From the maintainer:
- **A single thread**, not a conversation per session and no sidebar.

My calls, made without asking; check these:
1. **Sessions are labels in the thread**: a coloured dot, the name, whose session it is and its
   status, with Open in terminal. Each session keeps one colour all day.
2. **What needs you is pinned in a strip at the top** as well as sitting in the thread, so a
   request is never lost far up the scroll.
3. **Reply… points the message box at a session**; otherwise messages go to Pixie.
4. **The decks around Pixie go**, replaced by one badge, a one-line preview and the live
   captions. Keeping both would show every reply twice.
5. **Sessions show what DeskPixie knows about them** (turn summaries, questions, permissions,
   what you sent), not the terminal's full transcript.
6. **History is kept for 7 days** on disk, owned by Go, and not in log exports by default.
7. **The window never opens itself**; the preview and the badge say something arrived.

## 11. Left to the builder

- Exact sizes, spacing and colours; follow the Settings window's look and macOS defaults.
- The storage format inside `chat/*.jsonl`, and how entries map from the existing line kinds.
- Whether "Thinking…" shows the coordinator's progress lines (#221 had them in the card).
- Keyboard shortcuts beyond those named here.
- How a session's name is chosen when two have the same label, and the palette of session
  colours (they must differ in lightness too, not only hue).
- Whether to add a "show only one session" filter later. It's left out now.
