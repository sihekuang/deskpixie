# Updates Bubble Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A bubble beside Bit that shows the agent updates you haven't heard yet: it pops briefly, collapses to a count badge, expands on hover/click, marks what you viewed as heard, and focuses the job's herdr pane when you click a line. Nothing pops or starts a conversation while you're away.

**Architecture:** The Go core already keeps unheard lines in the outbox. The controller emits the current undelivered list as a new `EfUpdates` effect after every step (only while `agent.openConversationForUpdates` is off). The runtime hands it to the app, which emits an `updates` engine event. Swift shows it in a child `NSPanel` of the mascot panel, driven by a pure `UpdatesBubbleModel`. The shell calls back `agent.updatesSeen`, `agent.focusJob` and `presence.set`. The update debounce becomes the `notifications.debounceSecs` setting.

**Tech Stack:** Go 1.x (core, `go test -race ./...` from `core/`), Swift 6 / AppKit + SwiftUI (`swift test` from `mac/Packages/DeskPixieCore`, app built with `make -C mac`).

**Spec:** `docs/specs/2026-10-14-updates-bubble-design.md`

## Global Constraints

- Settings paths, labels, defaults and ranges exactly as spec §5.3:
  `notifications.debounceSecs` 20 (0–300, restart) · `notifications.bubble.enabled` true (live) · `notifications.bubble.popSecs` 6 (1–60, live) · `notifications.bubble.lines` 5 (1–10, live) · `notifications.awayAfterMins` 5 (1–60, live).
- Max hold = 1.5 × debounce (20 s → 30 s). Debounce 0 = no coalescing; dedupe still applies. Questions never debounced.
- "Seen" after the bubble has been expanded ≥ 1 s (fixed, not a setting).
- `agent.notifyPerMinute` keeps its path; only its Section changes to Notifications.
- Old `outbox.jsonl` records without `job`/`worker` must still restore.
- The bubble feed is off when `agent.openConversationForUpdates` is on.
- While away: no pop, no chime, no conversation opened by Bit; one pop on return if anything arrived.
- Code style: match surrounding code; comments as dense as neighbours; no new dependencies.
- Commits path-scoped, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never read `.env`.

## Rulings made while planning

- **Focus / Do Not Disturb detection is not implemented.** The only API (`INFocusStatusCenter`) needs a Communication Notifications entitlement and a user permission prompt. Fullscreen detection (menu bar hidden) is implemented. Logged in `docs/decisions.md` in Task 7.
- **Line → pane:** a line carries `Job` and, when it is about one worker, `Worker`. Focus target: that worker if live, else the job's first live worker, else the coordinator.
- **Expanded list stays stable:** after the 1 s "seen" call the core sends a shorter list; the expanded bubble keeps showing what it showed (plus newly added lines) until the pointer leaves.
- **Launch:** the first list after launch never pops (badge only).

## Review Focus

1. Old `outbox.jsonl` without the new fields restores and is shown — Task 3 test.
2. A pending permission question is not cleared by viewing — Task 3 + Task 4 tests.
3. Away → update → back gives exactly one pop; nothing pops while away — Task 6 model test.
4. With `openConversationForUpdates` on, no `EfUpdates` is ever emitted — Task 4 test.
5. Bubble near the right/top screen edge stays fully on screen — Task 6 placement test.

---

### Task 1: Notifications settings block and section

**Files:**
- Modify: `core/settings/config.go` (Config, Default, Clone not needed, `configFields`, `knownKeys`)
- Modify: `core/settings/schema.go` (Section const, Fields)
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Settings/SettingsSnapshot.swift` (section enum)
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Settings/SettingsLayout.swift` (tab)
- Test: `core/settings/config_test.go`, `core/settings/schema_test.go` (or wherever Fields are tested), Swift settings tests that enumerate sections/tabs

**Interfaces:**
- Produces: `settings.NotificationSettings{DebounceSecs uint64; Bubble BubbleSettings; AwayAfterMins uint64}`, `settings.BubbleSettings{Enabled bool; PopSecs uint64; Lines uint64}`, `Config.Notifications`, `SectionNotifications = "Notifications"`.

- [ ] **Step 1: Failing tests** in `core/settings/config_test.go`:

```go
func TestSettingsWithoutANotificationsBlockGetDefaults(t *testing.T) {
	c := parseConfig(t, `{"pack":"bit"}`)
	want := NotificationSettings{DebounceSecs: 20, Bubble: BubbleSettings{Enabled: true, PopSecs: 6, Lines: 5}, AwayAfterMins: 5}
	if !reflect.DeepEqual(c.Notifications, want) {
		t.Fatalf("%+v", c.Notifications)
	}
}

func TestNotificationsBlockUsesCamelCase(t *testing.T) {
	j := mustJSON(t, Default())
	want := map[string]any{"debounceSecs": float64(20), "awayAfterMins": float64(5),
		"bubble": map[string]any{"enabled": true, "popSecs": float64(6), "lines": float64(5)}}
	if !reflect.DeepEqual(j["notifications"], want) {
		t.Fatal(j["notifications"])
	}
}
```

And a schema test (next to the existing Fields tests):

```go
func TestNotificationFieldsAreInTheNotificationsSection(t *testing.T) {
	for _, p := range []string{"notifications.debounceSecs", "notifications.bubble.enabled", "notifications.bubble.popSecs",
		"notifications.bubble.lines", "notifications.awayAfterMins", "agent.notifyPerMinute"} {
		f := FieldByPath(p)
		if f == nil || f.Section != SectionNotifications {
			t.Fatalf("%s: %+v", p, f)
		}
	}
}
```

Also add a validation test in the existing range-validation style: `notifications.bubble.lines` = 11 is rejected, `notifications.debounceSecs` = 0 is accepted.

- [ ] **Step 2:** `cd core && go test ./settings/` → FAIL (undefined `NotificationSettings`).

- [ ] **Step 3: Implement.** In `config.go`:

```go
// NotificationSettings is the notifications block: how agent updates reach you.
type NotificationSettings struct {
	// DebounceSecs: a job's worker reports and progress go out once it has been quiet this long
	// (at most 1.5× this); 0 lets each one out as it comes.
	DebounceSecs uint64         `json:"debounceSecs"`
	Bubble       BubbleSettings `json:"bubble"`
	// AwayAfterMins: this long with no keyboard or mouse input counts as away.
	AwayAfterMins uint64 `json:"awayAfterMins"`
}

// BubbleSettings is notifications.bubble: the updates bubble beside Bit.
type BubbleSettings struct {
	Enabled bool   `json:"enabled"`
	PopSecs uint64 `json:"popSecs"`
	Lines   uint64 `json:"lines"`
}
```

Add `Notifications NotificationSettings \`json:"notifications"\`` after `Agent` in `Config` and in `configFields`; add `"notifications": true` to `knownKeys`; in `Default()` add
`Notifications: NotificationSettings{DebounceSecs: 20, Bubble: BubbleSettings{Enabled: true, PopSecs: 6, Lines: 5}, AwayAfterMins: 5},`.
Follow exactly how the `canvas` block gets its defaults when missing (look at how `Canvas` is filled during unmarshal and mirror it).

In `schema.go`: add `SectionNotifications Section = "Notifications"` between Workers and Canvas; change `agent.notifyPerMinute`'s section to `SectionNotifications`; add after it:

```go
	spec("notifications.debounceSecs", SectionNotifications, "Notification debounce (seconds)", "A job's updates wait until it has been quiet this long, then go out together. 0 sends each one as it comes. Questions never wait.", intKind(0, 300, false), ApplyRestart),
	spec("notifications.bubble.enabled", SectionNotifications, "Updates bubble", "Shows what happened since you last talked to Bit in a bubble beside it.", boolKind, ApplyLive),
	spec("notifications.bubble.popSecs", SectionNotifications, "Pop for (seconds)", "How long the bubble shows a new update before it shrinks to a badge.", intKind(1, 60, false), ApplyLive),
	spec("notifications.bubble.lines", SectionNotifications, "Updates shown", "The most updates the bubble lists.", intKind(1, 10, false), ApplyLive),
	spec("notifications.awayAfterMins", SectionNotifications, "Away after (minutes idle)", "With no keyboard or mouse input for this long, nothing pops and Bit doesn't start a conversation until you're back.", intKind(1, 60, false), ApplyLive),
```

Swift: add `case notifications = "Notifications"` to the section enum (between workers and canvas) and a tab `Tab(section: .notifications, title: "Notifications", symbol: "bell.badge")` in the same position in `SettingsLayout.swift`. Update any Swift test that lists all sections/tabs.

- [ ] **Step 4:** `cd core && go test ./settings/ ./app/...` and `cd mac/Packages/DeskPixieCore && swift test` → PASS. Fix any golden/snapshot test of the full schema or default JSON by adding the new block (not by deleting assertions).

- [ ] **Step 5: Commit** `settings: notifications section (debounce, updates bubble, away)`.

---

### Task 2: The update debounce follows `notifications.debounceSecs`

**Files:**
- Modify: `core/agent/debounce.go`, `core/agent/controller.go` (Settings, DefaultSettings, NewAgentController), `core/app/agentapp/agentapp.go`
- Test: `core/agent/debounce_test.go`, `core/app/agentapp/wiring_test.go`

**Interfaces:**
- Consumes: `settings.Config.Notifications.DebounceSecs` (Task 1).
- Produces: `agent.Settings.UpdateQuietMs Millis`; `NewUpdatesWith(quiet Millis) *Updates`. `NewUpdates()` stays (defaults). Constants `UpdateQuietMs`, `UpdateMaxHoldMs` stay as the defaults.

- [ ] **Step 1: Failing tests** in `debounce_test.go`:

```go
func TestTheQuietTimeIsASettingAndTheMaxHoldFollowsIt(t *testing.T) {
	u := NewUpdatesWith(10_000)
	u.AddReport(job(1), "w", "first", T0)
	wantDue(t, u.Due(T0+9_999, always))
	wantDue(t, u.Due(T0+10_000, always), DueUpdate{Job: job(1), Text: "first"})
	// Keeps reporting every 5 s: still heard at 1.5× = 15 s.
	u.AddReport(job(2), "w", "a", T0)
	u.AddReport(job(2), "w", "b", T0+5_000)
	u.AddReport(job(2), "w", "c", T0+10_000)
	wantDue(t, u.Due(T0+15_000, always), DueUpdate{Job: job(2), Text: "c"})
}

func TestAZeroQuietTimeLetsEachUpdateOutAtOnce(t *testing.T) {
	u := NewUpdatesWith(0)
	u.AddReport(job(1), "w", "done", T0)
	wantDue(t, u.Due(T0, always), DueUpdate{Job: job(1), Text: "done"})
	if u.AddReport(job(1), "w", "done", T0+1) {
		t.Fatal("dedupe still applies")
	}
}
```

(If `wantDue` with no wants doesn't exist as "none due", use the file's existing idiom for an empty result.)

In `wiring_test.go`, following its existing style, assert the runtime config built from a `settings.Config` with `Notifications.DebounceSecs = 7` has `Settings.UpdateQuietMs == 7000`.

- [ ] **Step 2:** `cd core && go test ./agent/ ./app/agentapp/` → FAIL.

- [ ] **Step 3: Implement.** `Updates` gets `quiet, maxHold Millis`; `NewUpdates()` returns `NewUpdatesWith(UpdateQuietMs)`;

```go
// NewUpdatesWith: quiet is notifications.debounceSecs; a job that keeps reporting is still
// heard after 1.5× that.
func NewUpdatesWith(quiet Millis) *Updates {
	return &Updates{held: map[JobID]*heldUpdate{}, said: map[JobID][]saidLine{}, quiet: quiet, maxHold: quiet * 3 / 2}
}
```

