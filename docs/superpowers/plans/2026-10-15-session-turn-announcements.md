# Session Turn Announcements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a session you started with `pix` finishes a turn, Pixie tells you it is done, with a one-sentence gist, behind a setting (#114).

**Architecture:** `pix` sends a new `turn` wire event on every Stop (final reply, duration, who started it). `pixhost` turns it into a new `PaneEventTurn`. The controller filters it by the two settings, makes a gist with a pure text-rules function and routes a new `LineSession` line through the existing update path (spoken / chime + notification / bubble / outbox). The bubble line carries the session app's bundle ID, so a click activates that app like a roster row does.

**Tech Stack:** Go (`core/`), Swift (`mac/`, package `mac/Packages/DeskPixieCore`).

**Spec:** `docs/superpowers/specs/2026-10-15-session-turn-announcements-design.md`

## Global Constraints

- Work only in the worktree `.worktrees/session-turns` (branch `session-turns`). Never touch the main checkout.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw
  ```
- Verification is local (CI doesn't run): `go -C core test -race ./...`; `cd mac/Packages/DeskPixieCore && swift test`; `make -C mac build`.
- Settings: `notifications.sessionTurns` (bool, default `false`, label "Announce finished sessions", help "Pixie tells you when a session you started with pix finishes a turn, even one Pixie didn't start."); `notifications.sessionTurnsMinSeconds` (int 0–3600, default `30`, label "Only turns longer than (seconds)", help "Shorter turns stay quiet; you were probably watching. Turns Pixie started are always announced."). Both in `SectionNotifications`, both `ApplyRestart`.
- Lines: done `<label> is done: <gist>`; asking `<label> is asking: <question>`; no text `<label> finished.`
- Caps: reply in the wire event 4 KB (UTF-8 boundary); `Detail` for the voice model 2 KB; gist 120 characters, word boundary, ending `…`.
- Voice prefix for the new kind: `"Session update: "`. Spoken form with a reply: `Prefix + Text + " Its reply, to sum up in one sentence: " + Detail`.
- Confirm words pending at the time are masked in everything a session line says (label, gist, detail).
- `wire.Version` stays 1.

## Review Focus

1. **A turn you interrupt with Esc, then a new prompt.** Claude fires no Stop on interrupt, and `pix` flips to idle on the screen marker. The next turn's duration must start at the next *working*, not at the interrupted one. Pinned in Task 4 (`TestAnInterruptedTurnDoesNotCountTowardTheNext`).
2. **A session's description containing a pending confirm word.** The description is agent-settable (`set_description`), so the label must be masked as well as the reply. Pinned in Task 5 (`TestASessionTurnNeverSpeaksAPendingConfirmWord`).
3. **A reply that is only a bullet list.** It must still give a readable sentence, not run-on words. Pinned in Task 2 (gist table case "list only").
4. **Lowercasing the first letter of the gist.** "I traced…" and "API keys…" must stay as they are. Pinned in Task 2 (`TestSessionTurnLine`).
5. **A session with no description and no git root.** The label falls back to the folder name, then to the tool. Pinned in Task 3 (`TestATurnLabelFallsBackToTheFolder`).

---

## File map

| File | Change |
|---|---|
| `core/settings/config.go`, `schema.go`, `config_test.go`, `schema_test.go` | Two notification settings |
| `core/agent/controller.go` | `Settings` fields, `PaneEventTurn` case, `onSessionTurn` |
| `core/app/agentapp/agentapp.go` | Fill the two controller settings |
| `core/agent/sessiongist.go` (new) + `_test.go` | `SessionGist`, `SessionTurnLine` |
| `core/agent/types.go` | `PaneEventTurn`, `PaneEvent.Turn`, `SessionTurn` |
| `core/pix/wire/wire.go` + `_test.go` | `DurationMs` |
| `core/pixhost/host.go` + `host_test.go` | `"turn"` event → `PaneEventTurn`; `session.label()` |
| `core/pix/session.go` + `session_test.go` | Turn timing, `endTurn`, `capUTF8` |
| `core/agent/routing.go`, `outbox.go`, `bubble.go` | `LineSession`, `Line.Asking/Detail/App`, `OutboxLine.App`, `UpdateItem.App` |
| `core/agent/session_turn_test.go` (new) | Controller tests |
| `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/UpdatesBubbleModel.swift` + tests | `UpdateLine.app` |
| `mac/DeskPixie/Pixie/AppActivation.swift` (new), `UpdatesBubbleController.swift`, `AgentRosterController.swift` | Click on a session line activates its app |

---

### Task 1: Settings

**Files:**
- Modify: `core/settings/config.go` (`NotificationSettings`, `Default()`)
- Modify: `core/settings/schema.go` (after the `notifications.awayAfterMins` row, ~line 402)
- Modify: `core/agent/controller.go` (`Settings` struct ~line 55-80, `DefaultSettings()` ~line 82)
- Modify: `core/app/agentapp/agentapp.go` (~line 361, the `agent.Settings{…}` literal)
- Test: `core/settings/config_test.go`, `core/settings/schema_test.go`

**Interfaces:**
- Produces: `settings.NotificationSettings.SessionTurns bool`, `.SessionTurnsMinSeconds uint64`; `agent.Settings.SessionTurns bool`, `agent.Settings.SessionTurnsMinMs agent.Millis` (default `30_000`).

- [ ] **Step 1: Write the failing tests**

In `core/settings/config_test.go`, change `TestNotificationsBlockUsesCamelCase`'s `want` to:

```go
	want := map[string]any{"debounceSecs": float64(20), "awayAfterMins": float64(5),
		"sessionTurns": false, "sessionTurnsMinSeconds": float64(30),
		"bubble": map[string]any{"enabled": true, "popSecs": float64(6), "lines": float64(5)}}
