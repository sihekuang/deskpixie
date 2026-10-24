# Badge and Reply Deck Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only what needs the user goes in the badge, and everything else becomes a card in a reply deck under Pixie. Both are swipeable decks, and the behavior is the same in voice and text mode (#286, fixes #283).

**Architecture:**
- Go (`core/agent`) gives every outbox line a lane.
  - The bubble lists only alert-lane lines that are still unresolved.
  - Reply-lane lines become `ReplyCard`s. A card is emitted as `EfReply`, combined within 5 s, and marked delivered once the shell reports it shown.
- Swift draws two decks:
  - `QuickChatModel`'s history gains update cards.
  - `UpdatesBubbleModel` shows one alert at a time.
  - Both use one swipe model.

**Tech Stack:** Go 1.x core (`core/agent`, `core/app`), Swift 6 package `DeskPixieCore`, SwiftUI/AppKit app target.

**Spec:** `docs/superpowers/specs/2026-10-24-badge-and-reply-deck-design.md`
(mockup: https://claude.ai/artifact/SPnacFbSrxWTtYeFrwZYzd)

## Global Constraints

- **Lanes:**
  - Alert = a line with a token, `Kind == LineQuestion`, or `Asking`.
  - Reply = every other line.
- **Combining:** a card joins the previous one when the new line comes within `ReplyJoinMs = 5_000` of that card's last row.
- **Row text:**
  - `**<Title>:** <Brief or Text>` when the line has a title, else its text.
  - Rows are joined by `\n`.
- **Voice, conversation open:** no `EfReply`. The spoken caption is the card (spec call 1).
- **Combining never delays speech** (spec call 2).
- **When a badge item leaves:**
  - A token line: when it's no longer pending.
  - A session's `Asking` line: when that session's next turn arrives.
  - A job question: when its job leaves Question.
  - Seeing it, clicking it (which brings up its pane) and "Clear all" never clear it (spec call 3). "Clear all" only offers itself for reply-lane leftovers, of which there are none now, so it goes.
- **A card that isn't spoken pops up on its own** for `captionDismissSecs`, whatever `alwaysShowCaption` is (spec call 4).
- **Delivered = in the deck.** The shell calls `agent.repliesShown` with a card's `lines` when it takes the card in. At launch the shell pulls `agent.replies`: undelivered reply-lane lines younger than `replyHistoryMins` (spec call 5).
- **Every reply-lane line that reaches the outbox gets a card,** including lines over the cap and `LineUnsolicited` (spec call 6).
- **A question about a message typed in the box** goes in the card *and* the badge (spec call 7).
- Run before every push: `(cd core && gofmt -l . && go vet ./... && go test -race ./...)`, `(cd mac/Packages/DeskPixieCore && swift test)`, and the signed `make -C mac build`.
- Tests never touch the real home folder, settings or app data. Commits reference `(#286)`.

## Review Focus

1. **Open a conversation for updates on, voice mode, and the open fails** (no mic, a failed connect): the update must still become a card. Covered in Task 3's `TestAFailedOpenStillShowsTheCard`.
2. **A restart with undelivered updates:** recent ones come back as cards and old ones don't. Covered in Task 2's `TestPendingRepliesComeBackWithinTheHistory`.
3. **A session that asks, then answers itself on its next turn:** the badge drops it. Covered in Task 1's `TestASessionsNextTurnTakesItsQuestionOffTheBadge`.
4. **A typed message at work when an update arrives:** the card must not replace the typed answer, and the update still lands in history. Covered in Task 5's `testAnUpdateNeverCoversATypedMessageAtWork`.
5. **A swipe crossing several cards in one gesture:** it moves one card only. Covered in Task 6's `testOneGestureStepsOneCard`.

---

### Task 1: Lanes, and a bubble that only holds what needs you

**Files:**
- Modify: `core/agent/bubble.go` (`bubbleItems`, `updatesSeen`; add `alerting`, `unresolved`)
- Modify: `core/agent/controller.go`:
  - add `asking map[string]uint64` to `AgentController`, init in `NewAgentController`;
  - in `applyDelivery` `DeliverOutbox`, record `Asking` lines;
  - in `onSessionTurn`, clear the session's entry on any turn;
  - in `restore`, rebuild it.
- Test: `core/agent/bubble_test.go`

**Interfaces:**
- Produces:
  - `func alerting(l OutboxLine) bool`
  - `func (c *AgentController) unresolved(l OutboxLine) bool`
  - `c.asking[session] = outbox line id` of that session's open question.

- [ ] **Step 1: Write the failing tests** in `core/agent/bubble_test.go`:

```go
// #283/#286: with openConversationForUpdates on and text mode, an update reaches no bubble
// (it's a reply card) and a question does.
func TestTheBubbleHoldsOnlyWhatNeedsYou(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	c.Step(TextModeInput(true), T0)
	fx := sessionTurn(c, longTurn, T0+10) // "…is done": a reply
	if items, _ := lastUpdates(t, fx); len(items) != 0 {
		t.Fatalf("a done line in the bubble: %+v", items)
	}
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	items, _ := lastUpdates(t, sessionTurn(c, asking, T0+20))
	if len(items) != 1 || !items[0].Asking {
		t.Fatalf("%+v", items)
	}
}

func TestSeeingAQuestionDoesntTakeItOffTheBadge(t *testing.T) {
	c := ctlWith(nil)
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	items, _ := lastUpdates(t, sessionTurn(c, asking, T0))
	c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+10)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("seen cleared it: %+v", got)
	}
}

func TestASessionsNextTurnTakesItsQuestionOffTheBadge(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	sessionTurn(c, asking, T0)
	short := longTurn
	short.Duration = 1_000 // not told, but it moved on
	sessionTurn(c, short, T0+10)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAlertingLanes(t *testing.T) {
	tok := "hawk"
	for _, tc := range []struct {
		l    OutboxLine
		want bool
	}{
		{OutboxLine{Kind: LineQuestion, Token: &tok}, true},
		{OutboxLine{Kind: LineQuestion, Job: job(1)}, true},
		{OutboxLine{Kind: LineSession, Asking: true}, true},
		{OutboxLine{Kind: LineSession}, false},
		{OutboxLine{Kind: LineReply}, false},
		{OutboxLine{Kind: LineInfo}, false},
		{OutboxLine{Kind: LineUnsolicited}, false},
	} {
		if alerting(tc.l) != tc.want {
			t.Fatalf("%+v", tc.l)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail.** Run `cd core && go test ./agent -run 'Bubble|Badge|Alerting' -count=1`. Expected: FAIL, `alerting` undefined.

- [ ] **Step 3: Implement.**
  - **`bubbleItems`:** keep the outbox lines where `alerting(l) && c.unresolved(l)`, drop the `c.settings.OpenConversation` clause, and keep the item-building code as it is.
  - **`unresolved`:**
    - a token line: `IsPendingToken`;
    - `Asking` with a session: `c.asking[l.Session] == l.ID`;
    - a job question: the job is in `JobQuestion`;
    - otherwise: `!l.Delivered`.
  - **`updatesSeen` `keep`:** an alerting line is kept while `unresolved`, clicked or not. A reply-lane line falls through as before.
  - **`applyDelivery` `DeliverOutbox`:** after the `Push`, if `d.Line.Asking && d.Line.Session != ""`, set `c.asking[session]` to the new line's id (`c.outbox.lines[len-1].ID`).
  - **`onSessionTurn`:** first line `delete(c.asking, name)`.
  - **`restore`:** for each session, its newest outbox line, if it is `Asking`, becomes `c.asking`'s entry.

- [ ] **Step 4: Run the whole agent package.** Run `cd core && go test ./agent -count=1`. Expected: the new tests PASS. Existing bubble tests that assumed done lines are listed (e.g. `TestAnOutboxedLineIsSentToTheBubbleWithItsJob`) now FAIL by design. Change each to assert that the done line is **not** in the bubble, with a `// #286` comment. Re-run until green.

- [ ] **Step 5: Commit.** Run `git commit -am "agent: the bubble holds only what needs you (#286)"`.

---

### Task 2: Reply cards: emit, combine, shown, pending

**Files:**
- Create: `core/agent/replies.go`
- Create: `core/agent/replies_test.go`
- Modify: `core/agent/controller.go`:
  - `EfReply` effect kind and `Effect.Reply *ReplyCard`;
  - `InRepliesShown` input and `RepliesShownInput(ids []uint64)`;
  - `Settings.ReplyHistoryMs Millis`;
  - call `c.cardFor(line, now, fx)` from `applyDelivery`'s `DeliverOutbox` when `!alerting`.
- Modify: `core/agent/runtime.go`:
  - `Deps.Replies ReplySink` (nil: ignored);
  - `EfReply` → `d.Replies.Reply(*e.Reply)`;
  - `func (h *AgentHandle) RepliesShown(ids []uint64)`;
  - `func (h *AgentHandle) PendingReplies() []ReplyCard`.
- Modify: `core/agent/controller_helpers_test.go` (`fmtEffect` names `EfReply`).

**Interfaces:**
- Produces:

```go
const ReplyJoinMs Millis = 5_000

// ReplyCard is a card in the reply deck (#286): one update, or several that came within
// ReplyJoinMs, a row each.
type ReplyCard struct {
	ID       uint64   `json:"id"`    // its first line's outbox id
	Text     string   `json:"text"`  // the rows, one per line
	Count    int      `json:"count"` // how many rows
	Lines    []uint64 `json:"lines"` // outbox ids, for agent.repliesShown
	Sessions []string `json:"sessions,omitempty"`
	At       Millis   `json:"at"`
}
type ReplySink interface{ Reply(ReplyCard) }
func replyRow(l OutboxLine) string
func (c *AgentController) cardFor(l OutboxLine, now Millis, fx *[]Effect)
func (c *AgentController) PendingReplies(now Millis) []ReplyCard
```

- [ ] **Step 1: Write the failing tests** (`core/agent/replies_test.go`, package `agent`):

```go
func replies(fx []Effect) []ReplyCard {
	var out []ReplyCard
	for _, e := range fx {
		if e.Kind == EfReply {
			out = append(out, *e.Reply)
		}
	}
	return out
}

func TestADoneLineIsAReplyCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	cards := replies(sessionTurn(c, longTurn, T0))
	if len(cards) != 1 || cards[0].Count != 1 ||
		cards[0].Text != "**deskpixie: fix login redirect:** The redirect loop is fixed and all 412 tests pass." {
		t.Fatalf("%+v", cards)
	}
}

func TestUpdatesWithinFiveSecondsShareACard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	first := replies(sessionTurn(c, longTurn, T0))[0]
	other := longTurn
	other.Label = "live-like: benchmark"
	second := replies(c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "live-claude", Turn: &other}), T0+4_000))[0]
	if second.ID != first.ID || second.Count != 2 || len(second.Lines) != 2 || !strings.Contains(second.Text, "\n**live-like: benchmark:**") {
		t.Fatalf("%+v", second)
	}
	third := replies(sessionTurn(c, longTurn, T0+4_000+ReplyJoinMs+1))[0]
	if third.ID == first.ID || third.Count != 1 {
		t.Fatalf("a gap starts a new card: %+v", third)
	}
}

func TestEveryReplyLaneLineGetsACardEvenOverTheCap(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	if n := len(replies(sessionTurn(c, longTurn, T0+10))); n != 1 {
		t.Fatalf("over the cap: %d cards", n)
	}
}

func TestAShownCardIsDeliveredAndNotReplayed(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	card := replies(sessionTurn(c, longTurn, T0))[0]
	c.Step(RepliesShownInput(card.Lines), T0+10)
	if len(c.outbox.Undelivered()) != 0 {
		t.Fatal("still undelivered after it was shown")
	}
}

func TestPendingRepliesComeBackWithinTheHistory(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true; s.ReplyHistoryMs = 300_000 })
	sessionTurn(c, longTurn, T0)
	sessionTurn(c, longTurn, T0+400_000)
	got := c.PendingReplies(T0 + 500_000)
	if len(got) != 1 || got[0].At != T0+400_000 {
		t.Fatalf("%+v", got)
	}
}

func TestVoiceOpenSendsNoCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	c.Step(VoiceInput(VoiceSignal{Kind: voice.SigOpened}), T0)
	if n := len(replies(sessionTurn(c, longTurn, T0+10))); n != 0 {
		t.Fatal("a card for a spoken line: the caption is its card")
	}
}
```

- [ ] **Step 2: Run them to see them fail.** Run `cd core && go test ./agent -run 'Reply|Replies|Card' -count=1`. Expected: FAIL, undefined `EfReply`.

- [ ] **Step 3: Implement.**
  - **`cardFor`:**
    - Keep `c.lastCard *ReplyCard` and `c.lastRowAt Millis`.
    - If `c.lastCard != nil && now-c.lastRowAt <= ReplyJoinMs`, append a row (`Text += "\n" + replyRow(l)`, `Count++`, `Lines = append(...)`, merge `Sessions`).
    - Else start a new card `{ID: l.ID, Text: replyRow(l), Count: 1, ...}`.
    - Set `lastRowAt = now`, then emit `Effect{Kind: EfReply, Reply: &copy}`.
  - **`RepliesShownInput`:** marks the given ids delivered with `outbox.MarkDeliveredWhere(id in set)` and appends the records.
  - **`PendingReplies`:** walks the undelivered, non-alerting outbox lines with `now-At <= ReplyHistoryMs` in order, grouping by the same 5 s rule, and returns the cards.
  - **`runtime.go`:**
    - `PendingReplies` takes `ctlMu` and uses `h.deps.Clock.Now()`.
    - `RepliesShown` sends the input.

- [ ] **Step 4: Run.** Run `cd core && go test ./agent -count=1`. Expected: PASS.

- [ ] **Step 5: Commit.** Run `git commit -am "agent: reply cards, combined within 5 s, delivered once shown (#286)"`.

---

### Task 3: Voice parity: spoken questions wait on the badge; open-for-updates holds the card until it can't open

**Files:**
- Modify: `core/agent/controller.go`:
  - in `deliver`: when `c.voiceOpen && alerting(asOutbox(line))`, also `PushHeard` the line;
  - for a typed job, a question with no token also gets `PushHeard` (spec call 7).
- Modify: `core/agent/replies.go`: `cardFor` holds the card (`c.heldCards = append(...)`) while `c.settings.OpenConversation && !c.textMode && !c.voiceOpen && !c.away`.
- Modify: `core/agent/opener.go`: when an open fails, emit the held cards via `c.releaseHeldCards(now, fx)`:
  - `openerSaw(SigClosed)` while waiting;
  - an `OpenWaitMs` expiry in `tryOpen`.
- Modify: on `voice.SigOpened`, drop `heldCards` (the conversation says them, and the caption is the card).
- Test: `core/agent/replies_test.go`

**Interfaces:**
- Consumes: Task 2's `cardFor`, `ReplyCard`, `EfReply`.
- Produces: `func (c *AgentController) releaseHeldCards(now Millis, fx *[]Effect)`.

- [ ] **Step 1: Write the failing tests:**

```go
func TestOpenForUpdatesHoldsTheCardWhileItOpens(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	fx := sessionTurn(c, longTurn, T0)
	if len(replies(fx)) != 0 {
		t.Fatal("a card while Pixie opens a conversation to say it")
	}
	wantContains(t, fx, Effect{Kind: EfOpenConversation}, "opens")
}

func TestAFailedOpenStillShowsTheCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	fx := c.Step(VoiceInput(VoiceSignal{Kind: voice.SigClosed}), T0+1_000) // failed connect
	if len(replies(fx)) != 1 {
		t.Fatal(fmtEffects(fx))
	}
}

func TestTextModeShowsTheCardWithOpenForUpdatesOn(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	c.Step(TextModeInput(true), T0)
	if len(replies(sessionTurn(c, longTurn, T0+10))) != 1 {
		t.Fatal("no card in text mode")
	}
}

func TestASpokenQuestionWaitsOnTheBadge(t *testing.T) {
	c := workingOnJob1()
	c.Step(VoiceInput(VoiceSignal{Kind: voice.SigOpened}), T0)
	stop(c, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`, T0+10)
	if got := c.Updates(); len(got) != 1 || got[0].Kind != LineQuestion {
		t.Fatalf("%+v", got)
	}
}