`Due` uses `u.quiet` / `u.maxHold` instead of the constants. Update the file's header comment to say the times come from the setting (defaults below). In `controller.go`: add `UpdateQuietMs Millis` to `Settings` (comment: `notifications.debounceSecs`), set it to `UpdateQuietMs` in `DefaultSettings()`, and build `updates: NewUpdatesWith(settings.UpdateQuietMs)`. Grep tests for `Settings{` literals that don't start from `DefaultSettings()` and would now get 0; set `UpdateQuietMs: UpdateQuietMs` there. In `agentapp.go` set `UpdateQuietMs: agent.Millis(cfg.Notifications.DebounceSecs) * 1000` (use whatever variable holds the full `settings.Config` there; thread it in if only `cfg.Agent` is in scope).

- [ ] **Step 4:** `cd core && go test -race ./agent/ ./app/...` → PASS.
- [ ] **Step 5: Commit** `agent: the update debounce follows notifications.debounceSecs`.

---

### Task 3: Lines remember their job and worker; Outbox.MarkSeen

**Files:**
- Modify: `core/agent/routing.go` (Line), `core/agent/outbox.go`, the `NewLine` call sites in `core/agent/controller.go`, `controller_workers.go`, `liveness.go`, `debounce.go` (DueUpdate)
- Test: `core/agent/outbox_test.go`, `core/agent/debounce_test.go`