```

In `core/settings/schema_test.go`, add:

```go
// #114: announcing finished pix sessions is off by default and read at startup.
func TestSessionTurnSettings(t *testing.T) {
	on := FieldByPath("notifications.sessionTurns")
	if on == nil || on.Section != SectionNotifications || on.Kind.Type != KindBool || on.Apply != ApplyRestart ||
		on.Label != "Announce finished sessions" {
		t.Fatalf("%+v", on)
	}
	min := FieldByPath("notifications.sessionTurnsMinSeconds")
	if min == nil || min.Section != SectionNotifications || min.Apply != ApplyRestart ||
		min.Label != "Only turns longer than (seconds)" {
		t.Fatalf("%+v", min)
	}
	if d := Default().Notifications; d.SessionTurns || d.SessionTurnsMinSeconds != 30 {
		t.Fatalf("%+v", d)
	}
}
```

In `core/agent/controller_test.go`, add:

```go
func TestSessionTurnsAreOffByDefaultWithAThirtySecondMinimum(t *testing.T) {
	if s := DefaultSettings(); s.SessionTurns || s.SessionTurnsMinMs != 30_000 {
		t.Fatalf("%+v", s)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./settings/ ./agent/ -run 'TestNotificationsBlockUsesCamelCase|TestSessionTurnSettings|TestSessionTurnsAreOffByDefault'`
Expected: FAIL (compile errors: unknown fields `SessionTurns`, `SessionTurnsMinSeconds`, `SessionTurnsMinMs`).

- [ ] **Step 3: Implement**

`core/settings/config.go`, in `NotificationSettings` after `AwayAfterMins`:

```go
	// SessionTurns: Pixie tells you when a session you started with pix finishes a turn, even
	// one it didn't start (#114). SessionTurnsMinSeconds: a turn of yours shorter than this
	// stays quiet (one Pixie started is always told).
	SessionTurns           bool   `json:"sessionTurns"`
	SessionTurnsMinSeconds uint64 `json:"sessionTurnsMinSeconds"`
```

and in `Default()` the notifications literal becomes:

```go
		Notifications: NotificationSettings{DebounceSecs: 20, Bubble: BubbleSettings{Enabled: true, PopSecs: 6, Lines: 5}, AwayAfterMins: 5, SessionTurnsMinSeconds: 30},
```

`core/settings/schema.go`, after the `notifications.awayAfterMins` row:

```go
	spec("notifications.sessionTurns", SectionNotifications, "Announce finished sessions", "Pixie tells you when a session you started with pix finishes a turn, even one Pixie didn't start.", boolKind, ApplyRestart),
	spec("notifications.sessionTurnsMinSeconds", SectionNotifications, "Only turns longer than (seconds)", "Shorter turns stay quiet; you were probably watching. Turns Pixie started are always announced.", intKind(0, 3600, false), ApplyRestart),
```

`core/agent/controller.go`, in `Settings` after `SpeakWorkerReports`:

```go
	// SessionTurns (`notifications.sessionTurns`, off by default): a session you started with
	// pix that finishes a turn is announced (#114). SessionTurnsMinMs: a turn of yours shorter
	// than this stays quiet; one Pixie started is always announced.
	SessionTurns      bool
	SessionTurnsMinMs Millis
```

and `DefaultSettings()` gains `SessionTurnsMinMs: 30_000` in its literal.

`core/app/agentapp/agentapp.go`, in the `agent.Settings{…}` literal after `SpeakWorkerReports`:

```go
			SessionTurns:        c.Notifications.SessionTurns,
			SessionTurnsMinMs:   agent.Millis(c.Notifications.SessionTurnsMinSeconds) * 1000,
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test ./settings/ ./agent/ ./app/...`
Expected: PASS. If `TestTheLiveListIsPinned` or any other test that lists settings fails, a new key was marked live by mistake: both must be `ApplyRestart`.

- [ ] **Step 5: Commit**

```bash
git add core/settings core/agent/controller.go core/agent/controller_test.go core/app/agentapp/agentapp.go
git commit -m "settings: announce finished pix sessions, with a minimum turn length (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 2: The gist and the line

**Files:**
- Create: `core/agent/sessiongist.go`
- Test: `core/agent/sessiongist_test.go`

**Interfaces:**
- Produces: `func SessionGist(reply string) (text string, asking bool)`; `func SessionTurnLine(label, gist string, asking bool) string`; `const MaxGistRunes = 120`.

Rulings already made against the spec (record them in the ledger):
- Spec §4 step 4 says a too-short first sentence falls back to "the paragraph before". The plan first tries the **next sentence of the same paragraph** ("Done. All 412 tests pass." → "All 412 tests pass."), then the paragraph before. Same intent, covers the single-paragraph case.
- Spec §4 step 1 says remove `*`/`_` emphasis. The plan removes `**`, `__`, `*` and backticks but keeps single `_`, so `snake_case` names survive.
- List items get a closing `.` when they lack one, so a list-only reply splits into sentences.

- [ ] **Step 1: Write the failing tests**

`core/agent/sessiongist_test.go`:

```go
package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionGist(t *testing.T) {
	long := strings.Repeat("everything ", 20) + "works."
	for _, tc := range []struct {
		name, reply, want string
		asking            bool
	}{
		{"plain done", "The redirect loop is fixed and all 412 tests pass.", "The redirect loop is fixed and all 412 tests pass.", false},
		{"outcome last", "I traced the loop to the cookie path.\n\nThe redirect loop is fixed and all 412 tests pass.", "The redirect loop is fixed and all 412 tests pass.", false},
		{"question last", "I looked at it.\n\nShould I also update the docs?", "Should I also update the docs?", true},
		{"question after a sentence", "Fixed the loop. Want me to update the docs too?", "Want me to update the docs too?", true},
		{"short then paragraph before", "Changed the cookie path to slash.\n\nDone.", "Changed the cookie path to slash.", false},
		{"short then next sentence", "Done. All 412 tests pass.", "All 412 tests pass.", false},
		{"code only", "```go\nfunc main() {}\n```", "", false},
		{"list only", "## Summary\n\n- **Fixed** the `login` redirect\n- Added a test", "Fixed the login redirect.", false},
		{"table then words", "| a | b |\n|---|---|\n| 1 | 2 |\n\nAll green now.", "All green now.", false},
		{"snake_case kept", "Renamed load_config to read_config everywhere.", "Renamed load_config to read_config everywhere.", false},
		{"empty", "  \n\n ", "", false},
	} {
		got, asking := SessionGist(tc.reply)
		if got != tc.want || asking != tc.asking {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got, asking, tc.want, tc.asking)
		}
	}
	got, _ := SessionGist(long)
	if utf8.RuneCountInString(got) > MaxGistRunes || !strings.HasSuffix(got, "…") || strings.Contains(got, "everythi…") {
		t.Errorf("capped on a word boundary: %q", got)
	}
}

func TestSessionTurnLine(t *testing.T) {
	for _, tc := range []struct {
		label, gist string
		asking      bool
		want        string
	}{
		{"deskpixie: fix login redirect", "The redirect loop is fixed and all 412 tests pass.", false,
			"deskpixie: fix login redirect is done: the redirect loop is fixed and all 412 tests pass."},
		{"api", "Should I also update the docs?", true, "api is asking: should I also update the docs?"},
		{"api", "", false, "api finished."},
		{"api", "API keys rotated", false, "api is done: API keys rotated."},
		{"api", "I traced it to the cookie path.", false, "api is done: I traced it to the cookie path."},
		{"api", "Cut at a word…", false, "api is done: cut at a word…"},
	} {
		if got := SessionTurnLine(tc.label, tc.gist, tc.asking); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./agent/ -run 'TestSessionGist|TestSessionTurnLine'`
Expected: FAIL (undefined: `SessionGist`, `SessionTurnLine`, `MaxGistRunes`).

- [ ] **Step 3: Implement**

`core/agent/sessiongist.go`:

```go
package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The gist of a pix session's turn (#114, spec §4): one sentence of its final reply, by text
// rules (no model call): the outcome agents usually end with, or the question they end on.

// MaxGistRunes caps the gist.
const MaxGistRunes = 120

var (
	gistFence     = regexp.MustCompile("(?s)```.*?(?:```|$)")
	gistTable     = regexp.MustCompile(`(?m)^[ \t]*\|.*$`)
	gistHeading   = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	gistListItem  = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+.*$`)
	gistMarker    = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d+[.)])[ \t]+`)
	gistEmphasis  = regexp.MustCompile("\\*\\*|__|\\*|`")
	gistParagraph = regexp.MustCompile(`\n[ \t]*\n`)
	gistSentence  = regexp.MustCompile(`[.!?](?:\s|$)`)
)

