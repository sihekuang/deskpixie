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

The fix is an ordinary chat window, laid out like ChatGPT or Claude, which people already know
how to use. The one thing DeskPixie adds is that it talks to several agents at once, so the
window lists them the way multi-agent tools do (Codex, Cursor's Agents window, Claude Code
desktop).

## 2. What ordinary chat looks like (prior art)

What the products agree on, and so what this design copies:

| Pattern | Seen in |
|---|---|
| A sidebar listing conversations, newest work first, with a New button; it can be hidden for a compact window | ChatGPT (and its companion window), Claude, Codex app, Claude Code desktop |
| The conversation top to bottom, newest at the bottom; it opens scrolled to the bottom, with a "jump to latest" button when you've scrolled up | all of them |
| Your messages as bubbles on the right; the assistant's as plain text across the width, not bubbles | ChatGPT, Claude |
| A message box pinned at the bottom: grows to a few lines, Return sends, Shift-Return adds a line, + attaches, and Send turns into Stop while it works | all of them |
| Permission requests as cards inside the conversation, with Allow / Deny right there | Claude Code desktop, Claude |
| Several sessions at once: each is its own conversation in the sidebar, grouped by project or folder, with a status | Codex app, Claude Code desktop, Cursor |
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
- Each agent session (your `pix` sessions and Pixie's workers) is its own conversation, with its
  status at a glance.
- What needs you is never lost, and it's always one click away from wherever you are.
- It looks and behaves like the chat apps people already use, so it needs no explaining.

**Non-goals (not now)**
- Several conversations with Pixie (New chat starts the coordinator over, as Restart does today).
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

```
┌───────────────────────────────────────────────────────────────────────────┐
│ ● ● ●                         Pixie                               [⊟]     │
├──────────────────────┬────────────────────────────────────────────────────┤
│ [+ New chat]         │                         ── Today 2:10 PM ──        │
│                      │                       ┌──────────────────────────┐ │
│ ✦ Pixie            • │                       │ fix the failing login    │ │
│                      │                       │ test in api              │ │
│ NEEDS YOU          2 │                       └──────────────────────────┘ │
│ 🟠 api-server         │  Pixie                                              │
│    wants to run git… │  On it: I've asked a worker to look at the login   │
│ 🟠 docs               │  test in api.                                       │
│    asks a question   │                                                    │
│                      │  ┌─ api-server needs you ─────────────────────────┐ │
│ YOUR SESSIONS        │  │ 🔒 Run `git push origin fix-login`?            │ │
│ ● deskpixie   working│  │ [Allow]  [Deny]                [Open session ›]│ │
│ ○ webstorm-ui   idle │  └────────────────────────────────────────────────┘ │
│                      │                                                    │
│ PIXIE'S AGENTS       │  Coordinator                                       │
│ ● login-fix  working │  The login test failed because the token expiry    │
│ ✓ readme      done • │  check used local time. Fixed in `auth/token.go`;  │
│                      │  tests pass.                [api-server ›]          │
│                      │                                                    │
│                      │                                    [↓ Latest]      │
│                      ├────────────────────────────────────────────────────┤
│                      │ [+]  Message Pixie…                          [↑]   │
└──────────────────────┴────────────────────────────────────────────────────┘
```

### 4.1 Sidebar

From top to bottom:

1. **New chat.** It starts the coordinator over with an empty conversation. It asks first,
   like Restart Coordinator… does (#179).
2. **Pixie.** Your conversation with Pixie and the coordinator. It's always first and always
   there.
3. **Needs you (n).** Only shown when something's waiting. Each row is a session or Pixie with
   an open permission or question, and a one-line gist ("wants to run git push", "asks a
   question"). Clicking a row opens that conversation, scrolled to the request. This is
   Cursor's "Needs attention" group.
4. **Your sessions.** The `pix` sessions you started.
5. **Pixie's agents.** The coordinator's workers.

Each session row has three parts:
- a status dot, from the roster that already exists (#91): working (green, pulsing), waiting on
  you (orange), done (✓), idle (grey), gone (struck through, kept until you close it);
- the session's name, which is the roster label;
- a dot for unread when something arrived that you haven't seen.

Rows in each group sort by latest activity. A finished or gone session stays listed for the day
(it's kept in history, §6) and can be closed from the list with ×.

The sidebar can be hidden (⌘⇧S or the toolbar button). The window then shows just the current
conversation, as ChatGPT's companion window does. The minimum width is about 400 pt, so it can
sit beside other work.

### 4.2 A conversation

It runs top to bottom, newest at the bottom. It opens scrolled to the newest message, or to the
oldest unread one. When you're scrolled up and something new arrives, a **↓ Latest** button
shows instead of the view jumping. Messages more than 5 minutes apart get a small time divider.

**In Pixie's conversation:**
- **Your messages**, typed or spoken, are bubbles on the right. A spoken one has a small mic
  mark. Attachments show as chips under the bubble.
- **Pixie's words** are plain text across the width, labelled "Pixie". These are what it said
  aloud (captions) and its quick answers.
- **The coordinator's replies** are labelled "Coordinator". They get the rich text that exists
  today (#277): links, paths and commits you can click. Under a reply sit the buttons for the
  sessions it mentions (open session ›). Lines that arrive within 5 s still combine into one
  message, as now (#286).
- **While it works**, a "Thinking…" line shows at the bottom, and the message box's Send turns
  into **Stop** (#221's Stop).
- **Requests from a session** show as a short pointer, not a copy: "api-server needs you" with
  the gist and **Open ›**. This is Codex's labelled request from another thread. The full card
  lives in the session's own conversation, so nothing is shown twice.

**In a session's conversation:**
- **Updates** are the session's turns as DeskPixie knows them: the brief from when a turn
  finished (#114), and Pixie's announcements about it.
- **Permission requests** are cards with **Allow / Deny**, and their options when there are
  some. They're the same choices as today's badge cards (`agent.decide`, `agent.choose`).
- **Questions** are cards with their options, and "Type an answer" fills the message box
  (`agent.typedAnswer`).
- **Answered cards** shrink to one line saying what happened: "Allowed: git push origin
  fix-login · 2:14 PM". So the history shows what you decided.
- **Messages you send to the session** are bubbles on the right. This is the same message
  DeskPixie can already send a session; for a session, the box reads "Message api-server…".
- **Header:** the session's name, status and folder, and **Open in terminal**
  (`agent.focusSession`), since the terminal holds everything else.

### 4.3 The message box

It is pinned to the bottom, like the text box under Pixie today (#221, #240), moved here:
- 1–5 lines; Return sends, Shift-Return adds a line;
- **+** and dropping files attach them;
- Send turns into **Stop** while a typed message is at work;
- Esc while it's empty closes nothing (it's a normal window).

In Pixie's conversation it sends a typed message to the coordinator (`chat.send`). In a
session's conversation it sends to that session.

### 4.4 Opening it, and what stays around Pixie

The window is a normal macOS window: titled, resizable, with its size and place remembered and a
Dock icon while open, like Settings. It never opens by itself. You open it by:
- clicking Pixie's badge (below), or the 💬 toggle;
- the voice hotkey in text mode (today that focuses the box under Pixie);
- **Chat…** in the menu bar menu (⌘⇧C while DeskPixie is active);
- a notification's click, which opens the conversation the notification is about.

Around Pixie, the two decks go. What stays:
- **Live captions while Pixie or you speak** (the "earlier" bubble, #254, and the caption).
  You're mid-conversation, and looking at a window would be wrong. Once a turn ends, its words
  are in the chat.
- **One badge** beside Pixie, merging the updates badge and the reply cards:
  - **orange with a count** when something needs you;
  - **an accent dot** when there's something unread and nothing needs you;
  - hidden when you're all caught up.
- **A one-line preview** that pops next to the badge for a few seconds when something arrives
  ("Coordinator: The login test failed because…", "api-server wants to run git push"). This is
  today's pop (`notifications.bubble.popSecs`), on the same settings. Clicking it opens the chat
  at that message.
- The roster chip under Pixie (#91). Clicking a row opens that session's conversation instead
  of its terminal; ⌥-click still goes to the terminal.

## 5. Delivered, read and spoken

The rules from #286 still hold, with "shown" now meaning in the window:

- A line is **delivered** when it was on screen in the open chat window, or when Pixie said it
  aloud. Delivered lines aren't replayed or announced again (today's outbox flag,
  `agent.repliesShown`).
- **Unread** is per conversation: it's what arrived after you last had that conversation on
  screen. It drives the sidebar dots and Pixie's badge dot.
- **In voice mode**, Pixie still says replies aloud as now (holding a card while it opens a
  conversation, #286). What it says goes into the chat as Pixie's words; it isn't added again as
  an unread card.
- **In text mode**, nothing is spoken: replies arrive in the chat, the badge dot and preview show
  them, and opening the window reads them.
- **A permission or question** stays in Needs you until it's answered in the window, in the
  terminal, or by voice. Whichever comes first wins, and the others update to the one-line
  record.

## 6. History

Today nothing is kept past `replyHistoryMins` (5 minutes, in memory), and your own words aren't
kept at all. The chat keeps a real transcript:

- **Go owns it** (decide in Go, show in Swift): one append-only file per conversation under the
  app's data folder (`chat/<conversation>.jsonl`), written as entries happen. One entry is one
  thing on screen: your message, Pixie's words, a coordinator reply (with its rows and
  sessions), a session update, a request and its outcome. Combining and "answered" updates
  rewrite the entry with the same id.
- **How long:** kept for `chat.keepDays` days (new setting, default 7). Older entries are
  dropped at launch. Clear History in the window's menu empties it.
- **Gone sessions:** a session's conversation stays readable after the session ends, until its
  entries age out or you close it.
- **What the shell asks for:** the sidebar list, a conversation's entries a page at a time
  (newest first, for scrolling back), and events for each new or changed entry. §8 has the
  calls.

Privacy: this is everything said to and by Pixie, on disk. It stays in the app's data folder.
It isn't added to Export Logs… unless a separate tick asks for it, and the Settings help for
`chat.keepDays` says what's kept.

## 7. Settings

| Setting | Change |
|---|---|
| `chat.keepDays` (new) | How long the chat keeps history; 1–90, default 7. Live. |
| `replyMode` | Unchanged: voice or text decides whether Pixie speaks replies. |
| `alwaysShowCaption`, `captionDismissSecs` | Only governed the card under Pixie: removed. settings.json keeps unknown keys harmlessly. |
| `captionFadeSecs` | Still the live "earlier" caption's fade. |
| `replyHistoryMins` | Replaced by `chat.keepDays` for the window. Go keeps using `ReplyHistoryMs` internally only to decide which undelivered lines Pixie may still announce after launch. |
| `notifications.bubble.*` | Now governs the badge's one-line preview (pop, lines). |

## 8. How it splits (Go decides, Swift does)

**Go (`core/agent`, `core/app`):**
- A `chat` store holds the conversations and entries and writes the files. It's fed from what
  already flows through the controller: typed jobs and their `ChatLine`s, voice
  heard/transcript lines, outbox lines (`cardFor`), bubble items (permissions, questions), and
  roster changes for the sidebar.
- New dp_calls:
  - `chat.list` gives the sidebar: conversations with title, kind, status, unread count and gist;
  - `chat.entries {conversation, before?, limit}`;
  - `chat.read {conversation, upTo}` marks a conversation read and its lines delivered;
  - `chat.close {conversation}`;
  - `chat.clear`.
- Existing calls are reused: `chat.send`, `chat.stop` and session messaging, plus the request
  calls `agent.decide`, `agent.choose`, `agent.typedAnswer` and `agent.focusSession`.
- New events: `chat.entry {conversation, entry}` for a new or changed entry, and `chat.list`
  for sidebar changes. `reply.card` and `updates` give way to these. The badge state (count,
  unread, preview) comes from Go too, as a `badge` event.

**Swift (`mac/`):**
- `ChatWindowController`: a normal `NSWindow` hosting SwiftUI, like Onboarding. It never orders
  itself front without a user action.
- Package models, which are testable: `ChatListModel` (sidebar) and `ChatConversationModel`
  (entries, paging, unread, scroll-to-latest).
- Views: a sidebar `List`, a conversation `ScrollView` reusing `RichMessage`, `SessionButtons`,
  `AttachmentChips` and today's permission card contents, and the message box from
  `QuickChatView`.
- Around Pixie: `QuickChatCard` (the reply deck) and the badge deck go. The badge, the preview
  pop and the live captions stay.

## 9. Building it in steps

Each step ships on its own:

1. **Go transcript and calls** (§6, §8), with tests: entries from every source, combining,
   answered records, unread, paging, keepDays. Nothing visible changes yet.
2. **The window**: Pixie's conversation with the message box, and the sidebar with the session
   conversations and Needs you, opened from the menu. The decks still exist, so the two can be
   compared.
3. **The switch**: the badge and preview replace both decks, the roster rows and notifications
   open the chat, and the settings change (§7). The deck code is deleted.

## 10. My calls (made without asking; check these)

1. **One Pixie conversation, plus one per session.** Not one merged feed (that's what's hard to
   follow today) and not several Pixie chats (the coordinator is one long conversation). New
   chat starts the coordinator over.
2. **The decks under Pixie go**, replaced by one badge, a one-line preview and the live captions.
   Keeping both would show every reply twice.
3. **Sessions show DeskPixie's view** (turn summaries, questions, permissions, what you sent),
   not the terminal's full transcript.
4. **Session requests live in the session's conversation**, with a pointer in Pixie's. That
   avoids copies and keeps each request next to its session's work.
5. **History is kept for 7 days** on disk, owned by Go, and not in log exports by default.
6. **The window never opens itself**; a preview and the badge say something arrived.
7. **Sidebar order is Pixie, Needs you, Your sessions, Pixie's agents**, sorted by latest
   activity inside each group.

## 11. Left to the builder

- Exact sizes, spacing and colours; follow the Settings window's look and macOS defaults.
- The storage format inside `chat/*.jsonl`, and how entries map from the existing line kinds.
- Whether "Thinking…" shows the coordinator's progress lines (#221 had them in the card).
- Keyboard shortcuts beyond those named here.
- How a session's name is chosen when two have the same label.