**Interfaces:**
- Produces: `Line.Job JobID`, `Line.Worker string`, `NewJobLine(kind LineKind, text string, job JobID) Line`; `OutboxLine.Job JobID \`json:"job,omitempty"\``, `OutboxLine.Worker string \`json:"worker,omitempty"\``; `DueUpdate.Worker string` (set when the update is one worker's report); `func (o *Outbox) MarkSeen(ids []uint64, keep func(OutboxLine) bool) []string`.

- [ ] **Step 1: Failing tests** in `outbox_test.go`:

```go
func TestPushKeepsTheLinesJobAndWorker(t *testing.T) {
	o := NewOutbox()
	l := NewJobLine(LineInfo, "The upload worker says it's done.", job(3))
	l.Worker = "w-upload"
	o.Push(l, T0)
	got := o.Undelivered()[0]
	if got.Job != job(3) || got.Worker != "w-upload" {
		t.Fatalf("%+v", got)
	}
	r := RestoreOutbox(o.Lines(), T0+1).Undelivered()[0]
	if r.Job != job(3) || r.Worker != "w-upload" {
		t.Fatalf("restored %+v", r)
	}
}

func TestAnOldRecordWithoutJobRestores(t *testing.T) {
	o := RestoreOutbox([]string{`{"id":1,"text":"hi","kind":"info","token":null,"at":5,"delivered":false}`}, 10)
	if u := o.Undelivered(); len(u) != 1 || u[0].Job != "" || u[0].Worker != "" {
		t.Fatalf("%+v", u)
	}
}

func TestMarkSeenDeliversTheShownLinesButKeepsWhatKeepSays(t *testing.T) {
	o := NewOutbox()
	o.Push(NewLine(LineReply, "a"), T0)
	o.Push(NewLine(LineQuestion, "b?"), T0)
	o.Push(NewLine(LineReply, "c"), T0)
	recs := o.MarkSeen([]uint64{1, 2, 99}, func(l OutboxLine) bool { return l.Kind == LineQuestion })
	if len(recs) != 1 {
		t.Fatalf("records %v", recs)
	}
	u := o.Undelivered()
	if len(u) != 2 || u[0].Text != "b?" || u[1].Text != "c" {
		t.Fatalf("%+v", u)
	}
	if again := o.MarkSeen([]uint64{1}, func(OutboxLine) bool { return false }); len(again) != 0 {
		t.Fatal("idempotent")
	}
}
```

(`job(n)` is the existing test helper for a JobID; use the one the tests already use.)

In `debounce_test.go`: one worker's report gives `DueUpdate{Job, Text, Worker: "w"}`; two workers' combined report gives `Worker: ""`. Update existing `wantDue` expectations for single-worker reports to include `Worker`.

- [ ] **Step 2:** `cd core && go test ./agent/` → FAIL.

- [ ] **Step 3: Implement.**
  - `Line` gains `Job JobID` and `Worker string` (comment: what the line is about; zero values when it's about no one job/worker). `NewJobLine` sets Job.
  - `OutboxLine` gains the two `omitempty` fields; `UnmarshalJSON`'s raw struct reads them (optional); `Push` copies them; any place that turns an `OutboxLine` back into a `Line` (`voiceOpened`'s digest, reroute) copies them back.
  - `MarkSeen`:

```go
// MarkSeen (the updates bubble was looked at) marks those undelivered lines delivered, except
// the ones keep says still need you (a question waiting for its answer). Unknown or already
// delivered ids are ignored. Returns the jsonl records to append.
func (o *Outbox) MarkSeen(ids []uint64, keep func(OutboxLine) bool) []string {
	var records []string
	for k := range o.lines {
		l := &o.lines[k]
		if l.Delivered || !slices.Contains(ids, l.ID) || keep(*l) {
			continue
		}
		l.Delivered = true
		records = append(records, l.record())
	}
	return records
}
```

  - Set the job/worker at the call sites (only where it is in scope; leave the rest zero):
    - `controller.go` coordinator replies for job `id` (the `SpokenWorking/Question/Done/Failed` switch, ~1068–1090): `NewJobLine(kind, text, id)`.
    - the reply at ~878 (`deliverOpening(NewLine(LineReply, line)…)`): use the job id in scope if there is one.
    - `flushUpdates` (~1209): `l := NewJobLine(LineInfo, u.Text, u.Job); l.Worker = u.Worker`.
    - `controller_workers.go` ~465 (worker waiting at a prompt): `l := NewJobLine(LineInfo, text, w.Plan.Job); l.Worker = name`.
    - `liveness.go` ~115: `NewJobLine(LineInfo, …, j.ID)` (use the job variable's id field name).
    - Permission questions: wherever the question `Line` with a `Token` is built, set `Job` if the pending confirm knows its job.
  - `DueUpdate.Worker`: in `Due`, when `len(h.reports) == 1` set `Worker: h.reports[0].worker`.

- [ ] **Step 4:** `cd core && go test -race ./agent/` → PASS.
- [ ] **Step 5: Commit** `agent: lines remember their job and worker; Outbox.MarkSeen`.

---

### Task 4: Controller: the bubble feed, seen, presence and focus target

**Files:**
- Modify: `core/agent/controller.go`, `core/agent/opener.go`, `core/agent/controller_helpers_test.go` (`visible` filter)
- Create: `core/agent/bubble.go`, `core/agent/bubble_test.go`

**Interfaces:**
- Consumes: Task 3's `MarkSeen`, `OutboxLine.Job/Worker`.
- Produces:
  - `type UpdateItem struct { ID uint64 \`json:"id"\`; Text string \`json:"text"\`; Kind LineKind \`json:"kind"\`; At Millis \`json:"at"\`; Job JobID \`json:"job,omitempty"\`; Worker string \`json:"worker,omitempty"\` }`
  - `EfUpdates` effect kind; the list is in `Effect.Value` as `[]UpdateItem` (never nil: empty slice for "none").
  - Inputs: `UpdatesSeenInput(ids []uint64) Input` (`InUpdatesSeen`, ids in new field `Input.IDs []uint64`), `PresenceInput(away bool) Input` (`InPresence`, field `Input.Away bool`).
  - `func (c *AgentController) FocusTarget(job JobID, worker string) string` — agent name to focus.

- [ ] **Step 1:** Add `EfUpdates` to the `visible()` skip list in `controller_helpers_test.go` (it is bookkeeping like `EfLabel`), so existing `wantEffects` tests are unaffected.

- [ ] **Step 2: Failing tests** in `bubble_test.go` (reuse helpers from `opener_test.go`/`controller_helpers_test.go`: `workingOnJob1`, `stop`, `doneStop`, `voiceOpened`, `ctlWith`, `openingCtl`):

```go
package agent

import "testing"

func lastUpdates(t *testing.T, fx []Effect) ([]UpdateItem, bool) {
	t.Helper()
	var out []UpdateItem
	found := false
	for _, e := range fx {
		if e.Kind == EfUpdates {
			out, found = e.Value.([]UpdateItem), true
		}
	}
	return out, found
}

func TestAnOutboxedLineIsSentToTheBubbleWithItsJob(t *testing.T) {
	c := workingOnJob1()
	items, ok := lastUpdates(t, stop(c, doneStop, T0))
	if !ok || len(items) != 1 || items[0].Text != "There are 12 open issues." || items[0].Job != job(1) {
		t.Fatalf("%+v", items)
	}
}

func TestAnUnchangedListIsNotSentAgain(t *testing.T) {
	c := workingOnJob1()
	stop(c, doneStop, T0)
	if _, ok := lastUpdates(t, c.Step(TickInput(), T0+1000)); ok {
		t.Fatal("re-sent")
	}
}

func TestOpeningAConversationEmptiesTheBubble(t *testing.T) {
	c := workingOnJob1()
	stop(c, doneStop, T0)
	items, ok := lastUpdates(t, c.Step(voiceOpened(), T0+1000))
	if !ok || len(items) != 0 {
		t.Fatalf("%v %+v", ok, items)
	}
}

func TestSeenLinesLeaveTheBubbleAndAreNotReplayed(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, doneStop, T0))
	fx := c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if got, ok := lastUpdates(t, fx); !ok || len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	wantNone(t, visible(c.Step(voiceOpened(), T0+2000)), "seen = heard")
}

func TestAQuestionStillWaitingStaysAfterBeingSeen(t *testing.T) {
	c := workingOnJob1()
	q := `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`
	items, _ := lastUpdates(t, stop(c, q, T0))
	fx := c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if _, ok := lastUpdates(t, fx); ok {
		t.Fatal("the question should still be listed (list unchanged)")
	}
}

func TestNoFeedWhenBitOpensConversationsForUpdates(t *testing.T) {
	c := openingCtl()
	if _, ok := lastUpdates(t, stop(c, doneStop, T0)); ok {
		t.Fatal("fed the bubble with openConversationForUpdates on")
	}
}

func TestAwayHoldsTheOpenAndTheChimeUntilBack(t *testing.T) {
	c := openingCtl()
	c.Step(PresenceInput(true), T0)
	fx := stop(c, doneStop, T0+10)
	if opens(fx) != 0 || anyKind(fx, EfChime) {
		t.Fatal(fmtEffects(fx))
	}
	if !anyKind(fx, EfNotify) {
		t.Fatal("the notification still goes to Notification Center")
	}
	if opens(c.Step(PresenceInput(false), T0+60_000)) != 1 {
		t.Fatal("did not open on return")
	}
}

func TestFocusTargetPrefersTheLiveWorkerThenTheCoordinator(t *testing.T) {
	c := workingOnJob1()
	if got := c.FocusTarget(job(1), "nobody"); got != Coordinator {
		t.Fatal(got)
	}
	if got := c.FocusTarget("", ""); got != Coordinator {
		t.Fatal(got)
	}
	// With a live worker for job 1 (admit + up it with the helpers controller_worker_test.go uses):
	// FocusTarget(job(1), "") == that worker's name, and FocusTarget(job(1), name) == name.
}
```

Finish the last test with a live worker using the existing worker-admission helpers in `controller_worker_test.go`. Check the status strings `stop` accepts against `spoken_test.go` and adjust the question example if needed.

- [ ] **Step 3:** `cd core && go test ./agent/ -run 'Bubble|Seen|Feed|Away|Focus|Unchanged|Opening'` → FAIL.

- [ ] **Step 4: Implement** in `bubble.go`:

```go
package agent

import "slices"

// The updates bubble (spec docs/specs/2026-10-14-updates-bubble-design.md): the outbox's
// undelivered lines, shown beside Bit while Bit isn't opening conversations for them.

// UpdateItem is one line in the bubble.
type UpdateItem struct { /* fields as in Interfaces */ }

// bubbleItems: the undelivered lines, minus permission questions no longer pending.
func (c *AgentController) bubbleItems() []UpdateItem {
	items := []UpdateItem{}
	for _, l := range c.outbox.Undelivered() {
		if l.Token != nil && !c.confirms.IsPendingToken(*l.Token) {
			continue
		}
		items = append(items, UpdateItem{ID: l.ID, Text: l.Text, Kind: l.Kind, At: l.At, Job: l.Job, Worker: l.Worker})
	}
	return items
}

// syncUpdates sends the bubble its list when it changed (every Step ends here).
func (c *AgentController) syncUpdates(fx *[]Effect) {
	if c.settings.OpenConversation || c.shutDown {
		return
	}
	items := c.bubbleItems()
	if c.bubbleSent && slices.Equal(items, c.bubbleLast) {
		return
	}
	c.bubbleSent, c.bubbleLast = true, items
	*fx = append(*fx, Effect{Kind: EfUpdates, Value: items})
}

// updatesSeen: the bubble was looked at: those lines count as heard, except a question still
// waiting for its answer.
func (c *AgentController) updatesSeen(ids []uint64, fx *[]Effect) {
	keep := func(l OutboxLine) bool {
		if l.Kind != LineQuestion {
			return false
		}
		if l.Token != nil {
			return c.confirms.IsPendingToken(*l.Token)
		}
		j := c.jobs.Get(l.Job)
		return j != nil && j.Status == JobQuestion
	}
	for _, r := range c.outbox.MarkSeen(ids, keep) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
}

// FocusTarget is the pane a click on a bubble line focuses: the line's worker if it is still
// live, else the job's first live worker, else the coordinator.
func (c *AgentController) FocusTarget(job JobID, worker string) string {
	if worker != "" && c.workers.Get(worker) != nil {
		return worker
	}
	if job != "" {
		if names := c.workers.ForJob(job); len(names) > 0 {
			return names[0]
		}
	}
	return Coordinator
}
```

(Use the real accessor names for jobs — `c.jobs.Get`/`Status` — as they exist in `jobs.go`. Make `UpdateItem` comparable so `slices.Equal` works.)

In `controller.go`: fields `bubbleSent bool; bubbleLast []UpdateItem; away bool`; `EfUpdates` at the end of the EffectKind list with comment `// Value: []UpdateItem: the updates bubble's list (sent when it changes)`; `InUpdatesSeen` and `InPresence` at the end of the InputKind list; `Input.IDs []uint64`, `Input.Away bool`; constructors `UpdatesSeenInput`, `PresenceInput`. In `Step`: `case InUpdatesSeen: c.updatesSeen(in.IDs, &fx)` and `case InPresence: c.setAway(in.Away, now, &fx)`; after `c.syncCoordTab(&fx)` add `c.syncUpdates(&fx)`.

Presence in `opener.go`:

```go
// setAway: while you're away Bit doesn't open a conversation or chime; on return it tries again.
func (c *AgentController) setAway(away bool, now Millis, fx *[]Effect) {
	c.away = away
	if !away {
		c.tryOpen(now, fx)
	}
}
```

and in `tryOpen`, right after the `waiting` expiry block: `if c.away { return }` (keeps `retry` set). Chime: in `applyDelivery` (controller.go ~1397 region, the `DeliverChime` case) skip the `EfChime` effect when `c.away`.

- [ ] **Step 5:** `cd core && go test -race ./agent/` → PASS (all, including the existing suites).
- [ ] **Step 6: Commit** `agent: feed the updates bubble; seen, presence and focus target`.

---

### Task 5: Runtime and app glue: `updates` event, three calls

**Files:**
- Modify: `core/agent/runtime.go` (Deps, apply, AgentHandle methods), `core/app/agent.go` (AgentHooks, Agent interface), `core/app/agentapp/agentapp.go`, `core/app/app.go` (hook wiring + Register)
- Test: `core/agent/runtime_test.go`, `core/app/app_test.go`

**Interfaces:**
- Consumes: Task 4's `EfUpdates`, `UpdatesSeenInput`, `PresenceInput`, `FocusTarget`.
- Produces:
  - `agent.UpdatesSink interface{ Updates([]UpdateItem) }` in `Deps.Updates` (nil = ignored).
  - `(*AgentHandle) UpdatesSeen(ids []uint64)`, `SetAway(away bool)`, `FocusJob(ctx, job JobID, worker string) error`.
  - `app.AgentHooks.Updates func(items any)`; `app.Agent` gains `UpdatesSeen(ids []uint64)`, `SetAway(bool)`, `FocusJob(job, worker string)`.
  - Engine event `{"kind":"updates","lines":[UpdateItem…]}`; engine calls `agent.updatesSeen {ids}`, `agent.focusJob {job, worker}`, `presence.set {away}`.

- [ ] **Step 1: Failing tests.** In `runtime_test.go` (existing fake-deps style): a fake `UpdatesSink` receives the list when the runtime applies an `EfUpdates`; `FocusJob` with a fake Focuser host focuses `FocusTarget`'s name and opens the launcher (same as `OpenTerminal`). In `app_test.go` (existing fake-agent style): `agent.updatesSeen` with `{"ids":[1,2]}` reaches the fake agent; `presence.set {"away":true}` reaches `SetAway(true)`; the `Updates` hook emits an event whose JSON is `{"kind":"updates","lines":[…]}`; with no agent (agent disabled) the three calls return `{}` without error.

- [ ] **Step 2:** `cd core && go test ./agent/ ./app/...` → FAIL.

- [ ] **Step 3: Implement.**
  - `runtime.go`: `Deps.Updates UpdatesSink`; in `apply`: `case EfUpdates: if d.Updates != nil { d.Updates.Updates(e.Value.([]UpdateItem)) }`.

```go
// UpdatesSeen: the bubble showed these lines long enough to count as heard.
func (h *AgentHandle) UpdatesSeen(ids []uint64) { h.Input(UpdatesSeenInput(ids)) }

// SetAway: the shell's presence (screen locked, displays asleep or idle).
func (h *AgentHandle) SetAway(away bool) { h.Input(PresenceInput(away)) }

// FocusJob is a click on a bubble line: bring that job's pane to the front in a terminal.
func (h *AgentHandle) FocusJob(ctx context.Context, job JobID, worker string) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.ctlMu.Lock()
	target := h.ctl.FocusTarget(job, worker)
	h.ctlMu.Unlock()
	focusQuietly(ctx, h.deps.Host.Session(), target)
	return h.deps.Launcher.Open(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
}
```

  - `agentapp.go`: `Updates: updatesSink{c.Hooks}` in Deps, with `type updatesSink struct{ hooks app.AgentHooks }` whose `Updates` calls `hooks.Updates(items)` if non-nil; `(*Agent) UpdatesSeen/SetAway` forward to the handle; `(*Agent) FocusJob(job, worker string)` runs `a.handle.FocusJob(a.ctx, agent.JobID(job), worker)` in a goroutine (like `OpenTerminal`) and logs an error.
  - `app/agent.go`: add `Updates func(items any)` to `AgentHooks` (comment: the updates bubble's list) and the three methods to `Agent`. Update fakes in tests that implement `Agent`.
  - `app.go`: in the hooks literal `Updates: func(items any) { a.emit("updates", map[string]any{"lines": items}) },`. Register:

```go
	r.Register("agent.updatesSeen", func(args json.RawMessage) (any, error) {
		var in struct{ IDs []uint64 `json:"ids"` }
		if err := decodeArgs("agent.updatesSeen", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.UpdatesSeen(in.IDs)
		}
		return struct{}{}, nil
	})
	r.Register("agent.focusJob", func(args json.RawMessage) (any, error) {
		var in struct{ Job, Worker string }
		if err := decodeArgs("agent.focusJob", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.FocusJob(in.Job, in.Worker)
		}
		return struct{}{}, nil
	})
	r.Register("presence.set", func(args json.RawMessage) (any, error) {
		var in struct{ Away bool `json:"away"` }
		if err := decodeArgs("presence.set", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.SetAway(in.Away)
		}
		return struct{}{}, nil
	})
```

(Match how the struct tags are written next to it: `json:"job"`/`json:"worker"`.)

- [ ] **Step 4:** `cd core && go vet ./... && go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `app: updates event; agent.updatesSeen, agent.focusJob, presence.set`.

---

### Task 6: Swift core package: event, bubble model, placement, presence rule

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift`
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Mascot/UpdatesBubbleModel.swift`, `.../Mascot/BubblePlacement.swift`, `.../Platform/PresenceRule.swift`, `.../Settings/BubbleSettingsValues.swift`
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/EngineEventTests.swift`, create `UpdatesBubbleModelTests.swift`, `BubblePlacementTests.swift`, `PresenceRuleTests.swift`, `BubbleSettingsValuesTests.swift`

**Interfaces:**
- Consumes: the `updates` event JSON (Task 5); settings snapshot (Task 1).
- Produces:

```swift
public struct UpdateLine: Decodable, Sendable, Equatable, Identifiable {
    public let id: UInt64
    public let text: String
    public let kind: String      // "reply" | "question" | "unsolicited" | "info"
    public let at: Int64         // ms since epoch
    public let job: String?
    public let worker: String?
}
// EngineEvent: case updates([UpdateLine])

public enum PopPermission: Sendable { case allowed, badgeOnly, hold }   // hold = away

public struct UpdatesBubbleModel: Sendable {
    public enum Phase: Equatable, Sendable { case hidden, popped(until: TimeInterval), badge, expanded(since: TimeInterval) }
    public init(popSecs: TimeInterval = 6, maxLines: Int = 5)
    public var popSecs: TimeInterval; public var maxLines: Int
    public private(set) var phase: Phase
    public private(set) var lines: [UpdateLine]        // the core's list, oldest first
    public var count: Int                              // lines.count
    public var shown: [UpdateLine]                     // newest first, at most maxLines (frozen while expanded, plus additions)
    public var more: Int                               // display count beyond maxLines
    public mutating func apply(_ new: [UpdateLine], now: TimeInterval, pop: PopPermission)
    public mutating func pointerEntered(now: TimeInterval)
    public mutating func pointerLeft()
    public mutating func tick(now: TimeInterval) -> [UInt64]   // ids to mark seen (once per expansion)
    public mutating func back(now: TimeInterval, pop: PopPermission)   // presence returned
}

public enum BubblePlacement {
    public enum Side: Sendable { case left, right }
    /// Desktop-space rects (top-left origin). Prefers the right of Bit; flips left when it
    /// wouldn't fit; clamps vertically into `screen`. Bubble top aligns with Bit's top.
    public static func frame(mascot: CGRect, bubble: CGSize, screen: CGRect, gap: CGFloat = 8) -> (CGRect, Side)
}

public enum PresenceRule {
    public static func isAway(locked: Bool, displaysAsleep: Bool, idleSecs: TimeInterval, awayAfterMins: Int) -> Bool
}

public struct BubbleSettingsValues: Equatable, Sendable {
    public var enabled: Bool; public var popSecs: Int; public var lines: Int; public var awayAfterMins: Int
    public static let defaults = BubbleSettingsValues(enabled: true, popSecs: 6, lines: 5, awayAfterMins: 5)
    public static func fromSnapshot(_ data: Data) -> BubbleSettingsValues?   // mirror MascotSettings.fromSnapshot
}
```

- [ ] **Step 1: Failing tests.**

`EngineEventTests.swift`:

```swift
func testUpdatesEventDecodes() {
    let json = #"{"kind":"updates","lines":[{"id":3,"text":"Done.","kind":"reply","at":1700000000000,"job":"job_000000000001"},{"id":4,"text":"hi","kind":"info","at":1700000000001}]}"#
    guard case .updates(let lines)? = EngineEvent.decode(Data(json.utf8)) else { return XCTFail() }
    XCTAssertEqual(lines.map(\.id), [3, 4])
    XCTAssertEqual(lines[0].job, "job_000000000001")
    XCTAssertNil(lines[1].worker)
}
func testEmptyUpdatesDecodes() {
    guard case .updates(let l)? = EngineEvent.decode(Data(#"{"kind":"updates","lines":[]}"#.utf8)) else { return XCTFail() }
    XCTAssertTrue(l.isEmpty)
}
```

`UpdatesBubbleModelTests.swift` (helper `func line(_ id: UInt64) -> UpdateLine`):

```swift
func testFirstListAfterLaunchIsBadgeOnly() {
    var m = UpdatesBubbleModel()
    m.apply([line(1)], now: 0, pop: .allowed)
    XCTAssertEqual(m.phase, .badge)
}
func testNewLinePopsThenCollapsesToBadge() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1)], now: 10, pop: .allowed)
    XCTAssertEqual(m.phase, .popped(until: 16))
    XCTAssertEqual(m.tick(now: 15.9), []); XCTAssertEqual(m.phase, .popped(until: 16))
    _ = m.tick(now: 16); XCTAssertEqual(m.phase, .badge)
}
func testAnotherLineWhilePoppedRestartsTheTimer() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1)], now: 10, pop: .allowed)
    m.apply([line(1), line(2)], now: 14, pop: .allowed)
    XCTAssertEqual(m.phase, .popped(until: 20))
    XCTAssertEqual(m.shown.map(\.id), [2, 1])
}
func testHoverExpandsAndOneSecondMarksSeenOnce() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1), line(2)], now: 1, pop: .allowed)
    m.pointerEntered(now: 2)
    XCTAssertEqual(m.tick(now: 2.9), [])
    XCTAssertEqual(Set(m.tick(now: 3)), [1, 2])
    XCTAssertEqual(m.tick(now: 4), [])
}
func testExpandedListStaysUntilThePointerLeaves() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1), line(2)], now: 1, pop: .allowed)
    m.pointerEntered(now: 2); _ = m.tick(now: 3)
    m.apply([], now: 3.1, pop: .allowed)            // the core cleared what was seen
    XCTAssertEqual(m.shown.map(\.id), [2, 1])
    m.pointerLeft()
    XCTAssertEqual(m.phase, .hidden)
}
func testPendingQuestionKeepsTheBadgeAfterLeaving() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1), line(2)], now: 1, pop: .allowed)
    m.pointerEntered(now: 2); _ = m.tick(now: 3)
    m.apply([line(2)], now: 3.1, pop: .allowed)
    m.pointerLeft()
    XCTAssertEqual(m.phase, .badge); XCTAssertEqual(m.count, 1)
}
func testAwayHoldsThePopAndReturnPopsOnce() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1)], now: 1, pop: .hold)
    m.apply([line(1), line(2)], now: 2, pop: .hold)
    XCTAssertEqual(m.phase, .badge)
    m.back(now: 100, pop: .allowed)
    XCTAssertEqual(m.phase, .popped(until: 106))
    _ = m.tick(now: 106)
    m.back(now: 200, pop: .allowed)
    XCTAssertEqual(m.phase, .badge)                 // nothing new: no second pop
}
func testBadgeOnlyNeverPops() {
    var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
    m.apply([line(1)], now: 1, pop: .badgeOnly)
    XCTAssertEqual(m.phase, .badge)
}
func testEmptyListHides() {
    var m = UpdatesBubbleModel(); m.apply([line(1)], now: 0, pop: .allowed)
    m.apply([], now: 1, pop: .allowed)
    XCTAssertEqual(m.phase, .hidden)
}
func testMoreCountsBeyondMaxLines() {
    var m = UpdatesBubbleModel(popSecs: 6, maxLines: 2)
    m.apply((1...5).map(line), now: 0, pop: .allowed)
    XCTAssertEqual(m.shown.map(\.id), [5, 4]); XCTAssertEqual(m.more, 3)
}
```

`BubblePlacementTests.swift`: Bit at `(100,100,64,64)`, bubble `240×120`, screen `(0,0,1440,900)` → right side, x = 172, y = 100. Bit at `(1360,100,64,64)` → left side, x = 1360-8-240 = 1112. Bit at `(100,840,64,64)` → y clamped so maxY ≤ 900 (y = 780).

`PresenceRuleTests.swift`: locked → away; asleep → away; idle 299 s with 5 min → present; idle 300 s → away.

`BubbleSettingsValuesTests.swift`: a snapshot JSON fixture with `notifications` values parses; a snapshot without them gives `.defaults`. Copy the snapshot shape `MascotSettings.fromSnapshot` parses (look at its test).

- [ ] **Step 2:** `cd mac/Packages/DeskPixieCore && swift test` → FAIL (types missing).

- [ ] **Step 3: Implement.** `EngineEvent`: `case updates([UpdateLine])`, private `struct Updates: Decodable { var lines: [UpdateLine] }`, decode `"updates"`. Model rules (all tested above):
  - `apply`: `added = new ids not in old lines`. Store `lines = new`. If `lines` is empty and not expanded → `.hidden`, clear `heldPop`. If this is the first `apply` ever → `.badge` (or `.hidden` if empty); no pop. If `added` non-empty: `.allowed` → `.popped(until: now + popSecs)` unless expanded (stay expanded, prepend additions to the frozen list); `.hold` → `heldPop = true`, `.badge` unless expanded; `.badgeOnly` → `.badge` unless expanded/popped. Otherwise keep the phase (but `.hidden` with non-empty lines → `.badge`).
  - `pointerEntered`: if `!lines.isEmpty || phase is popped` → `.expanded(since: now)`, freeze `shown`, reset `seenSent`.
  - `pointerLeft`: unfreeze; `.hidden` if `lines.isEmpty`, else `.badge`.
  - `tick`: popped and `now >= until` → `.badge`. Expanded, `now - since >= 1`, `!seenSent` → `seenSent = true`, return the frozen ids.
  - `back`: if `heldPop` and `!lines.isEmpty` and `pop == .allowed` → `.popped(until: now + popSecs)`; clear `heldPop`.
  - `shown`: frozen list while expanded, else `lines.reversed().prefix(maxLines)`; `more = max(0, total − maxLines)`.
  `BubblePlacement`, `PresenceRule`, `BubbleSettingsValues` as specified.

- [ ] **Step 4:** `swift test` → PASS.
- [ ] **Step 5: Commit** `mac core: updates event, bubble model, placement, presence rule`.

---

### Task 7: The bubble window, presence monitor and wiring (app target)

**Files:**
- Create: `mac/DeskPixie/Mascot/UpdatesBubbleController.swift`, `mac/DeskPixie/Mascot/UpdatesBubbleView.swift`, `mac/DeskPixie/Engine/PresenceMonitor.swift`
- Modify: `mac/DeskPixie/Mascot/MascotController.swift` (expose frame/opacity changes), `mac/DeskPixie/Engine/EngineCoordinator.swift` (event + settings + wiring), `docs/decisions.md`
- Check `mac/project.yml`: new files under existing source folders are picked up by xcodegen; regenerate if the build says otherwise.

**Interfaces:**
- Consumes: Task 6 types; engine calls from Task 5.
- Produces: nothing other tasks use.

- [ ] **Step 1: `MascotController` hooks.** Add `var onFrameChange: ((NSRect) -> Void)?` (called from `panelMoved()` and `place(_:)`) and `var onOpacityChange: ((CGFloat) -> Void)?` (called where `view.setOpacity` is called in `pollCursor`). Add `var isShown: Bool { panel.isVisible }`.

- [ ] **Step 2: `UpdatesBubbleView` (SwiftUI).** Inputs: `count: Int`, `phase`, `shown: [UpdateLine]`, `more: Int`, `onLine: (UpdateLine) -> Void`, `onMore: () -> Void`. Badge: small capsule with the count (`9+` above 9). Bubble: rounded rect (12 pt radius, `.regularMaterial` background, 240 pt wide), rows: text (`.lineLimit(2)`, `.font(.callout)`) and a relative time (`Text(Date(timeIntervalSince1970: Double(at)/1000), style: .relative)` + " ago", `.caption`, secondary); question rows get a `questionmark.circle` symbol; last row "+N more" when `more > 0`. Rows are buttons calling `onLine`; "+N more" calls `onMore`.

- [ ] **Step 3: `UpdatesBubbleController`.** Owns an `NSPanel` configured like `MascotPanel` (borderless, nonactivating, clear, floating, all Spaces) hosting `NSHostingView(rootView:)`; added with `mascot.panel.addChildWindow(panel, ordered: .above)`. State: `var model = UpdatesBubbleModel()`, `var settings = BubbleSettingsValues.defaults`, `var present = true`. A 0.25 s timer calls `model.tick(now:)`; non-empty ids → `engine.call("agent.updatesSeen", ["ids": ids])` off main (use the same `worker.run` pattern as `EngineCoordinator`). Hover via an `NSTrackingArea` on the hosting view (`mouseEntered` → `model.pointerEntered`, `mouseExited` → `model.pointerLeft`). Clicking the badge also calls `pointerEntered`. `onLine` → `engine.call("agent.focusJob", ["job": line.job ?? "", "worker": line.worker ?? ""])`; `onMore` → `engine.call("app.action", ["id": "talk"])` (use the id the tray's "Talk" item uses; check `app.go` `case "talk"`). After every change: re-render, size the panel to the SwiftUI fitting size, place it with `BubblePlacement.frame` (convert with `DesktopSpace`/`ScreenDisplays` the way `MascotController.pollCursor` does), hide the panel for `.hidden` or when `!settings.enabled` or `!mascot.isShown`; set `panel.alphaValue` from `onOpacityChange`. `pop` permission: `.hold` if `!present`; `.badgeOnly` if `!NSMenu.menuBarVisible()` (a fullscreen app is in front); else `.allowed`. API: `apply(lines:)`, `apply(settings:)`, `presence(present:)` (on becoming present: `model.back(now:pop:)`).

- [ ] **Step 4: `PresenceMonitor`.** `@MainActor final class PresenceMonitor` with `var onChange: ((Bool) -> Void)?` (true = away) and `var awayAfterMins: Int`. Observes `DistributedNotificationCenter.default()` `com.apple.screenIsLocked` / `com.apple.screenIsUnlocked`, `NSWorkspace.shared.notificationCenter` `screensDidSleepNotification` / `screensDidWakeNotification`; a 5 s timer reads `CGEventSource.secondsSinceLastEventType(.combinedSessionState, eventType: CGEventType(rawValue: ~0)!)`. Computes `PresenceRule.isAway(...)`, calls `onChange` only on change.

- [ ] **Step 5: Wire in `EngineCoordinator`.** Create the bubble controller after the mascot loads (`finishStart`, next to `mascot = m`) and the presence monitor; presence `onChange` → `bubble.presence(present: !away)` and `engine.call("presence.set", ["away": away])` off main. `handle`: `case .updates(let lines): bubble?.apply(lines: lines)`. In `applyMascotSettings` (also run on `.settingsChanged`) parse `BubbleSettingsValues.fromSnapshot` from the same snapshot and apply it to the bubble and `presence.awayAfterMins`. Stop timers in the coordinator's stop path next to `mascot?.stop()`.

- [ ] **Step 6: Decision log.** Append to `docs/decisions.md`:

```markdown
## 2026-10-14 — Updates bubble: no Focus / Do Not Disturb check
**Decision:** The bubble skips its pop when a fullscreen app is in front (menu bar hidden) but does not detect Focus / Do Not Disturb.
**Trigger:** Spec §4 asks for badge-only during Focus; the only API, INFocusStatusCenter, needs the Communication Notifications entitlement and a permission prompt.
**Basis:** Planning ruling; revisit if Focus suppression is wanted enough to add the entitlement.
```

- [ ] **Step 7: Build and run checks.** `make -C mac` (or the Debug build command the repo uses) builds; `cd mac/Packages/DeskPixieCore && swift test` and `cd core && go test -race ./...` pass.

- [ ] **Step 8: Manual check (record results in the PR):** with `agent.openConversationForUpdates` off, start a job with a worker, don't talk to Bit: when the report lands the bubble pops beside Bit, collapses to a badge after 6 s; hover → expands; after 1 s the badge clears (questions stay); click a line → herdr comes forward on that worker's pane. Drag Bit to the right screen edge → bubble flips left. Lock the screen, trigger an update, unlock → exactly one pop, and no chime while locked. Change `notifications.bubble.popSecs` in Settings → next pop uses it.

- [ ] **Step 9: Commit** `mac: updates bubble window, presence monitor and wiring`.
