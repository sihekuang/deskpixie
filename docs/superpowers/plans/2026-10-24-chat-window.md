# Chat Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build one chat window, a single thread, for everything DeskPixie and the agents say:
your messages, Pixie, the coordinator, each session's updates and what needs you. It is an
addition: the reply cards and the badges around Pixie stay as they are (the maintainer's call,
2026-10-24).

**Architecture:**
- **Go keeps the thread.** It lives in a new package `core/chat`, a store backed by
  `chat/thread.jsonl`. `core/app` feeds the store from what already flows: `chat.send`, voice
  transcripts, and new agent hooks. It serves the thread through `chat.*` dp_calls and events.
- **Swift draws it.** A testable `ChatThreadModel` in the package, and a normal `NSWindow` in the
  app.
- **Two PRs**: Go thread (Tasks 1–5) and the window (Tasks 6–8b, with 8a's UI tests). Step 3
  (Tasks 9–10, replacing the decks) is withdrawn.

**Tech Stack:** Go 1.26 (`core/`), Swift 6 / SwiftUI / AppKit (`mac/`), dp_call JSON and events
over the C ABI.

**Spec:** `docs/superpowers/specs/2026-10-24-chat-window-design.md` (issue #302). The mockup is
the artifact "DeskPixie chat window".

## Global Constraints

- **Go decides, Swift does.** The thread, joining, needs, unread, outcomes and colours are all
  Go's. Swift only draws them and reports clicks (AGENTS.md).
- **Tests never touch the real home.** Use `t.TempDir()` for `chat/`, and the app harness's temp
  AppSupport.
- **Copy and choices, word for word from the spec:**
  - labels "Pixie" and "Coordinator";
  - a session's header is `<name> · your session|Pixie's agent · <status>`;
  - the strip reads "1 needs you" or "N need you";
  - records read "You allowed …", "You denied …", "You answered: …";
  - the window opens on "Thinking…";
  - the box reads "Message Pixie…", "Reply to <name>…" and "Replying to <name>";
  - the button "Open in terminal ›";
  - the window title "Pixie", the menu item "Chat…".
- **Joining:** lines from the same sender within 5 s (`agent.ReplyJoinMs`) join into one entry.
- **History setting:** `chatKeepDays`, 1–90, default 7, live. It's flat like `replyHistoryMins`;
  the spec's `chat.keepDays` is renamed to fit settings.json's flat keys.
- **The window never orders itself front** without a user action.
- **Session colours:** a palette of 6 that differ in lightness as well as hue. A session keeps
  its colour: `ColorFor(name)` is stable for a name.
- **One PR per step,** each cut from `origin/main`, closing nothing until the last
  (`Part of #302`; the last PR closes it).

## Review Focus

1. **A torn last line** in `thread.jsonl` (crash mid-write) is skipped, and everything before it
   loads (Task 2).
2. **A request answered in the terminal or by voice** while the window is closed leaves the
   needs list, recorded as "Answered elsewhere" and never as "You allowed" (Task 5).
3. **The updates list is re-sent many times.** The same request never shows twice: it's keyed by
   its update item's id (Tasks 4–5).
4. **Two sessions with the same label, or a label that changes:** entries are keyed by the
   session's address (`Session`), not its label (Task 4).
5. **A busy week of history** opens fast: pages of 50, newest first, and the window loads only
   the last page (Tasks 1, 6).

---

## Step 1 (PR 1): the thread in Go

### Task 1: `core/chat` store in memory

**Files:**
- Create: `core/chat/chat.go`, `core/chat/chat_test.go`

**Interfaces:**
- Produces:
  ```go
  type Who struct { Kind string `json:"kind"` /* you|pixie|coordinator|session */
      Name string `json:"name,omitempty"` /* session address */; Label string `json:"label,omitempty"`
      Yours bool `json:"yours,omitempty"`; Status string `json:"status,omitempty"`; Color int `json:"color"` }
  type Request struct { Token string `json:"token,omitempty"`; Item uint64 `json:"item"`
      Kind string `json:"kind"` /* permission|question */; Options []string `json:"options,omitempty"`
      Typed bool `json:"typed,omitempty"`; Outcome string `json:"outcome,omitempty"` }
  type Entry struct { ID string `json:"id"`; At int64 `json:"at"` /* ms */; Who Who `json:"who"`
      Text string `json:"text"`; Spoken bool `json:"spoken,omitempty"`; Sessions []string `json:"sessions,omitempty"`
      Lines []uint64 `json:"lines,omitempty"` /* outbox ids */; Attachments []string `json:"attachments,omitempty"`
      Request *Request `json:"request,omitempty"`; Read bool `json:"read,omitempty"` }
  const JoinMs = 5_000; const PageSize = 50
  func ColorFor(name string) int            // 0..5, stable (FNV of name)
  func NewMemory(now func() int64) *Store
  func (s *Store) Add(e Entry) Entry        // joins or appends; returns the stored entry
  func (s *Store) Update(id string, fn func(*Entry)) (Entry, bool)
  func (s *Store) Page(before string, limit int) []Entry   // newest first; before "" = from the end
  func (s *Store) Needs() []Entry           // requests with no Outcome, oldest first
  func (s *Store) MarkRead(upTo string) []uint64           // marks up to and including upTo; returns their outbox line ids not read before
  func (s *Store) Unread() int
  ```

- [ ] **Step 1: Write the failing tests** in `chat_test.go`:
  - `TestLinesFromOneSenderWithinFiveSecondsJoin`: two coordinator entries 4 s apart become one
    entry with text "a\nb" and the lines of both.
  - `TestLinesFromAnotherSenderOrLaterDontJoin`: a session entry between them, or 6 s later,
    starts a new entry.
  - `TestRequestsNeverJoin`.
  - `TestAddWithAnExistingIDReplaces`: same ID means replace in place, and the position is kept.
  - `TestPageIsNewestFirstAndPagesBack`: 120 entries give `Page("",50)` = ids 119..70 and
    `Page("e70",50)` = 69..20.
  - `TestNeedsAreOpenRequestsOldestFirst`: setting an Outcome removes it.
  - `TestMarkReadReturnsNewlyReadLines`: marking twice returns the lines once. `Unread()` counts
    entries not read, other than your own.
  - `TestColorForIsStable`: the same name gives the same colour, in 0..5.
- [ ] **Step 2:** Run `cd core && go test ./chat/`. Expected: FAIL (the package has no code yet).
- [ ] **Step 3: Implement** in `chat.go`.
  - Entries live in a slice plus an id→index map.
  - IDs: when `Add` gets no ID, it assigns `e<n>`.
  - Join test: same `Who.Kind` and `Who.Name`, no Request on either entry, and `e.At - last.At
    <= JoinMs`. Joined text is joined with "\n", and Lines and Sessions are unioned.
  - Your own entries count as read when added.
- [ ] **Step 4:** Run `go test ./chat/`. Expected: PASS.
- [ ] **Step 5: Commit:** "chat: the thread's store in memory (#302)".

### Task 2: persistence, history length, Clear

**Files:**
- Modify: `core/chat/chat.go`
- Create: `core/chat/file.go`, `core/chat/file_test.go`

**Interfaces:**
- Produces:
  ```go
  func Open(dir string, keepDays int, now func() int64) (*Store, error) // dir/thread.jsonl
  func (s *Store) SetKeepDays(days int)   // takes effect at the next Open; prunes now too
  func (s *Store) Clear() error
  ```
  Every `Add` and `Update` appends the entry's whole JSON as one line. On `Open`, the last record
  per id wins. Entries older than `keepDays` are dropped, and the file is rewritten compacted
  when anything was dropped or replaced.

- [ ] **Step 1: Write the failing tests:**
  - `TestTheThreadSurvivesReopening`;
  - `TestAnUpdateIsReplayedOverItsEntry`;
  - `TestATornLastLineIsSkipped` (write half a JSON line at the end);
  - `TestEntriesOlderThanKeepDaysAreDroppedAndCompacted` (8 days old with keep 7 is gone, and the
    file has one line per entry);
  - `TestClearEmptiesTheFile`;
  - `TestOpenMakesTheFolder`;
  - `TestOpenWithAFileWhereTheFolderGoesFails` (an error, not a panic).
- [ ] **Step 2:** Run `go test ./chat/`. Expected: FAIL.
- [ ] **Step 3: Implement** `file.go`.
  - `bufio.Scanner` with a 1 MB buffer; a line that doesn't parse is skipped.
  - File mode 0600, folder mode 0700.
  - `NewMemory` stays: a Store whose file is nil.
- [ ] **Step 4:** Run `go test ./chat/`. Expected: PASS.
- [ ] **Step 5: Commit:** "chat: kept in chat/thread.jsonl for chatKeepDays (#302)".

### Task 3: the `chatKeepDays` setting

**Files:**
- Modify: `core/settings/config.go` (field `ChatKeepDays uint32 json:"chatKeepDays"`, default 7,
  in the flat-key list at line ~290), `core/settings/schema.go` (in the "What Pixie said" group:
  `intKind(1, 90, false)`, unit days, `ApplyLive`), `core/settings/live.go` (new route
  `RouteChat`)
- Test: `core/settings/*_test.go` (the existing default, round-trip and route tests)

Label and help, word for word:
- label "Keep chat for";
- help "How many days the chat window keeps what was said. It stays on this Mac."

- [ ] **Step 1: Write the failing tests:**
  - `TestChatKeepDaysDefaultsToSevenAndIsLive`;
  - out-of-range values 0 and 91 are refused (follow the `captionFadeSecs` validation test);
  - the route is `RouteChat`.
- [ ] **Step 2:** Run `go test ./settings/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Keep each page at no more than `MaxPageRows`; `layout_test` checks
  this.
- [ ] **Step 4:** Run `go test ./settings/ ./app/`. Expected: PASS.
- [ ] **Step 5: Commit:** "settings: chatKeepDays (#302)".

### Task 4: agent lines and requests as thread entries

**Files:**
- Modify: `core/agent/controller.go` (in `applyDelivery`'s `DeliverOutbox`, for every pushed line
  that isn't `alerting`, add `Effect{Kind: EfLine, Line: <the OutboxLine>}`; new `EfLine` in the
  effect list; it runs through the same sink path as `EfReply`), `core/agent/replies.go` (new
  `LineSink interface{ Line(OutboxLine) }`)
- Modify: `core/app/agent.go` (new hooks), `core/app/agentapp/desktop.go` (sinks calling them)
- Create: `core/app/agentapp/thread.go`, `core/app/agentapp/thread_test.go`

**Interfaces:**
- Consumes: `chat.Entry`, `chat.Who`, `chat.Request`, `chat.ColorFor` (Task 1).
- Produces:
  ```go
  // core/app/agent.go AgentHooks:
  ThreadLine     func(chat.Entry)   // a session update or coordinator line, ready to Add
  ThreadRequests func([]chat.Entry) // every open request now (from the updates list), whole each time
  ThreadAnswer   func(chat.Entry)   // a typed job's final answer (ChatLine done|failed|question)
  // core/app/agentapp/thread.go:
  func EntryForLine(l agent.OutboxLine, r agent.RosterView) chat.Entry
  func RequestEntries(items []agent.UpdateItem, r agent.RosterView) []chat.Entry
  func EntryForChat(l agent.ChatLine) (chat.Entry, bool) // false while working
  ```
  Mapping:
  - A line with `Session` becomes `Who{Kind:"session", Name:Session, Label:<roster label or
    Title>, Yours:<row kind=="session">, Status:<roster status>, Color:ColorFor(Session)}`. Text
    is `Brief`, else `Text`; Lines is `[ID]`; Sessions is `[Session]`.
  - Any other line becomes `Who{Kind:"coordinator"}`.
  - A request has entry ID `req<item id>`, `Request{Item, Token, Kind:"permission" when Token set
    and no Options else "question", Options, Typed}`, and Text `Brief`, else `Text`.
  - A typed answer has entry ID `job<job id>` (an answer that grows replaces it) and
    `Who{Kind:"coordinator"}`.

- [ ] **Step 1: Write the failing tests** in `thread_test.go`:
  - `TestASessionLineIsLabelledFromTheRoster` (label, yours, status, colour);
  - `TestTwoSessionsWithOneLabelStayApart` (Name is the address);
  - `TestALineWithNoSessionIsTheCoordinators`;
  - `TestRequestsAreKeyedByTheirItem` (the same items twice give the same IDs);
  - `TestAPermissionAndAMenuMapToTheirKinds`;
  - `TestAWorkingChatLineIsNoEntry`, and `TestADoneChatLineIsTheCoordinatorsAnswer`.
  - In `core/agent`: `TestEveryReplyLineAlsoGoesToTheThread`: a non-alerting outbox line yields
    an `EfLine` with that line; an alerting line yields none.
- [ ] **Step 2:** Run `go test ./agent/ ./app/agentapp/`. Expected: FAIL.
- [ ] **Step 3: Implement.** The sinks call the hooks only when they're set, like `Reply` does.
  The roster used is the last one the roster sink saw.
- [ ] **Step 4:** Run `go test ./agent/ ./app/...`. Expected: PASS.
- [ ] **Step 5: Commit:** "agent: replies and requests as thread entries (#302)".

### Task 5: the thread in the app, its calls and events

**Files:**
- Create: `core/app/thread.go`, `core/app/thread_test.go`
- Modify: `core/app/app.go` (open the store in New at `AppSupport/chat` with
  `cfg.ChatKeepDays`; wire the hooks; register; on `RouteChat` call `SetKeepDays`),
  `core/app/chat.go` (`chat.send` adds your entry and takes an optional `session`),
  `core/app/app.go` `onVoiceEvent` (a `transcript` event adds an entry: role `user` is `You`,
  spoken; `assistant` is `Pixie`, spoken), and the `agent.decide` / `agent.choose` /
  `agent.typedAnswer` handlers (record the outcome).

**Interfaces:**
- Consumes: Task 1–2 Store, Task 4 hooks.
- Produces:
  - **dp_calls:**
    - `chat.entries {before?: string, limit?: int}` returns `{entries: []chat.Entry}`;
    - `chat.needs` returns `{entries}`;
    - `chat.read {upTo}` returns `{}` and calls `a.agent.RepliesShown(<line ids>)`;
    - `chat.clear` returns `{}`.
  - **Events:**
    - `chat.entry {entry}` on every Add or Update;
    - `chat.needs {entries}` when the open requests change;
    - `chat.badge {needs:int, unread:int, preview?:{id,who,text}}` after each change. The preview
      is the newest entry that isn't yours, sent only when that entry is new.
  - **`chat.send {text, attachments?, session?}`.** With `session`, the job text is `Tell the
    <label> session: <text>`; your entry is shown with "Replying to <label>" kept in
    `Entry.Sessions`.
  - **Outcomes:**
    - `agent.decide` allow gives `You allowed <text>`; deny gives `You denied <text>`;
    - `agent.choose` gives `You answered: <option>`;
    - `agent.typedAnswer` gives `You answered: <text>`;
    - a request missing from the next `ThreadRequests` with no outcome gives `Answered
      elsewhere`.

- [ ] **Step 1: Write the failing tests** in `thread_test.go`, using the existing harness with a
  fake agent that calls the hooks:
  - `TestSendingAddsYourMessage`;
  - `TestSendingToASessionAsksTheCoordinatorToTellIt` (the job text);
  - `TestVoiceTranscriptsAreYoursAndPixies`;
  - `TestAnUpdatesListSentTwiceAddsEachRequestOnce`;
  - `TestAllowingRecordsYouAllowed`;
  - `TestARequestAnsweredElsewhereIsRecordedSo`;
  - `TestReadingMarksTheLinesShown` (`RepliesShown` gets the line ids);
  - `TestTheBadgeCountsNeedsAndUnread` (preview only for a new entry, never yours);
  - `TestChatEntriesPagesBack`;
  - `TestChatKeepDaysIsLive`;
  - `TestTheThreadLivesInTheAppDataFolder` (the file is under the harness's AppSupport);
  - `TestAThreadThatCantOpenIsInMemory` (a file where `chat/` goes: it logs and keeps working).
- [ ] **Step 2:** Run `go test ./app/`. Expected: FAIL.
- [ ] **Step 3: Implement** `thread.go`. One `a.threadMu` guards the store's calls from hooks
  and dp_calls.
- [ ] **Step 4:** Run `cd core && gofmt -l . && go vet ./... && go test ./...`. Expected: PASS,
  with gofmt printing nothing.
- [ ] **Step 5: Commit:** "app: the chat thread, its calls and events (#302)". Push, and open
  PR 1 ("Part of #302"). Wait for CI and a second agent's review, then fix findings.

## Step 2 (PR 2): the window

### Task 6: Swift thread model

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Chat/ChatThread.swift`,
  `.../Chat/ChatThreadModel.swift`, `Tests/DeskPixieCoreTests/Chat/ChatThreadModelTests.swift`

**Interfaces:**
- Consumes: Task 5's calls and events.
- Produces:
  ```swift
  public struct ChatWho: Codable, Equatable, Sendable { kind, name, label, yours, status, color }
  public struct ChatRequest: Codable, Equatable, Sendable { token, item, kind, options, typed, outcome }
  public struct ChatEntry: Codable, Equatable, Sendable, Identifiable { id, at, who, text, spoken, sessions, lines, attachments, request, read }
  public struct ChatBadge: Codable, Equatable, Sendable { needs, unread, preview: Preview? }
  public protocol ChatClient: Sendable {
      func entries(before: String?, limit: Int) throws -> [ChatEntry]
      func needs() throws -> [ChatEntry]
      func read(upTo: String) throws
      func send(_ text: String, attachments: [String], session: String?) throws
      func decide(token: String, allow: Bool) throws
      func choose(token: String, option: Int) throws
      func typedAnswer(token: String, text: String) throws
      func stop() throws
  }
  @MainActor @Observable public final class ChatThreadModel {
      public private(set) var entries: [ChatEntry]   // oldest first, for drawing
      public private(set) var needs: [ChatEntry]
      public var replyTo: ChatWho?                    // the box's target; nil = Pixie
      public private(set) var thinking: Bool
      public private(set) var hasOlder: Bool
      public init(client: any ChatClient)
      public func load() async; public func loadOlder() async
      public func apply(_ e: ChatEntry); public func applyNeeds(_ n: [ChatEntry])
      public func sawBottom()  // reads up to the newest entry
      public func send(_ text: String, attachments: [String]) async
      public var placeholder: String { get }          // "Message Pixie…" | "Reply to <label>…"
      public static func needsLabel(_ n: Int) -> String // "1 needs you" | "N need you"
      public static func header(_ w: ChatWho) -> String // "<label> · your session|Pixie's agent · <status>"
  }
  ```

- [ ] **Step 1: Write the failing tests:**
  - `testLoadShowsTheLastPageOldestFirst`;
  - `testLoadOlderPrependsAndStopsAtTheStart`;
  - `testAnEntryWithAKnownIDReplacesInPlace`;
  - `testSendingToAReplyTargetPassesItsSessionAndClearsIt`;
  - `testPlaceholderAndNeedsLabelAndHeaderCopy` (the Global Constraints copy);
  - `testThinkingWhileATypedMessageWorks` (set on send, cleared by the coordinator entry that
    answers);
  - `testSawBottomReadsUpToTheNewest`;
  - `testAFailedCallSetsAnErrorAndKeepsEntries`.
- [ ] **Step 2:** Run `swift test --filter ChatThreadModelTests`. Expected: FAIL.
- [ ] **Step 3: Implement.** Calls run in `Task.detached`, as `AboutModel` does (#295). A load
  counter drops a stale result.
- [ ] **Step 4:** Run `swift test`. Expected: all pass.
- [ ] **Step 5: Commit:** "mac: the chat thread model (#302)".

### Task 7: events and the engine client

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift` (new cases
  `.chatEntry(ChatEntry)`, `.chatNeeds([ChatEntry])`, `.chatBadge(ChatBadge)`)
- Create: `.../Chat/EngineChatClient.swift`
- Modify: `mac/DeskPixie/Engine/EngineCoordinator.swift` (route to `ChatHub.model`)
- Test: the existing EngineEvent decode tests, plus a client-args test

- [ ] **Step 1: Write the failing tests:** `testDecodesChatEntryNeedsAndBadge` (from Go-shaped
  JSON), and `testChatClientSendsTheCoresArgs` (`chat.send` with `session` only when it's set).
- [ ] **Step 2:** Run `swift test`. Expected: FAIL.
- [ ] **Step 3: Implement.** `ChatHub` is like `SettingsHub`: one shared model, made on first
  use.
- [ ] **Step 4:** Run `swift test`. Expected: PASS.
- [ ] **Step 5: Commit:** "mac: chat events and client (#302)".

### Task 8: the window

**Files:**
- Create: `mac/DeskPixie/Chat/ChatWindowController.swift`, `ChatWindowView.swift`,
  `ChatEntryView.swift`, `ChatRequestCard.swift`, `ChatComposer.swift`
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Tray/TrayMenu.swift` (local id
  `chat`, title "Chat…", `Route.chat`), `mac/DeskPixie/MenuBar/MenuBarMenu.swift`

**Interfaces:**
- Consumes: `ChatThreadModel` (Task 6). It reuses `RichMessage`, `AttachmentChips` and the
  attachment drop handling from `QuickChatView`, and the request buttons' calls from
  `UpdatesBubbleController`.

The window:
- an `NSWindow` (titled, closable, miniaturizable, resizable), title "Pixie";
- frame autosave name `DeskPixieChatWindow`; minimum 400×480, default 720×760;
- a Dock presence while open, like Settings;
- shown only from `ChatWindowController.show(at: entryID?)`, which the menu item calls.

Layout, as in the mockup:
- the needs strip, whose pills scroll to `req<id>`;
- a `ScrollViewReader` thread in a column at most 680 pt wide;
- your bubbles on the right, with a mic mark when spoken;
- "Pixie" and "Coordinator" labels; session headers from `ChatThreadModel.header`, with a dot
  from the palette;
- request cards with Allow and Deny, options and Reply…, shrinking to the outcome line;
- "Open in terminal ›" calls `agent.focusSession`;
- "Thinking…";
- a "↓ Latest" button when scrolled up; `sawBottom()` is called when the newest entry appears;
- the composer, with the "Replying to <label> ×" chip;
- **Clear History…** in the window's menu: it asks first ("Clear the chat? This removes
  everything it kept on this Mac."), then calls `chat.clear`.

- [ ] **Step 1: Write the failing test:** `testChatIsALocalTrayRoute` (TrayMenu tests).
- [ ] **Step 2:** Run `swift test`. Expected: FAIL.
- [ ] **Step 3: Implement** the views and the menu item.
- [ ] **Step 4:** Run `swift test`, then the signed `make -C mac build`. Expected: PASS, BUILD
  SUCCEEDED.
- [ ] **Step 5: Check by hand** on an isolated build (AGENTS.md "Checking it end to end"):
  - open Chat… from the menu;
  - send a message: your bubble shows, then Thinking…, then the coordinator's answer;
  - a herdr test session's permission shows in the strip and as a card; Allow turns it into
    "You allowed …";
  - reopen the app: the thread is still there.

  Screenshot each step.
- [ ] **Step 6: Commit:** "mac: the chat window (#302)". Push and open PR 2. Review, CI, fixes.

### Task 8b: live text in the window (streaming)

What streams today shows in the window as it happens. Nothing live is stored: the thread keeps
the finished turns only.

**Go** (`core/app/thread.go`, `core/app/app.go`):
- A new event `chat.live {who, text, job?}`, where `who` is `you`, `pixie` or `coordinator`.
  An empty `text` clears that live line.
- Sources:
  - you: voice `heard` (the live caption of your words);
  - pixie: voice `saying` (Pixie's words while it speaks);
  - coordinator: a typed job's line while the job is still working, which `EntryForChat`
    drops today. It carries `job`.
- Clears:
  - your line clears on your final transcript, and Pixie's on Pixie's final transcript;
  - the coordinator's line clears when that job's done, failed or question line arrives, or
    when its job ends;
  - all lines clear on `chat.reset`.
- Test: `TestLiveLinesComeAndGo`, covering each source and each clear, and that nothing live is
  stored.

**Swift:**
- `ChatThreadModel` gets `live: [ChatLive]`, and `applyLive(_:)` replaces by `who`, plus
  `job` for the coordinator.
- `EngineEvent` gets `chat.live`, routed in `EngineCoordinator`.
- The view: your live words are a lighter bubble on the right; Pixie's live words are on the
  left, under the "Pixie" label, in the secondary colour. The coordinator's latest step is
  shown after "Thinking…" ("Thinking… · <step>").
- A live line that grows keeps the view at the bottom, the same as a growing row.
- Identifiers: `chat.live.you`, `chat.live.pixie`, `chat.live.coordinator`.
- Tests: `testLiveReplacesByWho`, `testAFinalEntryClearsItsLiveLine` (the model also clears on
  the matching `chat.entry`, in case events cross), and the event decode test.

- [ ] Write the failing tests (Go and Swift). Run them. Expected: FAIL.
- [ ] Implement. Run `go test ./...`, `swift test` and the signed build. Expected: PASS.
- [ ] Commit: "Chat window: live text while you and Pixie speak, and the coordinator's steps (#302)".

### Task 8a: UI tests for the window (XCUITest)

The app has no UI test target yet; this adds one and covers the window.

**Files:**
- Create: `mac/DeskPixieUITests/ChatWindowUITests.swift` and `UITestApp.swift`, a helper
  that builds an isolated data folder and launches the app.
- Modify: `mac/project.yml` (target `DeskPixieUITests`, type `bundle.ui-testing`, host
  `DeskPixie`, added to the DeskPixie scheme's test action), `mac/Makefile` (`uitest`:
  `xcodebuild test -scheme DeskPixie -only-testing:DeskPixieUITests`, with the same sign flags as
  `build`). No CI step: the UI tests run on this Mac, signed, before every push of PR 2
  (the maintainer's call, 2026-10-24).
- Modify the Task 8 views: add accessibility identifiers. Nothing else in the app changes.
  - `chat.window` and `chat.thread`;
  - `chat.entry.<id>` and `chat.needs.pill.<id>`;
  - `chat.request.allow`, `chat.request.deny` and `chat.request.reply`;
  - `chat.composer`, `chat.replyChip` and `chat.latest`.

**Isolation (AGENTS.md).** No test-only switch goes into the app. Instead, each test:
- makes a temporary folder;
- writes its `settings.json` there, with every value the app needs to skip onboarding, plus
  its own `agent.session` (`dp-uitest-<uuid>`) and a free `agent.port`;
- writes `chat/thread.jsonl` there in the store's own format, so a seeded thread comes from the
  real persistence code;
- launches the built app at its path, with `DESKPIXIE_SUPPORT_DIR` set to that folder;
- cleans up: terminates the app, runs `herdr session stop/delete` for its session if herdr is
  installed, and removes the folder.

**Your own app.** If a DeskPixie with the same bundle ID is already running, the test calls
`XCTSkip("Quit DeskPixie to run the UI tests")`. It never terminates an app it didn't launch.

**Tests that need no coordinator:**
- `testChatOpensFromTheMenuBar`: the status item's menu, Chat…, then the window `chat.window`.
- `testASeededThreadShowsWhoSaidWhat`: your bubble, "Pixie", "Coordinator", and a session
  header with its label and "Your session".
- `testANeedsPillScrollsToItsCardAndAllowShrinksIt`: pill, card, Allow; the strip loses the
  pill and the card reads "You allowed …".
- `testReplyPointsTheComposerAtTheSession`: Reply… shows the chip; × clears it.
- `testClearHistoryAsksFirstAndEmptiesTheThread`: Cancel keeps everything; Clear empties.
- `testTheThreadIsThereAfterARelaunch`.

**Live test** (`DESKPIXIE_UITEST_LIVE=1`, local only; needs herdr and an API key):
- `testASentMessageGetsTheCoordinatorsAnswer`: type, send, see Thinking…, then a coordinator
  entry.

- [ ] **Step 1: Write the UI tests** against the identifiers above. Run `make -C mac uitest`.
  Expected: FAIL, because the identifiers aren't there yet.
- [ ] **Step 2: Add the identifiers** and the target. Run `make -C mac uitest` signed
  (`SIGNING_IDENTITY=…`). Expected: PASS. Then run the live test once.
- [ ] **Step 3: Run the whole suite on this Mac** (your DeskPixie quit first, restarted after),
  and put the result in the PR description.
- [ ] **Step 4: Commit:** "mac: UI tests for the chat window (#302)".

## Step 3 (PR 3): withdrawn

The maintainer decided on 2026-10-24 that the chat window is an addition: the reply cards, the
updates badge and their settings stay. Tasks 9 and 10 (one badge and a preview instead of the
decks; removing the deck code and the `alwaysShowCaption` / `captionDismissSecs` settings) are not
built. PR 2 closes #302; it also adds a line about the window to README "Talking to Pixie".
