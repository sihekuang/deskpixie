# Resume past pix sessions from History — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep a short history of Claude Code sessions run through `pix`, and let the user resume
one by clicking it (menu bar / Pixie's right-click menu ▸ History, or Settings ▸ Sessions); it
reopens in a Herdr tab of its own session (`<agent.session>-yours`) as *your* session (⌨).

**Architecture:** Go decides everything: `core/pixhost` records history (a JSON file behind a
small store interface) and a `Resumer` turns "resume id" into a Herdr tab running
`pix claude --resume <id>` plus a `FocusPlan` that shows it. The tray model (`app.state`)
carries preformatted History rows; clicks are `agent:history:*` ids through the existing
`app.action` → `Agent.Menu` path. Swift only draws the submenu (shared by the menu bar and the
right-click menu) and a Settings extra row.

**Tech Stack:** Go 1.x (`core/`), Herdr socket API (protocol 22: `tab.create`,
`pane.send_input`, `pane.focus`), Swift/SwiftUI (`mac/`), XCTest.

**Spec:** `docs/superpowers/specs/2026-11-20-pix-session-history-design.md` (#461).

**Rules (AGENTS.md):** tests never touch the real home/app data/herdr session (`t.TempDir()`,
temp `HOME`, fakes); write code like the code around it (short doc comments, `(#461)` refs);
every commit message ends with
`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

Run Go tests from `core/`: `go test ./pixhost/ -run TestX -v`. Run Swift tests from
`mac/Packages/DeskPixieCore`: `swift test --filter X`.

---

## Phase A — Go: history

### Task A1: the history store

**Files:**
- Create: `core/pixhost/history.go`
- Test: `core/pixhost/history_test.go`

**Step 1: Write the failing tests**

```go
package pixhost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func at(min int) time.Time { return time.Date(2026, 11, 20, 10, min, 0, 0, time.UTC) }

func TestHistoryRecordUpdatesByID(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", Tool: "claude", Description: "first", Cwd: "/r", LastActive: at(1)})
	h.Record(HistoryEntry{SessionID: "a", Description: "login bug", LastActive: at(2)})
	got := h.List()
	if len(got) != 1 || got[0].Description != "login bug" || got[0].Cwd != "/r" || got[0].Tool != "claude" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryNewestFirstAndTrimmed(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	for i := range HistoryLimit + 3 {
		h.Record(HistoryEntry{SessionID: string(rune('a' + i)), LastActive: at(i)})
	}
	got := h.List()
	if len(got) != HistoryLimit || got[0].SessionID != string(rune('a'+HistoryLimit+2)) {
		t.Fatalf("len %d first %q", len(got), got[0].SessionID)
	}
}

func TestHistoryPersistsAcrossLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	NewHistoryFile(p).Record(HistoryEntry{SessionID: "a", Description: "x", LastActive: at(1)})
	if got := NewHistoryFile(p).List(); len(got) != 1 || got[0].Description != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryRemoveAndClear(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	h.Record(HistoryEntry{SessionID: "b", LastActive: at(2)})
	h.Remove("a")
	if got := h.List(); len(got) != 1 || got[0].SessionID != "b" {
		t.Fatalf("after remove %+v", got)
	}
	h.Clear()
	if got := h.List(); len(got) != 0 {
		t.Fatalf("after clear %+v", got)
	}
}

func TestHistoryDamagedFileIsSetAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	os.WriteFile(p, []byte("{not json"), 0o600)
	h := NewHistoryFile(p)
	if got := h.List(); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatalf("damaged file not set aside: %v", err)
	}
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if got := NewHistoryFile(p).List(); len(got) != 1 {
		t.Fatalf("not written after a damaged file: %+v", got)
	}
}

func TestHistoryUnwritableFolderKeepsInMemory(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o600) // a file where the folder should be
	h := NewHistoryFile(filepath.Join(blocker, "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if got := h.List(); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryUpdateKeepsOtherFields(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", Description: "x", Cwd: "/r", LastActive: at(1)})
	h.Update("a", func(e *HistoryEntry) { e.HerdrPane = "w1:p3" })
	h.Update("missing", func(e *HistoryEntry) { e.HerdrPane = "nope" })
	got := h.List()
	if len(got) != 1 || got[0].HerdrPane != "w1:p3" || got[0].Description != "x" {
		t.Fatalf("got %+v", got)
	}
}
```

**Step 2: Run to verify they fail**

Run: `cd core && go test ./pixhost/ -run TestHistory -v`
Expected: FAIL to compile (`NewHistoryFile` undefined).

**Step 3: Implement**

```go
package pixhost