// SessionGist is one sentence of reply, and whether it asks you something ("" when nothing
// readable is left, e.g. a code-only reply).
func SessionGist(reply string) (string, bool) {
	text := gistFence.ReplaceAllString(reply, "")
	text = gistTable.ReplaceAllString(text, "")
	text = gistHeading.ReplaceAllString(text, "")
	text = gistListItem.ReplaceAllStringFunc(text, func(item string) string {
		item = strings.TrimSpace(gistMarker.ReplaceAllString(item, ""))
		if r, _ := utf8.DecodeLastRuneInString(item); item != "" && !strings.ContainsRune(".!?:;", r) {
			item += "."
		}
		return item
	})
	text = gistEmphasis.ReplaceAllString(text, "")
	var paras []string
	for _, p := range gistParagraph.Split(text, -1) {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paras = append(paras, p)
		}
	}
	if len(paras) == 0 {
		return "", false
	}
	last := sentences(paras[len(paras)-1])
	if strings.HasSuffix(paras[len(paras)-1], "?") {
		return capGist(last[len(last)-1]), true
	}
	gist := last[0]
	if len(strings.Fields(gist)) < 3 {
		switch {
		case len(last) > 1:
			gist = last[1]
		case len(paras) > 1:
			gist = sentences(paras[len(paras)-2])[0]
		}
	}
	return capGist(gist), false
}

// sentences splits a non-empty paragraph after each . ! or ? that ends a sentence (never empty).
func sentences(p string) []string {
	var out []string
	start := 0
	for _, m := range gistSentence.FindAllStringIndex(p, -1) {
		if s := strings.TrimSpace(p[start : m[0]+1]); s != "" {
			out = append(out, s)
		}
		start = m[1]
	}
	if s := strings.TrimSpace(p[start:]); s != "" || len(out) == 0 {
		out = append(out, s)
	}
	return out
}