func TestATypedJobsQuestionIsInTheCardAndOnTheBadge(t *testing.T) {
	c := typedOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "question", Text: "Which branch?"})
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail.** Run `cd core && go test ./agent -run 'Open|Spoken|Typed' -count=1`. Expected: the new tests FAIL.
- [ ] **Step 3: Implement as in Files.** The existing `TestTextModeOpensNoConversationForUpdates` must still pass.
- [ ] **Step 4: Run.** Run `cd core && go test -race ./agent -count=1`. Expected: PASS.
- [ ] **Step 5: Commit.** Run `git commit -am "agent: voice and text alike for the badge and the deck (#286)"`.

---

### Task 4: App plumbing: `reply.card`, `agent.repliesShown`, `agent.replies`

**Files:**
- Modify: `core/app/agent.go`:
  - `AgentHooks.Reply func(card any)`;
  - `Agent` interface: `RepliesShown(ids []uint64)` and `PendingReplies() any`.
- Modify: `core/app/app.go`:
  - hook `Reply: func(c any) { a.emit("reply.card", fieldsOf(c)) }`;
  - register `agent.repliesShown` (`{ids}` → `RepliesShown`, `{}` reply);
  - register `agent.replies` (→ `{"cards": PendingReplies()}`; `{"cards":[]}` with no agent).