import (
	"cmp"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// HistoryLimit is how many past sessions History keeps (#461).
const HistoryLimit = 20

// HistoryEntry is one Claude conversation you ran through pix (#461).
type HistoryEntry struct {
	SessionID   string    `json:"sessionId"`
	Tool        string    `json:"tool"`
	Description string    `json:"description,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	GitRoot     string    `json:"gitRoot,omitempty"`
	Where       string    `json:"where,omitempty"`
	Started     time.Time `json:"started"`
	LastActive  time.Time `json:"lastActive"`
	Ended       bool      `json:"ended,omitempty"`
	// Unresumable: a resume of it ended at once without Claude reporting the conversation.
	Unresumable bool `json:"unresumable,omitempty"`
	// HerdrPane: the pane of its tab in the "yours" Herdr session, when it was resumed there.
	HerdrPane string `json:"herdrPane,omitempty"`
}

// HistoryStore keeps the past sessions (the pix host's seam; HistoryFile is the real one).
type HistoryStore interface {
	// Record merges e into the entry with its SessionID (non-zero fields win), or adds it.
	Record(e HistoryEntry)
	// Update changes the entry with id in place; nothing when there is none.
	Update(id string, f func(*HistoryEntry))
	// List is every entry, the most recently active first.
	List() []HistoryEntry
	Remove(id string)
	Clear()
}

// HistoryFile is History in a JSON file. A damaged file is set aside (.bad) and history starts
// empty; a failed write is logged and the entries stay in memory.
type HistoryFile struct {
	path    string
	mu      sync.Mutex
	entries []HistoryEntry
}

func NewHistoryFile(path string) *HistoryFile {
	h := &HistoryFile{path: path}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &h.entries); err != nil {
			log.Printf("pix: history: %s is damaged (%v); starting over", path, err)
			h.entries = nil
			os.Rename(path, path+".bad")
		}
	}
	return h
}

func (h *HistoryFile) Record(e HistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.entries, func(x HistoryEntry) bool { return x.SessionID == e.SessionID })
	if i < 0 {
		h.entries = append(h.entries, e)
	} else {
		h.entries[i] = merge(h.entries[i], e)
	}
	h.saveLocked()
}

// merge is old with e's non-zero fields over it. Ended and Unresumable are set by Update only.
func merge(old, e HistoryEntry) HistoryEntry {
	old.Tool = cmp.Or(e.Tool, old.Tool)
	old.Description = cmp.Or(e.Description, old.Description)
	old.Cwd = cmp.Or(e.Cwd, old.Cwd)
	old.GitRoot = cmp.Or(e.GitRoot, old.GitRoot)
	old.Where = cmp.Or(e.Where, old.Where)
	old.HerdrPane = cmp.Or(e.HerdrPane, old.HerdrPane)
	if old.Started.IsZero() {
		old.Started = e.Started
	}
	if e.LastActive.After(old.LastActive) {
		old.LastActive = e.LastActive
	}
	old.Ended = false // recorded again: it runs
	return old
}

func (h *HistoryFile) Update(id string, f func(*HistoryEntry)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := slices.IndexFunc(h.entries, func(x HistoryEntry) bool { return x.SessionID == id }); i >= 0 {
		f(&h.entries[i])
		h.saveLocked()
	}
}

func (h *HistoryFile) List() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.entries)
}

func (h *HistoryFile) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = slices.DeleteFunc(h.entries, func(x HistoryEntry) bool { return x.SessionID == id })
	h.saveLocked()
}

func (h *HistoryFile) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
	h.saveLocked()
}

// saveLocked sorts (newest first), trims to HistoryLimit and writes atomically (h.mu held).
func (h *HistoryFile) saveLocked() {
	slices.SortStableFunc(h.entries, func(a, b HistoryEntry) int { return b.LastActive.Compare(a.LastActive) })
	if len(h.entries) > HistoryLimit {
		h.entries = h.entries[:HistoryLimit]
	}
	b, err := json.MarshalIndent(cmp.Or(h.entries, []HistoryEntry{}), "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(h.path), 0o700)
	}
	if err == nil {
		tmp := h.path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, h.path)
		}
	}
	if err != nil {
		log.Printf("pix: history: cannot save %s: %v", h.path, err)
	}
}
```

Note: `cmp.Or` on a nil slice doesn't compile (slices aren't comparable). Write the
marshal input as `entries := h.entries; if entries == nil { entries = []HistoryEntry{} }`.

**Step 4: Run tests**

Run: `cd core && go test ./pixhost/ -run TestHistory -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add core/pixhost/history.go core/pixhost/history_test.go
git commit -m "pixhost: a history of past pix sessions in a JSON file (#461)"
```

---

### Task A2: pix tells the host which conversation it resumes; `PIX_WHERE`

A failed resume never reports a session id, so the host must know up front which conversation a
pix is resuming. pix reads `--resume <id>` / `-r <id>` / `--resume=<id>` from Claude's args and
sends it in its hello as `Resumes`. `PIX_WHERE` lets the Herdr tab say where it runs.

**Files:**
- Modify: `core/pix/wire/wire.go` (Msg: add `Resumes string \`json:"resumes,omitempty"\``,
  documented with the register fields; no protocol bump: older apps ignore it)
- Modify: `core/pix/where.go` (`Where` reads `PIX_WHERE` first; new `ResumeID`)
- Modify: `core/pix/session.go:1245` (hello gets `Resumes: s.o.resumes`), `sessionOpts` gets
  `resumes string`, `Run` sets it with `ResumeID(tool, rest)`
- Test: `core/pix/where_test.go` (create if missing; else add to the existing where tests —
  `grep -ln "func TestWhere" core/pix/*_test.go`)

**Step 1: Failing tests**

```go
func TestWherePixWhereWins(t *testing.T) {
	env := map[string]string{"PIX_WHERE": "Herdr", "TERM_PROGRAM": "iTerm.app"}
	if got := Where(func(k string) string { return env[k] }); got != "Herdr" {
		t.Fatal(got)
	}
}

func TestResumeID(t *testing.T) {
	cases := []struct {
		tool string
		args []string
		want string
	}{
		{"claude", []string{"--resume", "abc"}, "abc"},
		{"claude", []string{"-r", "abc", "--model", "x"}, "abc"},
		{"claude", []string{"--resume=abc"}, "abc"},
		{"claude", []string{"--resume"}, ""},            // the picker: no id yet
		{"claude", []string{"--resume", "--model"}, ""}, // a flag, not an id
		{"claude", []string{"--continue"}, ""},
		{"codex", []string{"resume", "abc"}, ""}, // Codex isn't recorded yet (#461)
	}
	for _, c := range cases {
		if got := ResumeID(c.tool, c.args); got != c.want {
			t.Errorf("%s %v: got %q want %q", c.tool, c.args, got, c.want)
		}
	}
}
```

**Step 2:** `cd core && go test ./pix/ -run 'TestWherePixWhereWins|TestResumeID' -v` → FAIL.

**Step 3: Implement** in `where.go`:

```go
// Where names the terminal or IDE pix runs in, shown with the session in Bit's list. PIX_WHERE
// wins: DeskPixie sets it on the Herdr tab it resumes a session in (#461).
func Where(env func(string) string) string {
	if w := env("PIX_WHERE"); w != "" {
		return w
	}
	... (unchanged)
}

// ResumeID is the conversation `claude --resume <id>` resumes (#461); "" for anything else.
func ResumeID(tool string, args []string) string {
	if tool != "claude" {
		return ""
	}
	for i, a := range args {
		if id, ok := strings.CutPrefix(a, "--resume="); ok {
			return id
		}
		if (a == "--resume" || a == "-r") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
		}
	}
	return ""
}
```

Wire it: `sessionOpts{..., resumes: ResumeID(tool, rest)}` in `Run`, and `Resumes: s.o.resumes`
in the hello literal at `session.go:1245`.

**Step 4:** run the two tests, then `go test ./pix/...` → PASS.

**Step 5: Commit** — `pix: say which conversation a run resumes; PIX_WHERE names where it runs (#461)`

---

### Task A3: the pix host records history

**Files:**
- Modify: `core/pixhost/host.go`
- Create: `core/pixhost/history_host_test.go`

Behaviour:
- `(*Host).SetHistory(store HistoryStore, now func() time.Time)` — nil store records nothing
  (default, so every existing test is unaffected).
- `session` gets `resumes string` (from `hello.Resumes`) and `reported bool` (a `session`
  event arrived).
- Record when: a `session` event is accepted (not a duplicate) — full entry from `s.hello`,
  `s.desc.Current()`, `Started: s.started`, `LastActive: now()`; a `description` event on a
  session with a `sessionID` — `Record({SessionID, Description, LastActive})`.
- If `s.resumes != "" && m.SessionID != s.resumes` on the `session` event (Claude forked a new
  id), `Remove(s.resumes)` after recording the new one.
- In `remove(s)`: if `s.sessionID != ""` → `Update(id, Ended = true, LastActive = now())`.
  Else if `s.resumes != "" && !s.reported && now().Sub(s.started) < ResumeFailWithin` →
  `Update(s.resumes, Unresumable = true)`. `const ResumeFailWithin = 30 * time.Second`.
- `(*Host).History() []HistoryEntry` — the store's list minus entries whose `SessionID` is held
  by a connected or unreachable session right now; nil store → nil.
- `(*Host).RemoveHistory(id)`, `(*Host).ClearHistory()` pass through.
- Every history change calls `h.changed()` so the tray refreshes (the app already pushes the
  tray on `Changes()` — verify in `agentapp.go` with `grep -n "Changes()" core/app/agentapp/*.go`;
  if the roster is the only listener, add `call(a.hooks.RefreshTray)` there too).
- Never call the store with `h.mu` held for longer than a copy: read what you need under the
  lock, record after unlocking (the store has its own lock and does file IO).

**Step 1: Failing tests.** Use the package's existing test helpers to connect a fake pix —
read `core/pixhost/host_test.go` first (`grep -n "^func " core/pixhost/host_test.go`) and reuse
its dial/register/send helpers. Tests (one each):

1. `TestHistoryRecordsOnSessionEvent` — register (`Tool: "claude", Cwd, GitRoot, Where`),
   send `session` event `SessionID: "s1"` → `fakeStore` has one entry with all fields.
2. `TestHistoryRecordsDescriptionChange` — after (1), send `description` (`Source: "agent"`)
   → entry's Description updated.
3. `TestHistoryHidesRunning` — after (1), `h.History()` is empty; after `exited` it has `s1`
   with `Ended`.
4. `TestHistoryMarksFailedResume` — register with `Resumes: "old"`, send `exited` at once (no
   session event) → `old` is `Unresumable`. With `now` advanced past `ResumeFailWithin` → not.
5. `TestHistoryForkedResumeReplacesOld` — register `Resumes: "old"`, session event `"new"` →
   `old` removed, `new` present.
6. `TestHistoryNilStoreIsSafe` — no `SetHistory`; full lifecycle doesn't panic; `History()` nil.

`fakeStore` (in the test file) wraps a `*HistoryFile` on `t.TempDir()` — the real store is
cheap and keeps the fake honest (Liskov).

**Step 2:** `go test ./pixhost/ -run TestHistory -v` → FAIL.
**Step 3:** implement as described.
**Step 4:** `go test ./pixhost/... -race` → PASS.
**Step 5: Commit** — `pixhost: record pix sessions in History; mark a resume that fails at once (#461)`

---

## Phase B — Go: opening the session in Herdr

### Task B1: Herdr client — type into a pane, focus a pane

**Files:**
- Modify: `core/herdr/client.go`
- Test: `core/herdr/client_test.go` (follow the existing `tr.answer` / `tr.sentFor` /
  `assertJSON` style)

```go
// SendInput types text into a pane, then presses keys (#461: a command line and Enter).
func (c *Client) SendInput(ctx context.Context, paneID, text string, keys []string) error {
	_, err := c.call(ctx, "pane.send_input", paneID, map[string]any{"pane_id": paneID, "text": text, "keys": nonNil(keys)})
	return err
}

// FocusPane brings a pane (and its tab) to the front of the session.
func (c *Client) FocusPane(ctx context.Context, paneID string) error {
	_, err := c.call(ctx, "pane.focus", paneID, map[string]any{"pane_id": paneID})
	return err
}
```

Tests assert the exact JSON sent: `{"pane_id":"w1:p2","text":"pix claude","keys":["Enter"]}`
and `{"pane_id":"w1:p2"}`. Check the key name Herdr uses for Enter in
`core/herdr/testdata` / existing `SendKeys` callers (`grep -rn '"Enter"\|"enter"' core/herdr core/agent`)
and use the same.

Commit — `herdr: send input to a pane and focus a pane (#461)`

### Task B2: Herdr host — run a command in a new tab

**Files:**
- Modify: `core/herdr/host.go`
- Test: `core/herdr/host_test.go` (reuse its fake server/transport setup)

```go
// RunInTab opens a tab labeled label in cwd with env and types argv into its shell (#461:
// a session you resume, which DeskPixie doesn't run as an agent). It waits out a shell that
// isn't at its prompt yet, like start. Returns the tab's pane.
func (h *Host) RunInTab(ctx context.Context, label, cwd string, env []agent.EnvVar, line string) (string, error)

// FocusPane brings pane to the front of the session; with no server running, ErrNoAgent.
func (h *Host) FocusPane(ctx context.Context, pane string) error
```

`RunInTab`: `client, workspace, err := h.client(ctx)`; `CreateTab` (retry once on
`IsWorkspaceNotFound` exactly as `start` does); then loop `SendInput(pane, line, ["Enter"])`
retrying on `IsPaneBusy || IsInputNotReady` until `h.paneBusyLimit`, sleeping `StartPoll`
(copy the loop shape from `start`). On final failure close the tab (`client.ClosePane`) and
return the error. `line` is built by the caller (`terminal.ShellLine`).

Tests: happy path sends `tab.create` with cwd/env/label then `pane.send_input`; a busy pane
retried then succeeds; a persistent failure closes the pane and returns the error.

Commit — `herdr: run a command line in a new tab; focus a pane (#461)`

### Task B3: the Resumer (decides; no Swift)

**Files:**
- Create: `core/pixhost/resume.go`
- Test: `core/pixhost/resume_test.go`

```go
// Tabs is the Herdr session your resumed sessions run in (#461): herdr.Host fits it.
type Tabs interface {
	RunInTab(ctx context.Context, label, cwd string, env []agent.EnvVar, line string) (string, error)
	FocusPane(ctx context.Context, pane string) error
	AttachCommand() []string
}

// Resumer resumes a past session in its own Herdr tab and plans showing it.
type Resumer struct {
	History interface {
		List() []HistoryEntry
		Update(id string, f func(*HistoryEntry))
	}
	Tabs    Tabs                  // nil: Herdr isn't available
	Planner agent.TerminalPlanner // the agent.terminal setting's
	Pix     string                // the app's own pix
	IsDir   func(string) bool
}

var (
	ErrFolderGone  = agent.LaunchError("That project folder is gone.")
	ErrNoHerdr     = agent.LaunchError("Resuming needs Herdr. Set it up in Settings ▸ Agents.")
	ErrUnresumable = agent.LaunchError("This conversation can't be resumed.")
	ErrNotInHistory = agent.LaunchError("That session is no longer in History.")
)

// Resume opens id's conversation in a new tab of the yours session, in its folder, and returns
// the plan that shows that tab.
func (r *Resumer) Resume(ctx context.Context, id string) (agent.FocusPlan, error)

// Show plans showing a resumed session's tab (the roster's click on a session whose Where is
// Herdr): focus its pane, then attach a terminal.
func (r *Resumer) Show(ctx context.Context, pane string) (agent.FocusPlan, error)
```

`Resume` order (each a test): entry missing → `ErrNotInHistory`; `Unresumable` →
`ErrUnresumable`; `Tabs == nil` → `ErrNoHerdr`; `!IsDir(cwd)` → `ErrFolderGone`; then
`line := terminal.ShellLine([]string{r.Pix, "claude", "--resume", id})`,
`env := []agent.EnvVar{{Key: "PIX_WHERE", Value: "Herdr"}}` (and nothing else — assert no
`PIXIE_AGENT_TOKEN`), label = description or the folder's base name; `pane, err :=
RunInTab(...)`; `History.Update(id, HerdrPane = pane)`; return `Show(ctx, pane)`.

`Show`: `FocusPane` (ignore its error: the attach still shows the session) then
`r.Planner.Plan(agent.Attach{Argv: r.Tabs.AttachCommand()})`.

Check `terminal.ShellLine` doesn't import `pixhost` (import cycle): `go list -deps
./agent/terminal | grep pixhost` must be empty. If `pixhost` can't import `agent/terminal`,
move `Resumer` to `core/app/agentapp/resume.go` instead — same code, same tests.

Fakes in the test: `fakeTabs` records calls, returns a pane `"w1:p7"`; planner =
`agenttest` fake terminal planner if one exists (`grep -n "TerminalPlanner" core/agent/agenttest/fakes.go`),
else a 5-line fake.

Commit — `pixhost: Resumer opens a past session in Herdr and plans showing it (#461)`

### Task B4: clicking a resumed session's row shows its Herdr tab

**Files:**
- Create: `core/app/agentapp/sessionplanner.go`
- Test: `core/app/agentapp/sessionplanner_test.go`
- Modify: `core/app/agentapp/agentapp.go:363` (`Sessions:` uses the wrapper)

```go
// herdrAwareSessions shows a session you resumed in Herdr by its pane (#461); any other the
// usual way, by its terminal app.
type herdrAwareSessions struct {
	inner   agent.SessionPlanner
	paneOf  func(pid int) (string, bool) // pixhost: the pid's session → its History HerdrPane
	resumer *pixhost.Resumer
}

func (p herdrAwareSessions) PlanSession(pid int) (agent.FocusPlan, error) {
	if pane, ok := p.paneOf(pid); ok && p.resumer != nil {
		return p.resumer.Show(context.Background(), pane)
	}
	return p.inner.PlanSession(pid)
}
```

Add `(*pixhost.Host).HerdrPaneOf(pid int) (string, bool)`: the session with `hello.Pid == pid`
whose `hello.Where == "Herdr"` and whose `sessionID` (or `resumes`) has a History entry with a
`HerdrPane`. Test it in `history_host_test.go`, and the wrapper with a fake inner planner
(pane known → resumer plan; unknown → inner).

Commit — `agentapp: a session resumed in Herdr is shown by its pane (#461)`

---

## Phase C — Go: wiring and the tray model

### Task C1: build the yours Herdr host, the store and the Resumer

**Files:**
- Modify: `core/app/agentapp/registry.go` (new `newYoursHerdr(d HostDeps) (*herdr.Host, error)`:
  same as `newHerdrHost` but session `YoursSession(d.Settings.Session)` and returning the raw
  `*herdr.Host`, no integration check)
- Modify: `core/app/agentapp/agentapp.go` (after `ph` is made: `ph.SetHistory(pixhost.NewHistoryFile(paths.PixHistory()), time.Now)`; build the Resumer; keep it on `Agent`)
- Modify: `core/app/agentapp/files.go` or wherever `Paths` lives (`grep -n "func (p Paths) Kept" core/app/agentapp/*.go`): add `PixHistory()` = `filepath.Join(p.Root, "pix-history.json")`
- Test: `core/app/agentapp/registry_test.go` / `wiring_test.go` (follow existing)

```go
// YoursSession is the Herdr session your resumed sessions run in (#461): never the
// coordinator's, so DeskPixie never reconciles, replaces or stops them.
func YoursSession(session string) string { return session + "-yours" }
```

Herdr unavailable or `SessionHost != "herdr"` is fine: `Tabs` stays nil and Resume says
`ErrNoHerdr`. Do **not** call `Shutdown` on quit in a way that stops its server (check
`herdr.Host.Shutdown` only stops the event pump — it does today).

Tests: `YoursSession("deskpixie") == "deskpixie-yours"`; with a fake herdr binary missing,
the Resumer has nil Tabs.

Commit — `agentapp: wire History, the yours Herdr session and the Resumer (#461)`

### Task C2: History rows in `app.state`; `agent:history:*` actions

**Files:**
- Modify: `core/app/app.go` (`AgentTray` gets `History []HistoryRow`)
- Modify: `core/app/tray.go` (`AgentState` gets `History []HistoryRef \`json:"history"\``;
  `HistoryItems(rows []HistoryRow, now time.Time) []HistoryRef`)
- Modify: `core/app/agentapp/agentapp.go` (`Tray()` fills History from `ph.History()`;
  `Menu()` handles the ids)
- Test: `core/app/tray_test.go`, `core/app/agentapp/agentapp_test.go`

```go
// HistoryRow is one past pix session for Tray ▸ History (#461).
type HistoryRow struct {
	ID, Description, Folder string // Folder: the git root's or cwd's base name
	LastActive              time.Time
	Unresumable             bool
}

// HistoryRef is one Tray ▸ History row: "login bug · deskpixie · 2h ago".
type HistoryRef struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// HistoryItems labels rows (description ≤ 40 chars, cut with "…"; no description: the folder
// alone) with how long ago they were active: "just now", "5 min ago", "2h ago", "yesterday",
// "3 days ago".
func HistoryItems(rows []HistoryRow, now time.Time) []HistoryRef
```

Tests for `HistoryItems`: each age bucket; long description cut; empty description; disabled
when Unresumable; nil → `[]` (never null — `AppState` lists always encode as `[]`).

Menu ids (in `Agent.Menu`, before the `agent:cancel:` default):
- `agent:history:resume:<id>` → `a.wg.Go`: `plan, err := a.resumer.Resume(a.ctx, id)`; on
  error `a.hooks.Notify("Couldn't resume", err.Error())` (LaunchError text is the sentence);
  else `a.hooks.Focus(plan)` (check the exact hook: `focusSink{c.Hooks}` → `c.Hooks.Focus`).
- `agent:history:remove:<id>` → `ph.RemoveHistory(id)`; `agent:history:clear` →
  `ph.ClearHistory()`; then `call(a.hooks.RefreshTray)`.

Tests: each id with a fake resumer seam (make `resumer` an interface field on `Agent` with
`Resume(ctx, id) (agent.FocusPlan, error)` for the test); an error notifies with the sentence;
success focuses the plan.

Commit — `app: Tray ▸ History rows and agent:history actions (#461)`

### Task C3: Settings extra row

**Files:**
- Modify: `core/settings/search.go` (`Extras`: add after `pix`:
  `{"pixHistory", SectionSessions, "Sessions you start with pix", "History", "Past Claude Code sessions you ran with pix. Resume one to open it in Herdr, in its project folder.", "", AllPlatforms}`)
- Test: existing extras/search tests (`grep -n "Extras" core/settings/*_test.go`) — add a search
  hit for "resume".

Commit — `settings: the History row on the Sessions page (#461)`

**Phase C checkpoint:** `cd core && gofmt -l . && go vet ./... && go test ./...` all clean.

---

## Phase D — Swift

### Task D1: decode History; the History submenu

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Tray/TrayMenu.swift`
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/TrayMenuTests.swift`

- `TrayAppState.Agent` (find its decoding, next to `jobs`): add
  `public var history: [HistoryRef]` decoded with `decodeIfPresent ?? []`, where
  `HistoryRef { id, label, enabled }`.
- In `build`, inside `if let agent = s.agent, agent.enabled`, after the Coordinator submenu:

```swift
items.append(.submenu(title: "History", enabled: true, items: historyItems(agent.history)))
```

```swift
/// Tray ▸ History (#461): past pix sessions, newest first; a click resumes one in Herdr.
public static func historyItems(_ rows: [TrayAppState.HistoryRef]) -> [TrayItem] {
    guard !rows.isEmpty else { return [.action(Row(id: "agent:history:none", title: "No past sessions", enabled: false))] }
    return rows.map { .action(Row(id: "agent:history:resume:\($0.id)", title: $0.label, enabled: $0.enabled)) }
        + [.separator, .action(Row(id: "agent:history:clear", title: "Clear History"))]
}
```

Tests: empty → one disabled "No past sessions"; two rows → two resume ids, separator, Clear;
disabled row stays disabled; `TrayMenu.route("agent:history:resume:x") == .core("agent:history:resume:x", focusCanvas: false)`;
decoding an `app.state` JSON without `history` gives `[]`. Update
`Parity/TrayLabelsAndRoutingTests.swift` if it snapshots the whole menu.

The right-click menu needs nothing: `TrayContextMenu` draws the same tree.

Commit — `mac: Tray ▸ History submenu, in the menu bar and Pixie's menu (#461)`

### Task D2: Settings ▸ Sessions ▸ History

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Settings/SettingsLayout.swift`
  (`Extra.pixHistory(help:)`, id `"pixHistory"`, label `"History"`, in `allExtras` after `.pix`)
- Modify: `mac/DeskPixie/Settings/SettingsWindowView.swift` (`extraRow` case `.pixHistory`)
- Test: the SettingsLayout tests (`grep -ln "allExtras\|extra(" mac/Packages/DeskPixieCore/Tests -r`):
  `extra("pixHistory")` exists and matches Go's label/help (there's likely a parity test).

The row lists `history` from the latest tray state (find how the window reaches app state:
`SettingsHub.model(for: appDelegate.composition)` — pass the composition's tray state through
the same way, or read `app.state` via the engine when the row appears). Each row: label,
**Resume** (`app.action agent:history:resume:<id>`), a trash button (`agent:history:remove:<id>`,
`.help("Remove from History")`); footer **Clear History**; empty: "No past sessions yet." in
`NoteText`. Use the existing `row(label:id:)` / `NoteText` helpers so it looks like the pix row.

Commit — `mac: History in Settings ▸ Sessions (#461)`

**Phase D checkpoint:** `(cd mac/Packages/DeskPixieCore && swift test)` and
`make -C mac build SIGNING_IDENTITY="Developer ID Application: …" DEVELOPMENT_TEAM=…` pass.

---

## Phase E — docs and end to end

### Task E1: docs

- `docs/pix.md`: a "Resuming a past session" section — where History is, that it opens in
  Herdr's `<session>-yours` session, stays yours (⌨), survives quitting DeskPixie, Claude only;
  a Troubleshooting row for "This conversation can't be resumed."
- Commit — `docs: resuming past pix sessions (#461)`

### Task E2: check it by hand (AGENTS.md "Checking it end to end")

1. Build signed; run isolated: `DESKPIXIE_SUPPORT_DIR=<scratch>`, its settings.json with its own
   `agent.session` (e.g. `dp-test-461`) and `agent.port`; binary run directly, stderr to a file.
2. In a terminal: `pix claude` with `DESKPIXIE_SUPPORT_DIR` pointing at the scratch app (check
   how pix finds the app's socket: `DaemonSocket(home, env)`), type a prompt, quit Claude.
3. Menu bar ▸ History shows it; right-click Pixie ▸ History shows it; Settings ▸ Sessions shows it.
4. Resume from each place: a terminal opens attached to `dp-test-461-yours`, Claude shows the
   old conversation, the roster lists it under ⌨ with "Herdr"; clicking its row brings that tab
   forward; Pixie can message it.
5. Verify Claude keeps the session id on `--resume` (the History entry isn't duplicated). If it
   forks a new id, the old entry must be gone (Task A3 case 5).
6. Quit the test app: the Herdr tab keeps running (`herdr --session dp-test-461-yours pane list`).
7. Error paths: rename the project folder → "That project folder is gone."; edit the history
   file to a bogus id → resume fails → next try says it can't be resumed.
8. Clean up: stop the app, `herdr session stop/delete` both test sessions, `rmdir` empty folders.

Record what was and wasn't checked for the PR description.

### Task E3: review, PR

- Full test run (AGENTS.md "Run before every push").
- Quick review of the diff by a second agent (superpowers:requesting-code-review); fix real
  findings.
- Push, open the PR (`Closes #461`, what changed, how it was tested, what wasn't), wait for CI.
  Merge only with the maintainer's go-ahead.