// capGist cuts s to MaxGistRunes on a word boundary, ending in "…".
func capGist(s string) string {
	if utf8.RuneCountInString(s) <= MaxGistRunes {
		return s
	}
	cut := string([]rune(s)[:MaxGistRunes-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// SessionTurnLine is what Pixie says of a finished turn: done with its gist, asking its
// question, or just finished when the reply had nothing readable.
func SessionTurnLine(label, gist string, asking bool) string {
	switch {
	case gist == "":
		return label + " finished."
	case asking:
		return label + " is asking: " + lowerFirst(gist)
	}
	if r, _ := utf8.DecodeLastRuneInString(gist); !strings.ContainsRune(".!?…", r) {
		gist += "."
	}
	return label + " is done: " + lowerFirst(gist)
}

// lowerFirst lowercases the first letter, unless the word is "I" (or "I'm", …) or an acronym.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	next, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(next) || (r == 'I' && (next == ' ' || next == '\'' || next == '’' || n == len(s))) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test ./agent/ -run 'TestSessionGist|TestSessionTurnLine' -v`
Expected: PASS. If a table case fails, fix `SessionGist`, not the expected value, unless the expected value contradicts spec §4.

- [ ] **Step 5: Commit**

```bash
git add core/agent/sessiongist.go core/agent/sessiongist_test.go
git commit -m "agent: the gist of a pix session's turn, by text rules (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 3: The turn event: wire field, agent types, pixhost

**Files:**
- Modify: `core/pix/wire/wire.go` (`Msg`, the field list ~line 36-58; the doc comment on `Msg`)
- Modify: `core/agent/types.go` (~line 303-316)
- Modify: `core/pixhost/host.go` (`onMsg` event switch ~line 223-265; new method on `session`)
- Test: `core/pix/wire/wire_test.go`, `core/pixhost/host_test.go`

**Interfaces:**
- Produces:
  - `wire.Msg.DurationMs int64` (`json:"durationMs,omitempty"`).
  - `agent.PaneEventTurn` (a `PaneEventKind`, after `PaneEventExited`).
  - `agent.PaneEvent.Turn *agent.SessionTurn` (set only for `PaneEventTurn`).
  - `type agent.SessionTurn struct { Label, Reply, App string; Duration agent.Millis; FromPixie bool }`.
  - Wire event: `{kind:"event", event:"turn", text:<reply>, durationMs:<ms>, source:"pixie"|"you"}`.

- [ ] **Step 1: Write the failing tests**

`core/pix/wire/wire_test.go`, add:

```go
func TestTurnDurationRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	Encode(&buf, Msg{Kind: KindEvent, Event: "turn", Text: "done", DurationMs: 42_000, Source: "you"})
	got, err := NewDecoder(&buf).Next()
	if err != nil || got.DurationMs != 42_000 || got.Text != "done" || got.Source != "you" {
		t.Fatalf("%+v %v", got, err)
	}
}
```

`core/pixhost/host_test.go`, add:

```go
// #114: a finished turn reaches the controller with what to call the session and where it runs.
func TestATurnBecomesAPaneEventWithTheSessionsLabel(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Cwd: "/x/api/web", App: "com.jetbrains.WebStorm"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: "All tests pass.", DurationMs: 42_000, Source: "you"})
	ev := <-events
	if ev.Kind != agent.PaneEventTurn || ev.Agent != r.Address || ev.Turn == nil {
		t.Fatalf("%+v", ev)
	}
	if want := (agent.SessionTurn{Label: "api", Reply: "All tests pass.", App: "com.jetbrains.WebStorm", Duration: 42_000}); *ev.Turn != want {
		t.Fatalf("%+v", *ev.Turn)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "fix the flaky upload test"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Source: "pixie"})
	ev = <-events
	if ev.Turn == nil || ev.Turn.Label != "fix the flaky upload test" || !ev.Turn.FromPixie {
		t.Fatalf("%+v", ev.Turn)
	}
	if len(listed(t, h)) != 1 {
		t.Fatal("a turn is not an exit")
	}
}