- Modify: `core/app/agentapp/desktop.go`: `replySink{hooks}`.
- Modify: `core/app/agentapp/agentapp.go`:
  - `Deps.Replies: replySink{c.Hooks}`;
  - `Settings.ReplyHistoryMs: agent.Millis(c.ReplyHistoryMins) * 60_000`;
  - `RepliesShown` and `PendingReplies` methods.
- Modify: the fakes implementing `app.Agent` in tests (`core/app/agent_test.go` `fakeAgent`).
- Test: `core/app/agent_test.go`

**Interfaces:**
- Consumes: `AgentHandle.RepliesShown`, `AgentHandle.PendingReplies` (Task 2).
- Produces: the event `reply.card` = a `ReplyCard`'s JSON. The calls are `agent.repliesShown {ids:[...]}` and `agent.replies → {cards:[ReplyCard]}`.

- [ ] **Step 1: Write the failing test:**

```go
func TestRepliesCallsReachTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.repliesShown", map[string]any{"ids": []uint64{3, 4}})
	if got := f.shownReplies(); !reflect.DeepEqual(got, []uint64{3, 4}) {
		t.Fatal(got)
	}
	if out := h.call("agent.replies", map[string]any{}); !strings.HasPrefix(string(out), `{"cards":`) {
		t.Fatal(string(out))
	}
}
```

- [ ] **Step 2: Run it to see it fail.** Run `cd core && go test ./app -run Replies -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement as in Files.**
- [ ] **Step 4: Run.** Run `cd core && gofmt -l . && go vet ./... && go test -race ./... -count=1`. Expected: clean and PASS.
- [ ] **Step 5: Commit.** Run `git commit -am "app: reply.card event and the reply deck's calls (#286)"`.

---

### Task 5: Swift model: update cards in the reply deck

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/QuickChatModel.swift`:
  - add a `ReplyCard` Decodable (`id: UInt64, text, count, lines: [UInt64], sessions`);
  - `Reply` gains `id: UInt64?` and `count: Int`;
  - add `public var cardID: UInt64?` and `public var count: Int` for the shown card.
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift`: `case "reply.card": .replyCard(ReplyCard)`.
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/QuickChatModelTests.swift`, `EngineEventTests.swift`

**Interfaces:**
- Consumes: the `reply.card` JSON (Task 4).
- Produces: `public mutating func update(_ card: ReplyCard, now: Date = Date()) -> Bool`. It returns true when the card shows now; false means it went straight into history.

Behavior:
- **The same `cardID` is up:** replace `full`/`shown` with the card's text, no typing out, and update `count`.
- **A typed message is at work** (the `takeCaption()` rule refuses): append it to `history` as a `Reply` and set `newerWaiting`.
- **Otherwise:** `keep(now)`, then show it as a finished, non-live caption (`isCaption = true`, `captionLive = false`, `phase = .done`, `full = shown = text`, `saidAt = now`, `lateShown = true`).
  - It shows on its own (`showsCard` is true via `lateShown`).
  - `dismissIfOlder` puts it away after `captionDismissSecs`.

- [ ] **Step 1: Write the failing tests:**

```swift
func card(_ id: UInt64, _ text: String, count: Int = 1) -> ReplyCard {
    ReplyCard(id: id, text: text, count: count, lines: [id], sessions: [])
}

func testAnUpdateShowsOnItsOwnAndGoesIntoHistory() {
    var m = QuickChatModel()
    XCTAssertTrue(m.update(card(1, "**live-like:** done")))
    XCTAssertTrue(m.showsCard(hovering: false, always: false))
    XCTAssertEqual(m.shownFull, "**live-like:** done")
    _ = m.update(card(2, "**api:** done"))
    XCTAssertEqual(m.position, QuickChatModel.Position(index: 2, count: 2))
}

func testARowJoiningTheShownCardReplacesItInPlace() {
    var m = QuickChatModel()
    _ = m.update(card(1, "**a:** one"))
    _ = m.update(card(1, "**a:** one\n**b:** two", count: 2))
    XCTAssertEqual(m.count, 2)
    XCTAssertNil(m.position) // still one card, nothing to step back to
}

func testAnUpdateNeverCoversATypedMessageAtWork() {
    var m = QuickChatModel()
    m.mode = "text"; m.open = true
    m.sending(); _ = m.sent(job: "job_1")
    XCTAssertFalse(m.update(card(1, "**a:** done")))
    XCTAssertEqual(m.phase, .thinking)
    XCTAssertEqual(m.history.last?.text, "**a:** done")
}

func testAnUpdateCardIsPutAwayAfterTheDismissTime() {
    var m = QuickChatModel()
    let t0 = Date()
    _ = m.update(card(1, "**a:** done"), now: t0)
    XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(121)))
}
```

  In `EngineEventTests.swift`, decode `{"name":"reply.card","data":{"id":3,"text":"x","count":1,"lines":[3]}}` to `.replyCard`.

- [ ] **Step 2: Run them to see them fail.** Run `cd mac/Packages/DeskPixieCore && swift test --filter 'QuickChatModelTests|EngineEventTests'`. Expected: FAIL, `ReplyCard` undefined.
- [ ] **Step 3: Implement as in Behavior.**
- [ ] **Step 4: Run.** Run `swift test`. Expected: PASS.
- [ ] **Step 5: Commit.** Run `git commit -am "DeskPixieCore: update cards in the reply deck (#286)"`.

---

### Task 6: Swift models: one swipe, and the badge deck

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/DeckSwipe.swift`
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/UpdatesBubbleModel.swift`:
  - `public private(set) var deckIndex = 0`;
  - `public var current: UpdateLine?`;
  - `public var deckPosition: (index: Int, count: Int)?` (nil with fewer than two);
  - `public mutating func step(_ delta: Int)`;
  - `apply` and `dismiss` clamp `deckIndex`.
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/DeckSwipeTests.swift` (new), `UpdatesBubbleModelTests.swift`

**Interfaces:**
- Produces:

```swift
/// A trackpad swipe as deck steps (#286): one card per gesture, whatever its length.
public struct DeckSwipe: Equatable, Sendable {
    public static let threshold: CGFloat = 30
    public enum Phase: Sendable { case began, changed, ended, momentum }
    public init()
    /// −1 (older), +1 (newer) or 0. A vertical-dominant or momentum event steps nothing.
    public mutating func feed(deltaX: CGFloat, deltaY: CGFloat, phase: Phase) -> Int
}
```

  Sign: content follows the fingers, so a swipe to the right (positive `deltaX` with natural scrolling) shows the **older** card, −1.

- [ ] **Step 1: Write the failing tests:**

```swift
func testOneGestureStepsOneCard() {
    var s = DeckSwipe()
    XCTAssertEqual(s.feed(deltaX: 0, deltaY: 0, phase: .began), 0)
    XCTAssertEqual(s.feed(deltaX: 40, deltaY: 2, phase: .changed), -1)
    XCTAssertEqual(s.feed(deltaX: 200, deltaY: 0, phase: .changed), 0)
    XCTAssertEqual(s.feed(deltaX: 0, deltaY: 0, phase: .ended), 0)
    XCTAssertEqual(s.feed(deltaX: -40, deltaY: 0, phase: .began), 0)
    XCTAssertEqual(s.feed(deltaX: -40, deltaY: 0, phase: .changed), 1)
}

func testVerticalScrollingAndMomentumStepNothing() {
    var s = DeckSwipe()
    XCTAssertEqual(s.feed(deltaX: 40, deltaY: 80, phase: .changed), 0)
    XCTAssertEqual(s.feed(deltaX: 90, deltaY: 0, phase: .momentum), 0)
}
```

  In `UpdatesBubbleModelTests.swift`:

```swift
func testTheBadgeDeckShowsOneAtATimeAndClampsWhenOneIsAnswered() {
    var m = model()
    m.apply([line(1), line(2), line(3)], now: 0, pop: .allowed)
    XCTAssertEqual(m.current?.id, 1)
    m.step(1); m.step(1); m.step(1)
    XCTAssertEqual(m.current?.id, 3)
    m.apply([line(1), line(2)], now: 1, pop: .allowed) // 3 was answered
    XCTAssertEqual(m.current?.id, 2)
    XCTAssertEqual(m.deckPosition?.index, 2)
}
```

  (Use the file's existing `model()`/`line(_:)` helpers, adding them if missing.)

- [ ] **Step 2: Run them to see them fail.** Run `swift test --filter 'DeckSwipeTests|UpdatesBubbleModelTests'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `DeckSwipe` accumulates `deltaX` from `.began`, fires once when `|sum| >= threshold` and horizontal dominates, then stays silent until the next `.began`.
- [ ] **Step 4: Run.** Run `swift test`. Expected: PASS.
- [ ] **Step 5: Commit.** Run `git commit -am "DeskPixieCore: one swipe per card, and the badge deck (#286)"`.

---

### Task 7: App target: wire the cards, draw both decks

**Files:**
- Modify: `mac/DeskPixie/Engine/EngineCoordinator.swift`:
  - on `.replyCard(let c)`: `roster?.replyCard(c)`;
  - at startup, after the agent is up, call `agent.replies` and feed each card the same way.
- Modify: `mac/DeskPixie/Pixie/AgentRosterController.swift`: `func replyCard(_ c: ReplyCard)`, which does:
  1. `chat.update(c)`;
  2. `call("agent.repliesShown", ["ids": c.lines])`;
  3. relayout.
- Modify: `mac/DeskPixie/Pixie/QuickChatView.swift` (`QuickChatCard`):
  - **Header:** "Pixie · N updates · <ago>" when `count > 1`.
  - **Peeks:** behind the card, up to two peek layers when `position.index > 1`: inset 8 pt per side, raised 7 pt, opacity 0.75 and 0.55.
  - **Swipe:** a horizontal-swipe catcher. It's an `NSViewRepresentable` whose view overrides `scrollWheel(with:)`; it uses `hasPreciseScrollingDeltas` and maps `phase`/`momentumPhase` into `DeckSwipe`. −1 → `back`, +1 → `forward`.
- Modify: `mac/DeskPixie/Pixie/UpdatesBubbleView.swift`:
  - draw `model.current` as one card with its existing row content (buttons, menu, typed answer, "Chat about this");
  - header "‹ i of n ›" from `deckPosition`;
  - one peek layer when `count > 1`;
  - the same swipe catcher, calling `step`.
- Modify: `mac/DeskPixie/Pixie/UpdatesBubbleController.swift`:
  - pass `current`/`deckPosition`, `onStep: { model.step($0); render() }`;
  - a click on a card brings up its pane without dismissing it locally;
  - remove "Clear all" (`offersClearAll`, `dismissAll` and its view button).

  The core drops the card once it's resolved.

**Interfaces:**
- Consumes: `QuickChatModel.update`, `ReplyCard`, `DeckSwipe`, `UpdatesBubbleModel.current/deckPosition/step` (Tasks 5–6), the `reply.card` event and the `agent.repliesShown` / `agent.replies` calls (Task 4).

- [ ] **Step 1: Build.** Run `make -C mac build SIGNING_IDENTITY="Developer ID Application: Daniel Lee (TULKQWS4XL)" DEVELOPMENT_TEAM=TULKQWS4XL`. Expected: BUILD SUCCEEDED.
- [ ] **Step 2: Commit.** Run `git commit -am "mac: the reply deck and the badge deck (#286)"`.

---

### Task 8: End to end, then the PR

- [ ] **Step 1: Set up an isolated run.** Use `DESKPIXIE_SUPPORT_DIR=<scratch>` with a copy of the user's settings, with its own `agent.session` and `agent.port` (text mode, open-for-updates on: the #283 setup). Start a pix claude session in tmux with `env -u PIX_SOCK -u PIX_SESSION`.
- [ ] **Step 2: Check each case.** Watch the app's windows (`CGWindowListCopyWindowInfo`, `screencapture -l`):
  - A session's "done" → a reply card under Pixie, and no badge.
  - Two sessions finishing within 5 s → one card with two rows.
  - A session asking → badge "1". Hover → its card; its button brings up the terminal.
  - A two-finger swipe on the card → the older card.
  - Voice mode with open-for-updates off: "done" → a silent card.
- [ ] **Step 3: Clean up.** Stop the test app, kill tmux, `herdr session delete` its session, and remove the scratch folders (only empty ones, with `rmdir`).
- [ ] **Step 4: Push and open the PR.** Run the full test set, push, then open a PR closing #286 and #283, saying what was checked by hand. Get a second-agent review and wait for CI on the head commit.