func TestATurnLabelFallsBackToTheFolder(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, _ := dialPix(t, sock, wire.Msg{Tool: "codex", Cwd: "/x/scratch"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := <-events; ev.Turn == nil || ev.Turn.Label != "scratch" {
		t.Fatalf("%+v", ev)
	}
	p2, _ := dialPix(t, sock, wire.Msg{Tool: "codex"})
	wire.Encode(p2.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := <-events; ev.Turn == nil || ev.Turn.Label != "codex" {
		t.Fatalf("%+v", ev)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./pix/wire/ ./pixhost/ -run 'TestTurnDurationRoundTrips|TestATurn'`
Expected: FAIL (compile errors: `DurationMs`, `PaneEventTurn`, `SessionTurn`, `Turn`).

- [ ] **Step 3: Implement**

`core/pix/wire/wire.go`: add to `Msg` after `ExitCode`:

```go
	DurationMs  int64    `json:"durationMs,omitempty"`
```

and in `Msg`'s doc comment change "event → Event (status, description, session, exited, hook) and its fields" to "event → Event (status, description, session, exited, hook, turn) and its fields".

`core/agent/types.go`: the kinds become

```go
const (
	PaneEventStatus PaneEventKind = iota
	PaneEventExited
	// PaneEventTurn: a session you started with pix finished a turn (Turn set, #114).
	PaneEventTurn
)

// PaneEvent is a status change (Status set), an exit of a named agent, or a finished turn of
// one of your pix sessions (Turn set).
type PaneEvent struct {
	Kind   PaneEventKind
	Agent  string
	Status PaneStatus   // PaneEventStatus only
	Turn   *SessionTurn // PaneEventTurn only (a pointer, so PaneEvent stays comparable)
}

// SessionTurn is a finished turn of a session you started with pix (#114).
type SessionTurn struct {
	Label     string // its description, else its repo's or folder's name, else its tool
	Reply     string // the turn's final reply (capped by pix)
	App       string // bundle id of the app it runs in ("" unknown)
	Duration  Millis // from working to its Stop; 0 when no working was seen
	FromPixie bool   // Pixie typed the turn's prompt (message_session)
}
```

`core/pixhost/host.go`: in `onMsg`'s `case wire.KindEvent: switch m.Event {`, add before `case "exited":`

```go
		case "turn":
			h.mu.Lock()
			t := agent.SessionTurn{Label: s.label(), Reply: m.Text, App: s.hello.App,
				Duration: agent.Millis(max(m.DurationMs, 0)), FromPixie: m.Source == "pixie"}
			h.mu.Unlock()
			h.broadcast(agent.PaneEvent{Kind: agent.PaneEventTurn, Agent: s.addr, Turn: &t})
```

and add the method (next to `noTaskYet`):

```go
// label names s in what Pixie says (h.mu held): its description, else its repo's or folder's
// name, else its tool.
func (s *session) label() string {
	if d := s.desc.Current(); d != "" {
		return d
	}
	for _, p := range []string{s.hello.GitRoot, s.hello.Cwd} {
		if p != "" {
			return filepath.Base(p)
		}
	}
	return s.hello.Tool
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test -race ./pix/... ./pixhost/ ./agent/...`
Expected: PASS (the agent package still compiles: the controller's `default:` branch now receives `PaneEventTurn` but nothing sends one to it yet; Task 5 adds the explicit case).

- [ ] **Step 5: Commit**

```bash
git add core/pix/wire core/agent/types.go core/pixhost
git commit -m "pixhost: a pix session's finished turn becomes a pane event (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 4: `pix` reports each finished turn

**Files:**
- Modify: `core/pix/session.go` (`Session` struct ~line 67-82; `newSession` ~line 120; Claude interrupt branch ~line 131-139; `Hook` `UserPromptSubmit` ~line 151-167 and `Stop` ~line 169-180; `setStatus` ~line 288)
- Test: `core/pix/session_test.go`

**Interfaces:**
- Consumes: `wire.Msg.DurationMs` (Task 3); the wire event shape from Task 3.
- Produces: `const MaxTurnReplyBytes = 4 << 10`; `func capUTF8(s string, n int) string`; `Session.now func() time.Time` (tests replace it under `s.mu`).

- [ ] **Step 1: Write the failing tests**

Add to `core/pix/session_test.go` (it already imports `strings`, `time`, `wire`; add `unicode/utf8` to its imports):

```go
// setClock makes s read the time from *at (under s.mu: the PTY goroutines read it too).
func setClock(s *Session, at *time.Time) {
	s.mu.Lock()
	s.now = func() time.Time { return *at }
	s.mu.Unlock()
}

func turnEvent(t *testing.T, h *harness) wire.Msg {
	t.Helper()
	return waitEvent(t, h.d.events, "turn", func(m wire.Msg) bool { return m.Event == "turn" })
}

// #114: a finished turn goes to DeskPixie with its reply, how long it ran and who started it.
func TestAClaudeTurnReportsItsReplyAndDuration(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "fix the flaky upload test"})
	at = at.Add(42 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Fixed: the upload test is stable now."})
	m := turnEvent(t, h)
	if m.Text != "Fixed: the upload test is stable now." || m.DurationMs != 42_000 || m.Source != "you" {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestATurnPixieStartedSaysSo(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	task := "Task from the coordinator: run the upload tests again."
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "p", Command: "prompt", Text: task})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "p" })
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": task + "\n"})
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "They pass."})
	if m := turnEvent(t, h); m.Source != "pixie" {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestACodexTurnReportsItsLastMessage(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("Stop", map[string]any{"type": "agent-turn-complete", "input-messages": []any{"rename foo to bar"},
		"last-assistant-message": "Rename complete and verified."})
	m := turnEvent(t, h)
	if m.Text != "Rename complete and verified." || m.DurationMs != 0 || m.Source != "you" {
		t.Fatalf("no working seen: duration 0; %+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestAnInterruptedTurnDoesNotCountTowardTheNext(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	at = at.Add(10 * time.Minute)
	h.s.interrupted() // what the screen marker does
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "something quick"})
	at = at.Add(3 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Done."})
	if m := turnEvent(t, h); m.DurationMs != 3_000 {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestTheReplyIsCutOnACharacterBoundary(t *testing.T) {
	got := capUTF8(strings.Repeat("é", 3_000), MaxTurnReplyBytes)
	if len(got) > MaxTurnReplyBytes || !utf8.ValidString(got) || len(got) < MaxTurnReplyBytes-1 {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	if capUTF8("short", MaxTurnReplyBytes) != "short" {
		t.Fatal("short replies are kept")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./pix/ -run 'TestAClaudeTurn|TestATurnPixie|TestACodexTurn|TestAnInterruptedTurn|TestTheReplyIsCut'`
Expected: FAIL (undefined: `now` field, `interrupted`, `capUTF8`, `MaxTurnReplyBytes`).

- [ ] **Step 3: Implement**

`core/pix/session.go` (add `"unicode/utf8"` to its imports):

1. In `Session`, after `posted`:

```go
	turnStart     time.Time        // when this turn went working (zero: none seen)
	turnFromPixie bool             // a prompt of this turn was Pixie's
	now           func() time.Time // the clock (tests replace it, under mu)
```

2. In `newSession`, the literal gains `now: time.Now`:

```go
	s := &Session{o: o, status: "idle", afterStop: true, userDesc: o.description, now: time.Now}
```

3. Replace the Claude interrupt branch body with a call to a new method, so the turn's timing resets:

```go
	case "claude":
		s.runner.OnOutput = func(b []byte) {
			if watch.saw(string(b), claudeInterrupted) && s.currentStatus() == "working" {
				s.interrupted()
			}
		}
```

and add:

```go
// interrupted: Claude shows an interrupt (Esc) and fires no Stop: the turn is over, unannounced.
func (s *Session) interrupted() {
	s.mu.Lock()
	s.afterStop = true
	s.turnStart, s.turnFromPixie = time.Time{}, false
	s.mu.Unlock()
	s.setStatus("idle")
	s.flushSoon()
}
```

4. In `Hook`'s `UserPromptSubmit`, inside the existing `s.mu.Lock()` … `s.mu.Unlock()` right after `pixie := s.fromPixie(prompt)`, add as the first line under the lock:

```go
		s.turnFromPixie = s.turnFromPixie || pixie
```

5. Replace the `case "Stop":` block with:

```go
	case "Stop":
		pixie := false
		if s.o.tool == "codex" { // its notify carries the turn's prompts; there is no prompt hook
			if in, _ := p["input-messages"].([]any); len(in) > 0 {
				if first, _ := in[0].(string); first != "" {
					if s.fromPixie(first) {
						pixie = true
					} else {
						s.newWork(first)
					}
				}
			}
		}
		s.endTurn(p, pixie)
		s.gate.Started() // a turn ran, so your line was sent (a fast Codex turn shows no busy marker)
		s.mu.Lock()
		s.afterStop = true
		s.mu.Unlock()
		s.setStatus("idle")
		s.flushSoon()
```

6. `setStatus` records the turn's start:

```go
func (s *Session) setStatus(st string) {
	if st == "working" {
		s.gate.Started()
	}
	s.mu.Lock()
	if st == "working" && s.turnStart.IsZero() {
		s.turnStart = s.now()
	}
	s.status = st
	s.mu.Unlock()
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "status", Status: st})
}
```

7. Add:

```go
// MaxTurnReplyBytes caps the reply a turn event carries (#114).
const MaxTurnReplyBytes = 4 << 10

// endTurn tells DeskPixie a turn finished (#114): its final reply (Claude's Stop has
// last_assistant_message, Codex's notify last-assistant-message), how long it ran and whether
// Pixie typed its prompt. The app decides whether to announce it.
func (s *Session) endTurn(p map[string]any, pixie bool) {
	reply, _ := p["last_assistant_message"].(string)
	if reply == "" {
		reply, _ = p["last-assistant-message"].(string)
	}
	s.mu.Lock()
	var d time.Duration
	if !s.turnStart.IsZero() {
		d = s.now().Sub(s.turnStart)
	}
	pixie = pixie || s.turnFromPixie
	s.turnStart, s.turnFromPixie = time.Time{}, false
	s.mu.Unlock()
	source := "you"
	if pixie {
		source = "pixie"
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: capUTF8(reply, MaxTurnReplyBytes),
		DurationMs: max(d.Milliseconds(), 0), Source: source})
}

// capUTF8 cuts s to at most n bytes without splitting a character.
func capUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test -race ./pix/...`
Expected: PASS, including the existing `TestHooksReportStatusAndAutoDescription`, `TestCodexDescriptionFromItsTurn` and `TestClaudeInterruptOnScreenMeansIdle`.

- [ ] **Step 5: Commit**

```bash
git add core/pix/session.go core/pix/session_test.go
git commit -m "pix: report each finished turn with its reply and duration (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 5: The controller announces it

**Files:**
- Modify: `core/agent/routing.go` (`LineKind` consts and names ~line 17-30; `Prefix` ~line 49; `Line` and `Spoken` ~line 61-80; `Router.Route` and `RouteUpdate` ~line 129-160)
- Modify: `core/agent/outbox.go` (`OutboxLine`, its `UnmarshalJSON`, `Outbox.Push`)
- Modify: `core/agent/bubble.go` (`UpdateItem`, `bubbleItems`)
- Modify: `core/agent/controller.go` (the `InPane` switch ~line 485-504; new `onSessionTurn`)
- Create: `core/agent/session_turn_test.go`

**Interfaces:**
- Consumes: `Settings.SessionTurns`, `Settings.SessionTurnsMinMs` (Task 1); `SessionGist`, `SessionTurnLine` (Task 2); `PaneEventTurn`, `PaneEvent.Turn`, `SessionTurn` (Task 3); existing `maskPendingTokens`, `c.pendingTokens()`, `truncateBytes(s string, max int) string`, `c.deliverUpdate(line, now, fx)`.
- Produces: `LineSession` (serialized `"session"`); `Line.Asking bool`, `Line.Detail string`, `Line.App string`; `OutboxLine.App` and `UpdateItem.App` (`json:"app,omitempty"`); `const MaxSessionDetailBytes = 2 << 10`.

- [ ] **Step 1: Write the failing tests**

`core/agent/session_turn_test.go`:

```go
package agent

import (
	"strings"
	"testing"
)

// #114: a pix session's finished turn, announced when the setting is on.

const webstorm = "com.jetbrains.WebStorm"

func turnCtl(edit func(*Settings)) *AgentController {
	return ctlWith(func(s *Settings) {
		s.SessionTurns = true
		if edit != nil {
			edit(s)
		}
	})
}

func sessionTurn(c *AgentController, t SessionTurn, now Millis) []Effect {
	return c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "api-claude", Turn: &t}), now)
}

var longTurn = SessionTurn{Label: "deskpixie: fix login redirect",
	Reply: "I traced the loop to the cookie path.\n\nThe redirect loop is fixed and all 412 tests pass.",
	App:   webstorm, Duration: 45_000}

const longTurnLine = "deskpixie: fix login redirect is done: the redirect loop is fixed and all 412 tests pass."

func TestWithTheSettingOffATurnIsNotAnnounced(t *testing.T) {
	if fx := sessionTurn(ctl(), longTurn, T0); anyKind(fx, EfInject, EfNotify, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
}

func TestYourOwnShortTurnStaysQuiet(t *testing.T) {
	short := longTurn
	short.Duration = 5_000
	if fx := sessionTurn(turnCtl(nil), short, T0); anyKind(fx, EfInject, EfNotify, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
}

func TestYourOwnLongTurnIsAnnouncedWithItsGist(t *testing.T) {
	fx := sessionTurn(turnCtl(nil), longTurn, T0)
	wantContains(t, fx, eNotify(longTurnLine))
	if !anyKind(fx, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
	var bubble []UpdateItem
	for _, e := range effectsOfKind(fx, EfUpdates) {
		bubble = e.Value.([]UpdateItem)
	}
	if len(bubble) != 1 || bubble[0].Kind != LineSession || bubble[0].App != webstorm || bubble[0].Text != longTurnLine {
		t.Fatalf("the bubble line activates its app: %+v", bubble)
	}
}

func TestATurnPixieStartedIsAnnouncedHoweverShort(t *testing.T) {
	quick := longTurn
	quick.Duration, quick.FromPixie = 1_000, true
	wantContains(t, sessionTurn(turnCtl(nil), quick, T0), eNotify(longTurnLine))
}

func TestATurnThatAsksIsAnnouncedOverTheCap(t *testing.T) {
	c := turnCtl(func(s *Settings) { s.NotifyPerMinute = 1 })
	sessionTurn(c, longTurn, T0) // takes the minute's only slot
	asks := longTurn
	asks.Reply = "Fixed the loop.\n\nShould I also update the docs?"
	wantContains(t, sessionTurn(c, asks, T0+1), eNotify("deskpixie: fix login redirect is asking: should I also update the docs?"))
}

func TestInAConversationTheVoiceModelGetsTheReply(t *testing.T) {
	c := turnCtl(nil)
	openVoice(c)
	wantContains(t, sessionTurn(c, longTurn, T0), eInject("Session update: "+longTurnLine+
		" Its reply, to sum up in one sentence: I traced the loop to the cookie path. The redirect loop is fixed and all 412 tests pass."))
}

func TestASessionTurnNeverSpeaksAPendingConfirmWord(t *testing.T) {
	c := workingOnJob1()
	c.settings.SessionTurns = true
	askPush(c, 7, T0) // "hawk" is pending
	turn := SessionTurn{Label: "hawk rollout", Reply: "Say hawk to approve the push.", Duration: 60_000}
	fx := sessionTurn(c, turn, T0)
	if !anyKind(fx, EfNotify) {
		t.Fatal(fmtEffects(fx))
	}
	for _, e := range fx {
		if (e.Kind == EfNotify && strings.Contains(strings.ToLower(e.Text), "hawk")) ||
			(e.Kind == EfInject && strings.Contains(strings.ToLower(e.Notice.Text), "hawk")) {
			t.Fatalf("confirm word spoken: %s", fmtEffects(fx))
		}
	}
}

func TestASessionLineKeepsItsAppInTheOutbox(t *testing.T) {
	o := NewOutbox()
	rec := o.Push(Line{Kind: LineSession, Text: "api finished.", App: webstorm}, T0)
	var back OutboxLine
	if err := back.UnmarshalJSON([]byte(rec)); err != nil || back.App != webstorm || back.Kind != LineSession {
		t.Fatalf("%s → %+v %v", rec, back, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./agent/ -run 'Turn|SessionLine'`
Expected: FAIL (undefined: `LineSession`, `Line.App`, `OutboxLine.App`, `UpdateItem.App`).

- [ ] **Step 3: Implement**

`core/agent/routing.go`:

1. Add after `LineInfo` in the const block:

```go
	// LineSession is a session you started with pix finishing a turn (#114).
	LineSession
```

and change the names to `var lineKindNames = []string{"reply", "question", "unsolicited", "info", "session"}`.

2. In `Prefix`, add `case LineSession: return "Session update: "`.

3. `Line` gains, after `Worker`:

```go
	// Asking: the line asks you something; like a question, never held by the cap.
	Asking bool
	// Detail: more for the voice model to sum up (a session's reply); never shown.
	Detail string
	// App: the bundle id of the app a session line's session runs in (a click activates it).
	App string
```

4. `Spoken` becomes:

```go
// Spoken is the text as the voice model hears it.
func (l Line) Spoken() string {
	if l.Detail != "" {
		return l.Kind.Prefix() + l.Text + " Its reply, to sum up in one sentence: " + l.Detail
	}
	return l.Kind.Prefix() + l.Text
}

// urgent: a question, or a line that asks you something: never held by the cap.
func (l Line) urgent() bool { return l.Kind == LineQuestion || l.Asking }
```

5. In `Router.Route`, replace both `line.Kind == LineQuestion` tests with `line.urgent()`:

```go
	if line.urgent() || r.Take(now) {
		if line.urgent() {
			r.sent = append(r.sent, now)
		}
```

and in `RouteUpdate`: `if !conversationOpen || line.urgent() {`.

`core/agent/outbox.go`:

- `OutboxLine` gains after `Worker`: `App string \`json:"app,omitempty"\``.
- Its `UnmarshalJSON` raw struct gains `App string \`json:"app"\`` and the result literal gains `App: raw.App`.
- `Push` builds `entry := OutboxLine{…, Worker: line.Worker, App: line.App}`.

`core/agent/bubble.go`:

- `UpdateItem` gains after `Worker`:

```go
	// App: a session line's app (bundle id); a click activates it instead of a pane.
	App string `json:"app,omitempty"`
```

- In `bubbleItems`, the item literal gains `App: l.App`.

`core/agent/controller.go`:

1. In `Step`'s `case InPane:` switch, add before `case ev.Kind == PaneEventStatus:` (it must come before `default:`, which takes any other kind as a closed pane):

```go
		case ev.Kind == PaneEventTurn:
			if ev.Turn != nil {
				c.onSessionTurn(*ev.Turn, now, &fx)
			}
```

2. Add (next to `deliverUpdate`):

```go
// MaxSessionDetailBytes caps the reply the voice model is given to sum up (#114).
const MaxSessionDetailBytes = 2 << 10

// onSessionTurn: a session you started with pix finished a turn (#114). With the setting on,
// a turn of yours that ran long enough, or any turn Pixie started, is told like an update:
// its gist, or its question (never held by the cap). Pending confirm words are masked in all
// of it: the label is agent-settable too.
func (c *AgentController) onSessionTurn(t SessionTurn, now Millis, fx *[]Effect) {
	if !c.settings.SessionTurns || c.shutDown {
		return
	}
	if !t.FromPixie && t.Duration < c.settings.SessionTurnsMinMs {
		return
	}
	tokens := c.pendingTokens()
	reply := maskPendingTokens(t.Reply, tokens)
	gist, asking := SessionGist(reply)
	c.deliverUpdate(Line{
		Kind:   LineSession,
		Text:   SessionTurnLine(maskPendingTokens(t.Label, tokens), gist, asking),
		Asking: asking,
		App:    t.App,
		Detail: truncateBytes(strings.Join(strings.Fields(reply), " "), MaxSessionDetailBytes),
	}, now, fx)
}
```

(`controller.go` already imports `strings`; check, and add it if not.)

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test -race ./agent/...`
Expected: PASS, including every existing routing, outbox and bubble test. If `TestASessionTurnNeverSpeaksAPendingConfirmWord` finds no `EfNotify`, check whether `askPush` left the conversation open (then assert on `EfInject` only); never weaken the "no hawk" check.

- [ ] **Step 5: Run the whole Go suite and the app build of the core**

Run: `go -C core test -race ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add core/agent
git commit -m "agent: announce a pix session's finished turn (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 6: A click on a session line activates its app

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/UpdatesBubbleModel.swift` (`UpdateLine`, ~line 5-32)
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/UpdatesBubbleModelTests.swift`
- Create: `mac/DeskPixie/Pixie/AppActivation.swift`
- Modify: `mac/DeskPixie/Pixie/UpdatesBubbleController.swift` (`clicked`, ~line 135)
- Modify: `mac/DeskPixie/Pixie/AgentRosterController.swift` (`clicked`, ~line 73-85)

**Interfaces:**
- Consumes: the bubble's `updates` lines now carry `"kind":"session"` and `"app":"<bundle id>"` (Task 5).
- Produces: `UpdateLine.app: String?`; `enum AppActivation { static func activate(bundleID: String) }`.

- [ ] **Step 1: Write the failing test**

In `UpdatesBubbleModelTests.swift`, add:

```swift
    func testASessionLineCarriesItsApp() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":4,"text":"api is done: tests pass.","kind":"session","at":4,"app":"com.jetbrains.WebStorm"}"#.utf8))
        XCTAssertEqual(line.app, "com.jetbrains.WebStorm")
        XCTAssertFalse(line.offersDecision)
        let reply = try JSONDecoder().decode(UpdateLine.self, from: Data(#"{"id":5,"text":"Done.","kind":"reply","at":5}"#.utf8))
        XCTAssertNil(reply.app)
    }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd mac/Packages/DeskPixieCore && swift test --filter UpdatesBubbleModelTests`
Expected: FAIL to compile (`UpdateLine` has no member `app`).

- [ ] **Step 3: Implement the model**

In `UpdateLine`: change the `kind` comment to `/// "reply" | "question" | "unsolicited" | "info" | "session".`, add after `token`:

```swift
    /// A session line's app (bundle id): a click activates it instead of focusing a pane (#114).
    public let app: String?
```

and the init becomes:

```swift
    public init(id: UInt64, text: String, kind: String, at: Int64, job: String?, worker: String?,
                token: String? = nil, app: String? = nil) {
        self.id = id
        self.text = text
        self.kind = kind
        self.at = at
        self.job = job
        self.worker = worker
        self.token = token
        self.app = app
    }
```

- [ ] **Step 4: Run the package tests**

Run: `cd mac/Packages/DeskPixieCore && swift test`
Expected: PASS (all tests, 431 or more).

- [ ] **Step 5: Implement the click**

`mac/DeskPixie/Pixie/AppActivation.swift`:

```swift
import AppKit
import os

/// Brings a running app forward by bundle id, like a Dock click: the roster's session rows and
/// the bubble's session lines (#114).
enum AppActivation {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "activate")

    static func activate(bundleID id: String) {
        guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: id).first,
              let url = app.bundleURL else { return }
        // Launch Services activates like a Dock click; NSRunningApplication.activate() can be
        // ignored from an inactive accessory app.
        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: cfg) { _, error in
            if let error { log.error("activate \(id, privacy: .public): \(String(describing: error), privacy: .public)") }
        }
    }
}
```

In `AgentRosterController.clicked`, replace the `if r.kind == "session" { … }` body with:

```swift
        if r.kind == "session" {
            guard let id = r.app else { return }
            AppActivation.activate(bundleID: id)
        } else {
```

(keep the `else` branch as it is).

In `UpdatesBubbleController.clicked`:

```swift
    private func clicked(_ line: UpdateLine) {
        if let app = line.app {
            AppActivation.activate(bundleID: app)
            return
        }
        call("agent.focusJob", FocusJobArgs(job: line.job ?? "", worker: line.worker ?? ""))
    }
```

- [ ] **Step 6: Build the app**

Run: `make -C mac build`
Expected: `** BUILD SUCCEEDED **` (the Makefile regenerates the Xcode project because a source file was added).

- [ ] **Step 7: Commit**

```bash
git add mac/Packages/DeskPixieCore mac/DeskPixie/Pixie
git commit -m "mac: a click on a session's bubble line brings its app forward (#114)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

## Spec deviations decided in this plan

- **Notification click (spec §2 "How it reaches you").** The spec says clicking the notification *or* the bubble line brings the session forward. This plan wires the **bubble line** only. The existing macOS notification has no per-line click target today, and adding one is a separate change. Cost if wrong: a click on the notification does what it does today.
- **How the click is wired.** The spec says "#98's reuse applies". Bubble clicks go through `agent.focusJob`, which focuses a Herdr pane and attaches its terminal, so it can't reach a `pix` session. The plan instead puts the session app's bundle ID on the line and activates that app, exactly as a roster row does.
