# Agent Roster Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A chip under Pixie counts Pixie's agents (never the coordinator) and your reachable `pix` sessions; hovering lists them; clicking a row brings its terminal forward (#91).

**Architecture:** The Go runtime keeps a roster built from `CompositeHost.List` plus the jobs' worker rows, rebuilt (debounced 250 ms) on pane events, host operations and `pix` host changes, with a 30 s backstop, and pushes it as an `agents` event; the app reads `agent.roster` once at start. The Mac app draws it in a child panel of Pixie's panel, placed by a pure `ChipPlacement`, from a pure `AgentRosterModel`.

**Tech Stack:** Go 1.25 (`core/`), Swift 5.10 / SwiftUI / AppKit (macOS 14), XCTest, `testing/synctest`.

**Spec:** `docs/superpowers/specs/2026-10-15-agent-roster-design.md`

## Global Constraints

- The coordinator is **never counted**: listed first under Pixie's agents, but not in `agent_count`, the chip, or the section header.
- Chip text: `⚙ <agents> · ⌨ <yours>`; a zero side is left out; the chip hides when both are zero.
- Status words are `PaneStatus.String()`: `working`, `idle`, `blocked`, `done`, `unknown`. Dots: ● working, ◐ blocked, ○ idle or done, · unknown.
- An unreachable session is listed greyed and not counted.
- At most 8 rows per section, then "+N more" (not clickable).
- JSON field names are snake_case (`agent_count`, `your_count`).
- Older `pix` binaries send no `app`: their rows are not clickable. `wire.Version` does not change.
- Commit trailer on every commit:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw
  ```
- Work only in `/Users/daniel/Documents/Projects/deskpixie/.worktrees/agent-roster` (branch `agent-roster`).

## Review Focus

1. A `pix` session that connects while nothing else happens (no pane event): the chip must update within ~1 s (pinned by the `Changes()` tests in Task 3 and the runtime test in Task 4).
2. Only the coordinator running: no chip at all (Task 1 counts 0/0; Task 6 `visible` false).
3. `Host.List` failing (Herdr restarting): the last roster stays, nothing is emitted, no panic (Task 4 test with `FailList`).
4. The open list must not flip above/below under the pointer when it opens near the bottom edge (Task 6 `openHeight` test).
5. A session row whose app quit, or with no bundle id: clicking does nothing, no crash (Task 6 `clickable`; Task 7 guards `first`).

---

### Task 1: Build the roster (Go, pure)

**Files:**
- Create: `core/agent/roster.go`
- Test: `core/agent/roster_test.go`
- Modify: `core/agent/types.go` (`UserSession` gains `App`)

**Interfaces:**
- Produces: `type RosterRow struct{Name, Label, Detail, Status, Kind string; Reachable bool; App string}`, `type RosterView struct{Agents, Yours []RosterRow; AgentCount, YourCount int}`, `func EmptyRoster() RosterView`, `func BuildRoster(list []AgentInfo, workers []WorkerRow, hostName string) RosterView`; `UserSession.App string`.

- [ ] **Step 1: Add `App` to `UserSession`** (`core/agent/types.go`, after `Where`):

```go
	Where            string // terminal or IDE, e.g. "WebStorm"
	App              string // the terminal app's bundle id, e.g. "com.jetbrains.WebStorm" ("" = unknown)
```

- [ ] **Step 2: Write the failing test** (`core/agent/roster_test.go`):

```go
package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

func TestRosterCoordinatorOnlyIsListedButNotCounted(t *testing.T) {
	v := BuildRoster([]AgentInfo{{Name: Coordinator, Status: PaneIdle}}, nil, "herdr")
	if v.AgentCount != 0 || v.YourCount != 0 || len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" ||
		v.Agents[0].Label != "Coordinator" || v.Agents[0].Status != "idle" || len(v.Yours) != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestRosterWorkersPanesAndSessions(t *testing.T) {
	list := []AgentInfo{
		{Name: "deskpixie-claude", Status: PaneWorking},
		{Name: Coordinator, Status: PaneWorking},
		{Name: "old-claude", Status: PaneBlocked},
		{Name: "focus-claude", Status: PaneIdle, Origin: OriginUser, User: &UserSession{Tool: "claude", Repo: "focus", Where: "WebStorm", App: "com.jetbrains.WebStorm", Description: "login bug", Reachable: true}},
		{Name: "api-codex", Status: PaneWorking, Origin: OriginUser, User: &UserSession{Tool: "codex", Repo: "api", Where: "iTerm", Reachable: true}},
		{Name: "repo-claude", Status: PaneIdle, Origin: OriginUser, User: &UserSession{Tool: "claude", Where: "terminal", Reachable: false}},
	}
	workers := []WorkerRow{{Name: "deskpixie-claude", Repo: "deskpixie"}}
	v := BuildRoster(list, workers, "herdr")
	b, _ := json.Marshal(v)
	want := `{"agents":[` +
		`{"name":"coordinator","label":"Coordinator","status":"working","kind":"coordinator","reachable":true},` +
		`{"name":"deskpixie-claude","label":"deskpixie-claude","detail":"deskpixie","status":"working","kind":"worker","reachable":true},` +
		`{"name":"old-claude","label":"old-claude","detail":"herdr","status":"blocked","kind":"pane","reachable":true}],` +
		`"yours":[` +
		`{"name":"focus-claude","label":"login bug","detail":"WebStorm","status":"idle","kind":"session","reachable":true,"app":"com.jetbrains.WebStorm"},` +
		`{"name":"api-codex","label":"codex in api","detail":"iTerm","status":"working","kind":"session","reachable":true},` +
		`{"name":"repo-claude","label":"claude","detail":"terminal","status":"idle","kind":"session","reachable":false}],` +
		`"agent_count":2,"your_count":2}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestRosterEmptyIsArraysNotNull(t *testing.T) {
	b, _ := json.Marshal(BuildRoster(nil, nil, "tmux"))
	if string(b) != `{"agents":[],"yours":[],"agent_count":0,"your_count":0}` {
		t.Fatal(string(b))
	}
	if b, _ := json.Marshal(EmptyRoster()); string(b) != `{"agents":[],"yours":[],"agent_count":0,"your_count":0}` {
		t.Fatal(string(b))
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go -C core test ./agent/ -run Roster`
Expected: FAIL (build error: `undefined: BuildRoster`)

- [ ] **Step 4: Implement** (`core/agent/roster.go`):

```go
package agent

// The roster beside Pixie (#91): Pixie's agents in Herdr or tmux and the sessions you started
// with pix, with counts for the chip. The coordinator is listed first but never counted.

// RosterRow is one agent in the roster.
type RosterRow struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status"`
	// Kind: coordinator | worker | pane (in Pixie's session, no job owns it) | session (yours).
	Kind      string `json:"kind"`
	Reachable bool   `json:"reachable"`
	// App is a session's terminal app bundle id ("" = unknown: the row isn't clickable).
	App string `json:"app,omitempty"`
}

// RosterView is the whole roster. AgentCount excludes the coordinator; YourCount counts only
// reachable sessions.
type RosterView struct {
	Agents     []RosterRow `json:"agents"`
	Yours      []RosterRow `json:"yours"`
	AgentCount int         `json:"agent_count"`
	YourCount  int         `json:"your_count"`
}

// EmptyRoster has empty lists (never null in JSON).
func EmptyRoster() RosterView { return RosterView{Agents: []RosterRow{}, Yours: []RosterRow{}} }

// BuildRoster turns the combined host list into the roster; workers (the jobs' worker rows)
// tell workers from other panes; hostName ("herdr", "tmux") is the detail of those other panes.
func BuildRoster(list []AgentInfo, workers []WorkerRow, hostName string) RosterView {
	v := EmptyRoster()
	repos := map[string]string{}
	for _, w := range workers {
		repos[w.Name] = w.Repo
	}
	var coordinator []RosterRow
	for _, a := range list {
		switch {
		case a.Origin == OriginUser:
			if a.User == nil {
				continue
			}
			u := a.User
			label := u.Description
			if label == "" && u.Repo != "" {
				label = u.Tool + " in " + u.Repo
			}
			if label == "" {
				label = u.Tool
			}
			v.Yours = append(v.Yours, RosterRow{Name: a.Name, Label: label, Detail: u.Where, Status: a.Status.String(),
				Kind: "session", Reachable: u.Reachable, App: u.App})
			if u.Reachable {
				v.YourCount++
			}
		case a.Name == Coordinator:
			coordinator = []RosterRow{{Name: a.Name, Label: "Coordinator", Status: a.Status.String(), Kind: "coordinator", Reachable: true}}
		default:
			row := RosterRow{Name: a.Name, Label: a.Name, Status: a.Status.String(), Kind: "pane", Detail: hostName, Reachable: true}
			if repo, ok := repos[a.Name]; ok {
				row.Kind, row.Detail = "worker", repo
			}
			v.Agents = append(v.Agents, row)
			v.AgentCount++
		}
	}
	v.Agents = append(coordinator, v.Agents...)
	if v.Agents == nil {
		v.Agents = []RosterRow{}
	}
	return v
}
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go -C core test ./agent/ -run Roster`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
git add core/agent/roster.go core/agent/roster_test.go core/agent/types.go
git commit -m "agent: build the roster beside Pixie; the coordinator is listed, never counted (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 2: `pix` reports its terminal app's bundle id

**Files:**
- Modify: `core/pix/where.go` (add `AppID`), `core/pix/wire/wire.go` (`Msg.App`), `core/pix/session.go:59,299-300,369` (option + hello), `core/pixhost/host.go:399` (`List` maps `App`)
- Test: `core/pix/launch_test.go`, `core/pixhost/host_test.go`, `core/pixhost/e2e_test.go:130-132`

**Interfaces:**
- Consumes: `agent.UserSession.App` (Task 1).
- Produces: `func AppID(env func(string) string) string`; `wire.Msg.App string` (`json:"app,omitempty"`).

- [ ] **Step 1: Write the failing tests**

Append to `core/pix/launch_test.go`:

```go
func TestAppIDIsTheInheritedBundleID(t *testing.T) {
	if got := AppID(envOf(map[string]string{"__CFBundleIdentifier": "com.jetbrains.WebStorm"})); got != "com.jetbrains.WebStorm" {
		t.Fatal(got)
	}
	if got := AppID(envOf(nil)); got != "" {
		t.Fatal(got)
	}
}
```

(Check `envOf`'s parameter type at the top of `launch_test.go`; if it takes a `map[string]string`, this compiles as written; if it takes pairs, pass `"__CFBundleIdentifier", "com.jetbrains.WebStorm"`.)

Append to `core/pixhost/host_test.go`:

```go
func TestRegisterCarriesTheAppBundleID(t *testing.T) {
	h, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Where: "iTerm", App: "com.googlecode.iterm2"})
	if l := listed(t, h); len(l) != 1 || l[0].User.App != "com.googlecode.iterm2" {
		t.Fatalf("%+v", l[0].User)
	}
}
```

In `core/pixhost/e2e_test.go`, extend the assertion at lines 130-131 with `|| info.User.App != "com.jetbrains.WebStorm"`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./pix/ ./pixhost/ -run 'AppID|AppBundle|EndToEnd'`
Expected: FAIL (build errors: `undefined: AppID`, `unknown field App in struct literal of type wire.Msg`)

- [ ] **Step 3: Implement**

`core/pix/where.go`, after `Where`:

```go
// AppID is the bundle id of the app pix runs in: macOS sets __CFBundleIdentifier for an app and
// its terminals inherit it (WebStorm's terminal: com.jetbrains.WebStorm). "" when unknown.
func AppID(env func(string) string) string { return env("__CFBundleIdentifier") }
```

`core/pix/wire/wire.go`, after the `Where` field:

```go
	App         string   `json:"app,omitempty"`
```

`core/pix/session.go`: add `app` to `sessionOpts` (`tool, description, daemonSock, home, where, app string`), set it at line 369 (`where: Where(env), app: AppID(env),`), and send it in the hello at line 300 (`Where: s.o.where, App: s.o.app, Description: s.o.description}`).

`core/pixhost/host.go:399`: `User: &agent.UserSession{Tool: s.hello.Tool, Repo: repo, Where: s.hello.Where, App: s.hello.App,`

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test ./pix/... ./pixhost/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add core/pix core/pixhost
git commit -m "pix: report the terminal app's bundle id, so its row beside Pixie can bring it forward (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 3: The `pix` host says when its list changed

**Files:**
- Modify: `core/agent/composite.go` (`ChangeNotifier`, `CompositeHost.Changes`), `core/pixhost/host.go` (`changes` channel, `changed()`, `Changes()`; call sites at register, drop, descriptions, `broadcast`)
- Test: `core/pixhost/host_test.go`, `core/agent/composite_test.go`

**Interfaces:**
- Produces: `type ChangeNotifier interface{ Changes() <-chan struct{} }` (package `agent`); `func (c *CompositeHost) Changes() <-chan struct{}` (nil when the secondary isn't a `ChangeNotifier`); `func (h *pixhost.Host) Changes() <-chan struct{}`.

- [ ] **Step 1: Write the failing tests**

Append to `core/pixhost/host_test.go`:

```go
// changedSoon waits for a change signal.
func changedSoon(t *testing.T, h *Host, what string) {
	t.Helper()
	select {
	case <-h.Changes():
	case <-time.After(time.Second):
		t.Fatal("no change signal: " + what)
	}
}

func TestChangesOnRegisterDescriptionDropAndExit(t *testing.T) {
	h, sock := newHost(t)
	h.unreachableFor = 100 * time.Millisecond
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	changedSoon(t, h, "register")
	if err := h.SetDescription(r.Address, "uploads"); err != nil {
		t.Fatal(err)
	}
	changedSoon(t, h, "description")
	p.c.Close()
	changedSoon(t, h, "unreachable")
	changedSoon(t, h, "dropped")
}
```

Append to `core/agent/composite_test.go`:

```go
type notifyingUser struct {
	userHost
	ch chan struct{}
}

func (n notifyingUser) Changes() <-chan struct{} { return n.ch }

func TestCompositeForwardsTheSecondarysChanges(t *testing.T) {
	_, _, user := newComposite(t)
	if agent.NewCompositeHost(agenttest.NewFakeSessionHost(), user).Changes() != nil {
		t.Fatal("a secondary without changes gives nil")
	}
	n := notifyingUser{user, make(chan struct{}, 1)}
	c := agent.NewCompositeHost(agenttest.NewFakeSessionHost(), n)
	n.ch <- struct{}{}
	select {
	case <-c.Changes():
	case <-time.After(time.Second):
		t.Fatal("not forwarded")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./pixhost/ ./agent/ -run 'Changes'`
Expected: FAIL (build errors: `h.Changes undefined`, `c.Changes undefined`)

- [ ] **Step 3: Implement**

`core/agent/composite.go`, after `SecondaryHost`:

```go
// ChangeNotifier is a host whose list can change without a pane event (a pix session
// connected, dropped or was described): Changes gets a signal then (coalesced, never blocks).
type ChangeNotifier interface {
	Changes() <-chan struct{}
}

// Changes is the secondary's change signal; nil when it has none.
func (c *CompositeHost) Changes() <-chan struct{} {
	if n, ok := c.secondary.(ChangeNotifier); ok {
		return n.Changes()
	}
	return nil
}
```

`core/pixhost/host.go`: add `changes chan struct{}` to `Host`; in `New` set `changes: make(chan struct{}, 1)`; add:

```go
// Changes signals that List may have changed (coalesced: one pending signal at most).
func (h *Host) Changes() <-chan struct{} { return h.changes }

func (h *Host) changed() {
	select {
	case h.changes <- struct{}{}:
	default:
	}
}
```

Call `h.changed()`:
- in the connection handler right after `s, reason := h.register(hello, c)` succeeds (line 125, when `s != nil`);
- after a description is set from a pix event (lines 216-218) and at the end of `SetDescription` (line 355) — after `h.mu` is released;
- right after `s.conn = nil` (line 259) once the lock is released (unreachable);
- at the top of `broadcast` (covers status and exit).

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test -race ./pixhost/ ./agent/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add core/agent/composite.go core/agent/composite_test.go core/pixhost/host.go core/pixhost/host_test.go
git commit -m "pixhost: signal list changes a pane event doesn't report; the composite forwards them (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 4: The runtime keeps the roster and pushes changes

**Files:**
- Modify: `core/agent/traits.go` (`RosterSink`), `core/agent/runtime.go` (`Deps.Roster`, handle fields, `Run`, `runOp`/`runWorkerOp` pokes, `Roster()`, `FocusAgent`), `core/agent/controller.go` or `bubble.go` (`WorkerRows()` accessor)
- Modify: `core/agent/agenttest/fakes.go` (`RecordingRoster`)
- Test: `core/agent/runtime_roster_test.go`

**Interfaces:**
- Consumes: `BuildRoster`, `EmptyRoster`, `RosterView` (Task 1); `ChangeNotifier` (Task 3).
- Produces: `type RosterSink interface{ Roster(v RosterView) }`; `Deps.Roster RosterSink`; `func (h *AgentHandle) Roster() RosterView`; `func (h *AgentHandle) FocusAgent(ctx context.Context, name string) error`; `const RosterDebounce = 250 * time.Millisecond`, `const RosterBackstop = 30 * time.Second`; `agenttest.RecordingRoster{}` with `Views() []agent.RosterView`.

- [ ] **Step 1: Add the fake** (`core/agent/agenttest/fakes.go`, after `RecordingUpdates`):

```go
// RecordingRoster records every roster sent beside Pixie.
type RecordingRoster struct {
	mu    sync.Mutex
	views []agent.RosterView
}

func (r *RecordingRoster) Roster(v agent.RosterView) {
	r.mu.Lock()
	r.views = append(r.views, v)
	r.mu.Unlock()
}

func (r *RecordingRoster) Views() []agent.RosterView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.views)
}
```

- [ ] **Step 2: Write the failing tests** (`core/agent/runtime_roster_test.go`):

```go
package agent_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// rosterRig is a runtime on a composite host: a fake primary and a fake pix host whose
// sessions list as yours. warm starts the coordinator at once.
type rosterRig struct {
	h      *AgentHandle
	host   *agenttest.FakeSessionHost
	user   *pixFake
	roster *agenttest.RecordingRoster
}

type pixFake struct {
	*agenttest.FakeSessionHost
	ch chan struct{}
}

func (p *pixFake) Owns(n string) bool             { _, ok := p.StatusOf(n); return ok }
func (p *pixFake) Changes() <-chan struct{}       { return p.ch }
func (p *pixFake) List(ctx context.Context) ([]AgentInfo, error) {
	l, err := p.FakeSessionHost.List(ctx)
	for i := range l {
		l[i].Origin = OriginUser
		l[i].User = &UserSession{Tool: "claude", Repo: "api", Where: "iTerm", App: "com.googlecode.iterm2", Reachable: true}
	}
	return l, err
}

// connect adds one of your sessions without a pane event, then signals a change.
func (p *pixFake) connect(t *testing.T, name string) {
	if _, err := p.StartAgent(context.Background(), AgentSpec{Name: name}); err != nil {
		t.Fatal(err)
	}
	select {
	case p.ch <- struct{}{}:
	default:
	}
}

func newRosterRig(t *testing.T, warm bool) *rosterRig {
	host := agenttest.NewFakeSessionHost().ManualTurns()
	user := &pixFake{agenttest.NewFakeSessionHost(), make(chan struct{}, 1)}
	r := &rosterRig{host: host, user: user, roster: &agenttest.RecordingRoster{}}
	clock := agenttest.ClockAt(1_000_000)
	deps := Deps{
		Host: NewAgentHost(NewCompositeHost(host, user), clock, CoordinatorRecipe{Cwd: "/data/agent/workspace"}),
		Voice: &agenttest.RecordingVoiceLink{}, Notifier: &agenttest.RecordingNotifier{}, Pixie: &agenttest.RecordingNotifier{},
		Status: &agenttest.RecordingStatus{}, Launcher: &agenttest.FakeLauncher{}, TerminalDir: "/home/me/Projects",
		Store: &agenttest.MemStore{}, Clock: clock, IDs: &agenttest.SeqIds{}, Updates: &agenttest.RecordingUpdates{},
		Roster: r.roster,
	}
	r.h = NewRuntime(deps, RuntimeConfig{Settings: DefaultSettings(), Warm: warm})
	go r.h.Run(t.Context())
	return r
}

func lastRoster(t *testing.T, r *rosterRig) RosterView {
	t.Helper()
	v := r.roster.Views()
	if len(v) == 0 {
		t.Fatal("no roster sent")
	}
	return v[len(v)-1]
}

func TestTheRosterListsTheCoordinatorWithoutCountingIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		v := lastRoster(t, r)
		if v.AgentCount != 0 || len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" {
			t.Fatalf("%+v", v)
		}
		if got := r.h.Roster(); got.AgentCount != 0 || len(got.Agents) != 1 {
			t.Fatalf("Roster is the last view: %+v", got)
		}
	})
}

func TestASessionConnectingWithoutAPaneEventUpdatesTheRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		v := lastRoster(t, r)
		if v.YourCount != 1 || len(v.Yours) != 1 || v.Yours[0].App != "com.googlecode.iterm2" {
			t.Fatalf("%+v", v)
		}
	})
}

func TestAStatusChangeResendsButAnUnchangedListDoesNot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		n := len(r.roster.Views())
		r.host.SetStatus(Coordinator, PaneWorking)
		time.Sleep(time.Second)
		if v := r.roster.Views(); len(v) != n+1 || v[n].Agents[0].Status != "working" {
			t.Fatalf("%+v", v)
		}
		time.Sleep(RosterBackstop + time.Second)
		if len(r.roster.Views()) != n+1 {
			t.Fatal("the backstop rebuild of an unchanged list sends nothing")
		}
	})
}

func TestAFailingListKeepsTheLastRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		n := len(r.roster.Views())
		r.host.FailList(true)
		r.host.SetStatus(Coordinator, PaneWorking)
		time.Sleep(time.Second)
		if len(r.roster.Views()) != n || r.h.Roster().Agents[0].Status != "idle" {
			t.Fatalf("%+v", r.roster.Views())
		}
	})
}

func TestFocusAgentFocusesThatPaneAndAttaches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		if err := r.h.FocusAgent(context.Background(), Coordinator); err != nil {
			t.Fatal(err)
		}
	})
}
```

Before running, check the fake's initial coordinator status in `agenttest/fakes.go` `StartAgent`: if it starts panes `PaneIdle`, the assertions above hold as written; if it starts them `PaneWorking`, swap `idle`/`working` in the last two tests. Extend `TestFocusAgentFocusesThatPaneAndAttaches` to assert the focus and the attach the way `TestTheCoordinatorIsFocusedWhenItStartsAndWhenTheTerminalOpens` (`runtime_test.go:295`) does, using the same fake accessors it uses.

- [ ] **Step 3: Run them to verify they fail**

Run: `go -C core test ./agent/ -run 'Roster|FocusAgent'`
Expected: FAIL (build errors: `unknown field Roster in struct literal of type Deps`, `r.h.Roster undefined`, `r.h.FocusAgent undefined`, `undefined: RosterBackstop`)

- [ ] **Step 4: Implement**

`core/agent/traits.go`, after `UpdatesSink`:

```go
// RosterSink gets the roster beside Pixie whenever it changes (#91).
type RosterSink interface {
	Roster(v RosterView)
}
```

`core/agent/runtime.go` `Deps`, after `Updates`:

```go
	// Roster gets the roster beside Pixie on every change (nil: no roster is kept).
	Roster RosterSink
```

Accessor (in `core/agent/bubble.go`, next to `FocusTarget`):

```go
// WorkerRows are the workers of open jobs and any still running (list_workers' rows).
func (c *AgentController) WorkerRows() []WorkerRow { return c.jobs.WorkerRows() }
```

`AgentHandle` fields (next to `shuttingDown`):

```go
	// The roster beside Pixie (#91): rosterPoke asks for a rebuild; the last view sent.
	rosterPoke chan struct{}
	rosterMu   sync.Mutex
	rosterLast RosterView
	rosterSent bool
```

In `NewRuntimeWithWorkers`'s literal: `rosterPoke: make(chan struct{}, 1),`.

New code in `runtime.go` (after `FocusJob`):

```go
// RosterDebounce coalesces a burst of changes into one rebuild; RosterBackstop rebuilds anyway,
// for a change no event reported.
const (
	RosterDebounce = 250 * time.Millisecond
	RosterBackstop = 30 * time.Second
)

// Roster is the last roster sent (empty before the first), for the shell's first read.
func (h *AgentHandle) Roster() RosterView {
	h.rosterMu.Lock()
	defer h.rosterMu.Unlock()
	if !h.rosterSent {
		return EmptyRoster()
	}
	return h.rosterLast
}

// FocusAgent is a click on one of Pixie's agents in the roster: bring its pane forward, then
// attach a terminal. Your own sessions are not focused here (the app activates their terminal).
func (h *AgentHandle) FocusAgent(ctx context.Context, name string) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.Input(EnsureCoordinatorInput())
	focusQuietly(ctx, h.deps.Host.Session(), name)
	return h.deps.Launcher.Open(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
}

func (h *AgentHandle) pokeRoster() {
	select {
	case h.rosterPoke <- struct{}{}:
	default:
	}
}

// runRoster rebuilds the roster after a poke or a host change (debounced), and on the backstop.
func (h *AgentHandle) runRoster() {
	if h.deps.Roster == nil {
		return
	}
	var changes <-chan struct{}
	if n, ok := h.deps.Host.Session().(ChangeNotifier); ok {
		changes = n.Changes()
	}
	back := time.NewTicker(RosterBackstop)
	defer back.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.rosterPoke:
		case <-changes:
		case <-back.C:
		}
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(RosterDebounce):
		}
		select { // a poke during the wait is covered by this rebuild
		case <-h.rosterPoke:
		default:
		}
		h.rebuildRoster()
	}
}

func (h *AgentHandle) rebuildRoster() {
	if h.quitting() {
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	session := h.deps.Host.Session()
	list, err := session.List(ctx)
	if err != nil {
		log.Printf("agent: roster: %v", err)
		return
	}
	h.ctlMu.Lock()
	workers := h.ctl.WorkerRows()
	h.ctlMu.Unlock()
	v := BuildRoster(list, workers, session.Name())
	h.rosterMu.Lock()
	if h.rosterSent && reflect.DeepEqual(v, h.rosterLast) {
		h.rosterMu.Unlock()
		return
	}
	h.rosterLast, h.rosterSent = v, true
	h.rosterMu.Unlock()
	h.deps.Roster.Roster(v)
}
```

(Add `reflect` to the imports; `log` is already used by `runtime.go`.)

In `Run`: after `go h.recheck()` add `go h.runRoster()` and `h.pokeRoster()`; in the `case ev, ok := <-panes:` branch, after `h.Input(PaneInput(ev))`, add `h.pokeRoster()`. At the end of `runOp` and of `runWorkerOp` (both run every host start/stop), add `h.pokeRoster()` (use `defer h.pokeRoster()` as their first line).

- [ ] **Step 5: Run them to verify they pass**

Run: `go -C core test -race ./agent/...`
Expected: PASS (all agent tests, including the five new ones)

- [ ] **Step 6: Commit**

```bash
git add core/agent
git commit -m "agent: keep the roster beside Pixie and send it on change; FocusAgent (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 5: The app carries the roster (`agents` event, `agent.roster`, `agent.focusAgent`)

**Files:**
- Modify: `core/app/agent.go` (`AgentHooks.Roster`, `Agent.Roster`, `Agent.FocusAgent`), `core/app/app.go:220-227,1108-1140` (hook, two calls), `core/app/agentapp/desktop.go` (`rosterSink`), `core/app/agentapp/agentapp.go:310,440-460` (deps, methods)
- Test: `core/app/agent_test.go` (fake + tests)

**Interfaces:**
- Consumes: `AgentHandle.Roster()`, `AgentHandle.FocusAgent` (Task 4).
- Produces: event `{"kind":"agents","roster":<RosterView>}`; call `agent.roster` → `{"roster":<RosterView>}`; call `agent.focusAgent {"name":…}` → `{}`.

- [ ] **Step 1: Write the failing tests** (`core/app/agent_test.go`)

Add to `fakeAgent`: fields `roster any` and `focusedAgents []string`, and methods:

```go
func (f *fakeAgent) Roster() any { f.mu.Lock(); defer f.mu.Unlock(); return f.roster }
func (f *fakeAgent) FocusAgent(name string) {
	f.mu.Lock()
	f.focusedAgents = append(f.focusedAgents, name)
	f.mu.Unlock()
}
```

Tests:

```go
func TestTheRosterHookEmitsAnAgentsEvent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	f.hooks.Roster(map[string]any{"agent_count": 1})
	e := h.waitFor("agents", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"kind":"agents","roster":{"agent_count":1}}` {
		t.Fatal(string(b))
	}
}

func TestAgentRosterAndFocusAgentReachTheAgent(t *testing.T) {
	f := &fakeAgent{roster: map[string]any{"your_count": 2}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.roster", nil); string(out) != `{"roster":{"your_count":2}}` {
		t.Fatal(string(out))
	}
	h.call("agent.focusAgent", map[string]any{"name": "deskpixie-claude"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.focusedAgents, []string{"deskpixie-claude"}) {
		t.Fatal(f.focusedAgents)
	}
}

func TestAgentRosterIsEmptyWithoutAnAgent(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	if out := h.call("agent.roster", nil); string(out) != `{"roster":{"agents":[],"yours":[],"agent_count":0,"your_count":0}}` {
		t.Fatal(string(out))
	}
	if out := h.call("agent.focusAgent", map[string]any{"name": "x"}); string(out) != "{}" {
		t.Fatal(string(out))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C core test ./app/ -run 'Roster|FocusAgent'`
Expected: FAIL (build error: `f.hooks.Roster undefined`)

- [ ] **Step 3: Implement**

`core/app/agent.go`: in `AgentHooks` after `Updates`:

```go
	// Roster is the roster beside Pixie (the `agents` event's roster), on every change (#91).
	Roster func(view any)
```

In `Agent` after `FocusJob`:

```go
	// Roster is the roster beside Pixie as it is now, for the shell's first read.
	Roster() any
	// FocusAgent is a click on one of Pixie's agents in the roster: its pane in a terminal (async).
	FocusAgent(name string)
```

`core/app/app.go`, in the `AgentHooks{…}` literal: `Roster: func(view any) { a.emit("agents", map[string]any{"roster": view}) },`

After the `agent.focusJob` registration:

```go
	r.Register("agent.roster", func(json.RawMessage) (any, error) {
		var view any = map[string]any{"agents": []any{}, "yours": []any{}, "agent_count": 0, "your_count": 0}
		if a.agent != nil {
			view = a.agent.Roster()
		}
		return map[string]any{"roster": view}, nil
	})
	r.Register("agent.focusAgent", func(args json.RawMessage) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decodeArgs("agent.focusAgent", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil && in.Name != "" {
			a.agent.FocusAgent(in.Name)
		}
		return struct{}{}, nil
	})
```

`core/app/agentapp/desktop.go`, after `updatesSink`:

```go
type rosterSink struct{ hooks app.AgentHooks }

func (r rosterSink) Roster(v agent.RosterView) {
	if r.hooks.Roster != nil {
		r.hooks.Roster(v)
	}
}
```

`core/app/agentapp/agentapp.go`: in `Deps` add `Roster: rosterSink{c.Hooks},`; after `FocusJob` add:

```go
// Roster is the roster beside Pixie (agent.RosterView).
func (a *Agent) Roster() any { return a.handle.Roster() }

// FocusAgent is a click on one of Pixie's agents in the roster (async). A failure becomes the
// tray status line, as for FocusJob.
func (a *Agent) FocusAgent(name string) {
	if a.ctx.Err() != nil {
		return // quitting
	}
	a.wg.Go(func() {
		if err := a.handle.FocusAgent(a.ctx, name); err != nil {
			log.Printf("agent: focus agent: %v", err)
			a.setStatus(TerminalFailureStatus(err.Error()))
		}
	})
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go -C core test -race ./...`
Expected: PASS (all packages)

- [ ] **Step 5: Commit**

```bash
git add core/app
git commit -m "app: agents event, agent.roster and agent.focusAgent for the roster beside Pixie (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 6: Roster model, placement and event decoding (Swift, pure)

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/AgentRosterModel.swift`, `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/ChipPlacement.swift`
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift` (`.agents`)
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/AgentRosterModelTests.swift`, `.../ChipPlacementTests.swift`, `.../EngineEventTests.swift`

**Interfaces:**
- Consumes: the `agents` event and `agent.roster` reply JSON (Task 5).
- Produces: `RosterRow`, `RosterView` (Decodable, `RosterView.empty`), `EngineEvent.agents(RosterView)`, `AgentRosterModel` (`apply(_:)`, `open`, `visible`, `chipText`, `sections`, `static clickable(_:)`, `static dot(_:)`, `static maxRows = 8`), `ChipPlacement.frame(pixie:panel:screen:openHeight:gap:) -> (CGRect, below: Bool)`.

- [ ] **Step 1: Write the failing tests**

`AgentRosterModelTests.swift`:

```swift
import XCTest
@testable import DeskPixieCore

final class AgentRosterModelTests: XCTestCase {
    private func row(_ name: String, kind: String = "worker", status: String = "idle",
                     reachable: Bool = true, app: String? = nil) -> RosterRow {
        RosterRow(name: name, label: name, detail: nil, status: status, kind: kind, reachable: reachable, app: app)
    }

    private func view(agents: [RosterRow], yours: [RosterRow]) -> RosterView {
        RosterView(agents: agents, yours: yours,
                   agentCount: agents.filter { $0.kind != "coordinator" }.count,
                   yourCount: yours.filter(\.reachable).count)
    }

    func testCoordinatorAloneHidesTheChip() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("coordinator", kind: "coordinator")], yours: []))
        XCTAssertFalse(m.visible)
    }

    func testChipTextLeavesOutAZeroSide() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("coordinator", kind: "coordinator"), row("a"), row("b"), row("c")], yours: []))
        XCTAssertEqual(m.chipText, "⚙ 3")
        m.apply(view(agents: [row("coordinator", kind: "coordinator")], yours: [row("y", kind: "session")]))
        XCTAssertEqual(m.chipText, "⌨ 1")
        m.apply(view(agents: [row("coordinator", kind: "coordinator"), row("a")],
                     yours: [row("y", kind: "session"), row("z", kind: "session")]))
        XCTAssertEqual(m.chipText, "⚙ 1 · ⌨ 2")
    }

    func testSectionsCountWithoutTheCoordinatorAndCapRows() {
        var m = AgentRosterModel()
        let workers = (1...10).map { row("w\($0)") }
        m.apply(view(agents: [row("coordinator", kind: "coordinator")] + workers, yours: [row("y", kind: "session")]))
        let s = m.sections
        XCTAssertEqual(s.map(\.title), ["Pixie's agents (10)", "Your sessions (1)"])
        XCTAssertEqual(s[0].rows.first?.kind, "coordinator")
        XCTAssertEqual(s[0].rows.count, AgentRosterModel.maxRows)
        XCTAssertEqual(s[0].more, 11 - AgentRosterModel.maxRows)
        XCTAssertEqual(s[1].more, 0)
    }

    func testGoingEmptyClosesTheList() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("a")], yours: []))
        m.open = true
        m.apply(view(agents: [], yours: []))
        XCTAssertFalse(m.open)
    }

    func testClickable() {
        XCTAssertTrue(AgentRosterModel.clickable(row("a")))
        XCTAssertTrue(AgentRosterModel.clickable(row("y", kind: "session", app: "com.googlecode.iterm2")))
        XCTAssertFalse(AgentRosterModel.clickable(row("y", kind: "session")))
        XCTAssertFalse(AgentRosterModel.clickable(row("y", kind: "session", reachable: false, app: "com.apple.Terminal")))
    }

    func testDots() {
        XCTAssertEqual(["working", "blocked", "idle", "done", "unknown"].map(AgentRosterModel.dot), ["●", "◐", "○", "○", "·"])
    }
}
```

`ChipPlacementTests.swift`:

```swift
import CoreGraphics
import XCTest
@testable import DeskPixieCore

final class ChipPlacementTests: XCTestCase {
    private let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)
    private let pixie = CGRect(x: 100, y: 100, width: 128, height: 128)

    func testCentredUnderPixie() {
        let (f, below) = ChipPlacement.frame(pixie: pixie, panel: CGSize(width: 60, height: 20), screen: screen, openHeight: 200)
        XCTAssertTrue(below)
        XCTAssertEqual(f, CGRect(x: 134, y: 232, width: 60, height: 20))
    }

    func testFlipsAboveWhenTheOpenListWouldNotFitBelow() {
        let low = CGRect(x: 100, y: 700, width: 128, height: 128)
        let (chip, below) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 60, height: 20), screen: screen, openHeight: 200)
        XCTAssertFalse(below, "decided by the open list's height, so it doesn't flip when the list opens")
        XCTAssertEqual(chip.maxY, 700 - 4)
        let (open, stillAbove) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 260, height: 200), screen: screen, openHeight: 200)
        XCTAssertFalse(stillAbove)
        XCTAssertEqual(open.maxY, 700 - 4)
    }

    func testClampedHorizontally() {
        let edge = CGRect(x: 0, y: 100, width: 128, height: 128)
        let (f, _) = ChipPlacement.frame(pixie: edge, panel: CGSize(width: 260, height: 200), screen: screen, openHeight: 200)
        XCTAssertEqual(f.minX, 0)
    }
}
```

`EngineEventTests.swift`, add:

```swift
    func testDecodesAgents() {
        let json = #"{"kind":"agents","roster":{"agents":[{"name":"coordinator","label":"Coordinator","status":"idle","kind":"coordinator","reachable":true}],"yours":[{"name":"api-claude","label":"login bug","detail":"WebStorm","status":"working","kind":"session","reachable":true,"app":"com.jetbrains.WebStorm"}],"agent_count":0,"your_count":1}}"#
        guard case .agents(let v)? = EngineEvent.decode(Data(json.utf8)) else { return XCTFail() }
        XCTAssertEqual(v.agentCount, 0)
        XCTAssertEqual(v.yourCount, 1)
        XCTAssertEqual(v.yours.first?.app, "com.jetbrains.WebStorm")
        XCTAssertNil(v.agents.first?.detail)
        XCTAssertNil(EngineEvent.decode(Data(#"{"kind":"agents"}"#.utf8)), "roster is required")
    }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `swift test --package-path mac/Packages/DeskPixieCore --filter 'AgentRosterModelTests|ChipPlacementTests|EngineEventTests'`
Expected: FAIL (compile errors: `cannot find 'AgentRosterModel' in scope`, `cannot find 'ChipPlacement' in scope`, `type 'EngineEvent' has no member 'agents'`)

- [ ] **Step 3: Implement**

`AgentRosterModel.swift`:

```swift
import Foundation

/// One agent beside Pixie, as the core's `agents` event lists it (#91).
public struct RosterRow: Decodable, Sendable, Equatable, Identifiable {
    public let name: String
    public let label: String
    public let detail: String?
    /// `PaneStatus`: working | idle | blocked | done | unknown.
    public let status: String
    /// coordinator | worker | pane | session.
    public let kind: String
    public let reachable: Bool
    /// A session's terminal app bundle id; nil when unknown.
    public let app: String?
    public var id: String { name }

    public init(name: String, label: String, detail: String?, status: String, kind: String, reachable: Bool, app: String?) {
        self.name = name
        self.label = label
        self.detail = detail
        self.status = status
        self.kind = kind
        self.reachable = reachable
        self.app = app
    }
}

/// The whole roster. `agentCount` never counts the coordinator; `yourCount` only reachable sessions.
public struct RosterView: Decodable, Sendable, Equatable {
    public let agents: [RosterRow]
    public let yours: [RosterRow]
    public let agentCount: Int
    public let yourCount: Int

    public static let empty = RosterView(agents: [], yours: [], agentCount: 0, yourCount: 0)

    public init(agents: [RosterRow], yours: [RosterRow], agentCount: Int, yourCount: Int) {
        self.agents = agents
        self.yours = yours
        self.agentCount = agentCount
        self.yourCount = yourCount
    }

    private enum CodingKeys: String, CodingKey {
        case agents, yours
        case agentCount = "agent_count"
        case yourCount = "your_count"
    }
}

/// The chip under Pixie and the list it opens (spec docs/superpowers/specs/2026-10-15-agent-roster-design.md).
/// Pure: the controller feeds it the core's roster and the pointer, and draws it.
public struct AgentRosterModel: Sendable, Equatable {
    public static let maxRows = 8

    public struct Section: Sendable, Equatable {
        public let title: String
        public let rows: [RosterRow]
        /// The "+N more" row's N.
        public let more: Int
    }

    public private(set) var view: RosterView = .empty
    /// The list is open (pointer over the chip, or a click).
    public var open = false

    public init() {}

    public mutating func apply(_ v: RosterView) {
        view = v
        if !visible { open = false }
    }

    /// Anything counted: the coordinator alone shows no chip.
    public var visible: Bool { view.agentCount + view.yourCount > 0 }

    /// `⚙ 3 · ⌨ 2`; a zero side is left out.
    public var chipText: String {
        var parts: [String] = []
        if view.agentCount > 0 { parts.append("⚙ \(view.agentCount)") }
        if view.yourCount > 0 { parts.append("⌨ \(view.yourCount)") }
        return parts.joined(separator: " · ")
    }

    public var sections: [Section] {
        var out: [Section] = []
        if !view.agents.isEmpty { out.append(section("Pixie's agents (\(view.agentCount))", view.agents)) }
        if !view.yours.isEmpty { out.append(section("Your sessions (\(view.yourCount))", view.yours)) }
        return out
    }

    private func section(_ title: String, _ rows: [RosterRow]) -> Section {
        Section(title: title, rows: Array(rows.prefix(Self.maxRows)), more: max(0, rows.count - Self.maxRows))
    }

    /// Pixie's agents always; your sessions only when reachable with a known app.
    public static func clickable(_ r: RosterRow) -> Bool {
        r.kind != "session" || (r.reachable && !(r.app ?? "").isEmpty)
    }

    public static func dot(_ status: String) -> String {
        switch status {
        case "working": return "●"
        case "blocked": return "◐"
        case "idle", "done": return "○"
        default: return "·"
        }
    }
}
```

`ChipPlacement.swift`:

```swift
import CoreGraphics

/// Where the roster chip (and the list it opens) sits: centred under Pixie, clamped on screen.
/// Desktop coordinates (`DesktopSpace`: top-left origin, y down), like `BubblePlacement`.
public enum ChipPlacement {
    /// `panel` is the chip, or the chip with the open list. `openHeight` (the open panel's height)
    /// picks below/above in every phase, so the chip doesn't jump when the list opens. Above
    /// Pixie when the open panel wouldn't fit below.
    public static func frame(pixie: CGRect, panel: CGSize, screen: CGRect, openHeight: CGFloat,
                             gap: CGFloat = 4) -> (CGRect, below: Bool) {
        let below = pixie.maxY + gap + max(openHeight, panel.height) <= screen.maxY
        let x = clamp(pixie.midX - panel.width / 2, screen.minX, screen.maxX - panel.width)
        let y = clamp(below ? pixie.maxY + gap : pixie.minY - gap - panel.height,
                      screen.minY, screen.maxY - panel.height)
        return (CGRect(x: x, y: y, width: panel.width, height: panel.height), below)
    }

    /// `lo` wins when the range is empty.
    private static func clamp(_ v: CGFloat, _ lo: CGFloat, _ hi: CGFloat) -> CGFloat { max(lo, min(v, hi)) }
}
```

`EngineEvent.swift`: add the case after `updates`:

```swift
    /// The roster beside Pixie (#91): Pixie's agents and your pix sessions, with counts.
    case agents(RosterView)
```

the payload struct `private struct Agents: Decodable { var roster: RosterView }`, and in `decode`: `case "agents": return get(Agents.self).map { .agents($0.roster) }`.

- [ ] **Step 4: Run them to verify they pass**

Run: `swift test --package-path mac/Packages/DeskPixieCore`
Expected: PASS (all, including the new tests)

- [ ] **Step 5: Commit**

```bash
git add mac/Packages/DeskPixieCore
git commit -m "DeskPixieCore: roster model, chip placement and the agents event (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 7: The chip and list on screen (app)

**Files:**
- Create: `mac/DeskPixie/Pixie/AgentRosterController.swift`, `mac/DeskPixie/Pixie/AgentRosterView.swift`
- Modify: `mac/DeskPixie/Pixie/PixieController.swift:56,214,218` (frame observers), `mac/DeskPixie/Pixie/UpdatesBubbleController.swift:50` (observe), `mac/DeskPixie/Engine/EngineCoordinator.swift:35,102,144,409-411` (create, route `.agents`, stop)

**Interfaces:**
- Consumes: `AgentRosterModel`, `RosterView`, `ChipPlacement`, `EngineEvent.agents` (Task 6); calls `agent.roster`, `agent.focusAgent` (Task 5).
- Produces: `PixieController.observeFrame(_ f: @escaping (NSRect) -> Void)`; `AgentRosterController(engine:worker:pixie:)` with `apply(view:)` and `stop()`.

- [ ] **Step 1: Frame observers** (`PixieController.swift`). Replace `var onFrameChange: ((NSRect) -> Void)?` with:

```swift
    /// Called with Pixie's panel frame (AppKit) after every move, drag or placement (the updates
    /// bubble and the roster chip follow it).
    private var frameObservers: [(NSRect) -> Void] = []
    func observeFrame(_ f: @escaping (NSRect) -> Void) { frameObservers.append(f) }
```

and both `onFrameChange?(panel.frame)` calls with `frameObservers.forEach { $0(panel.frame) }`. In `UpdatesBubbleController.init`, replace `pixie.onFrameChange = { [weak self] _ in self?.layout() }` with `pixie.observeFrame { [weak self] _ in self?.layout() }`.

- [ ] **Step 2: The view** (`AgentRosterView.swift`):

```swift
import DeskPixieCore
import SwiftUI

/// The roster chip under Pixie and, when open, the list (spec docs/superpowers/specs/2026-10-15-agent-roster-design.md
/// §3). Drawn by `AgentRosterController` from its model; `below` puts the list under the chip.
struct AgentRosterView: View {
    let model: AgentRosterModel
    let below: Bool
    /// The chip is never wider than Pixie (it must not reach the updates bubble at Pixie's side).
    let maxChipWidth: CGFloat
    let onChip: () -> Void
    let onRow: (RosterRow) -> Void

    static let listWidth: CGFloat = 260

    var body: some View {
        VStack(spacing: 4) {
            if model.open && !below { list }
            chip
            if model.open && below { list }
        }
        .fixedSize()
    }

    private var chip: some View {
        Button(action: onChip) {
            Text(model.chipText)
                .font(.caption.weight(.semibold))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .padding(.horizontal, 7)
                .padding(.vertical, 3)
                .background(Capsule().fill(.regularMaterial))
                .overlay(Capsule().strokeBorder(.separator))
        }
        .buttonStyle(.plain)
        .frame(maxWidth: maxChipWidth)
        .accessibilityLabel("\(model.view.agentCount) of Pixie's agents, \(model.view.yourCount) of your sessions")
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(model.sections, id: \.title) { section in
                Text(section.title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.top, 4)
                ForEach(section.rows) { r in
                    Button { onRow(r) } label: { row(r) }
                        .buttonStyle(.plain)
                        .disabled(!AgentRosterModel.clickable(r))
                }
                if section.more > 0 {
                    Text("+\(section.more) more")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                }
            }
        }
        .padding(6)
        .frame(width: Self.listWidth, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }

    private func row(_ r: RosterRow) -> some View {
        HStack(spacing: 6) {
            Text(AgentRosterModel.dot(r.status))
                .foregroundStyle(r.status == "working" ? .green : r.status == "blocked" ? .orange : .secondary)
            Text(r.label).font(.callout).lineLimit(1).truncationMode(.tail)
            Spacer(minLength: 4)
            Text(r.status).font(.caption).foregroundStyle(.secondary)
            if let d = r.detail, !d.isEmpty {
                Text(d).font(.caption).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .opacity(r.reachable ? 1 : 0.45)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}
```

- [ ] **Step 3: The controller** (`AgentRosterController.swift`):

```swift
import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The roster chip under Pixie (#91): a child panel of the pixie panel showing
/// `AgentRosterModel`. Hovering or clicking the chip opens the list; a row brings its agent
/// forward (`agent.focusAgent`), or activates your session's terminal app by bundle id. Opaque
/// through Pixie's proximity fade; hidden with Pixie. The pointer is polled, like the updates bubble.
@MainActor
final class AgentRosterController {
    static let pollInterval: TimeInterval = 0.1

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "roster")

    private let engine: CoreEngine
    private let worker: CoreWorker
    private let pixie: PixieController
    private let panel: PixiePanel
    private let hosting: RosterHostingView
    private(set) var model = AgentRosterModel()
    private var below = true
    private var timer: Timer?
    private var eventsApplied = 0

    init(engine: CoreEngine, worker: CoreWorker, pixie: PixieController) {
        self.engine = engine
        self.worker = worker
        self.pixie = pixie
        panel = PixiePanel(frame: NSRect(x: 0, y: 0, width: 1, height: 1))
        hosting = RosterHostingView(rootView: AnyView(EmptyView()))
        panel.contentView = hosting
        pixie.observeFrame { [weak self] _ in self?.layout() }
        timer = Timer.scheduledTimer(withTimeInterval: Self.pollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.poll() }
        }
        RunLoop.main.add(timer!, forMode: .common)
        fetchCurrent()
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        hide()
    }

    /// The core's roster (`agents` event).
    func apply(view: RosterView) {
        eventsApplied += 1
        model.apply(view)
        render()
    }

    private func fetchCurrent() {
        let engine = engine, worker = worker, since = eventsApplied
        Task { @MainActor [weak self] in
            let view: RosterView?? = await worker.run {
                do {
                    return try engine.call("agent.roster", as: RosterReply.self).roster
                } catch {
                    Self.log.error("agent.roster: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            guard let self, let view = view ?? nil, self.eventsApplied == since else { return }
            self.apply(view: view)
        }
    }

    private func clicked(_ r: RosterRow) {
        guard AgentRosterModel.clickable(r) else { return }
        if r.kind == "session" {
            guard let id = r.app, let app = NSRunningApplication.runningApplications(withBundleIdentifier: id).first else { return }
            app.activate()
        } else {
            let engine = engine, name = r.name
            worker.async {
                do {
                    _ = try engine.call("agent.focusAgent", FocusAgentArgs(name: name))
                } catch {
                    Self.log.error("agent.focusAgent: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    private func poll() {
        let inside = panel.isVisible && panel.frame.contains(NSEvent.mouseLocation)
        if inside != model.open && (inside || model.open) {
            // Opens under the pointer; closes when it leaves the chip and the list.
            model.open = inside && model.visible
            render()
        }
    }

    private func render() {
        hosting.rootView = AnyView(AgentRosterView(
            model: model, below: below, maxChipWidth: max(40, pixie.panel.frame.width),
            onChip: { [weak self] in
                guard let self else { return }
                self.model.open.toggle()
                self.render()
            },
            onRow: { [weak self] in self?.clicked($0) }))
        layout()
    }

    private func layout() {
        guard model.visible, pixie.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            hide()
            return
        }
        let top = ScreenDisplays.primaryMaxY
        let bit = pixie.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? pixie.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        let openHeight = CGFloat(min(model.view.agents.count, AgentRosterModel.maxRows + 1)
                                 + min(model.view.yours.count, AgentRosterModel.maxRows + 1) + 3) * 22 + size.height
        let (rect, b) = ChipPlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                            panel: size, screen: area, openHeight: openHeight)
        if b != below {
            below = b
            render() // redraw with the list on the other side; layout runs again
            return
        }
        panel.setFrame(ScreenDisplays.appKitFrame(topLeft: rect.origin, size: rect.size), display: true)
        if panel.parent == nil { pixie.panel.addChildWindow(panel, ordered: .above) }
        if !panel.isVisible { panel.orderFrontRegardless() }
        if panel.alphaValue != 1 { panel.alphaValue = 1 }
    }

    private func hide() {
        if panel.parent != nil { pixie.panel.removeChildWindow(panel) }
        if panel.isVisible { panel.orderOut(nil) }
        model.open = false
    }

    private struct RosterReply: Decodable { var roster: RosterView }
    private struct FocusAgentArgs: Encodable, Sendable { let name: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class RosterHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
```

(`openHeight` estimates the open list's height from its row count, ~22 pt per row plus headers, so the side is chosen before the list exists; a wrong estimate only moves the chip once.)

- [ ] **Step 4: Wire it** (`EngineCoordinator.swift`): add `private var roster: AgentRosterController?` next to `bubble`; where `startBubble(pixie: m, …)` is called (line 102) add `roster = AgentRosterController(engine: engine, worker: worker, pixie: m)`; in `shutdown()` after `bubble?.stop()` add `roster?.stop()`; in the event switch add:

```swift
        case .agents(let view):
            roster?.apply(view: view)
```

- [ ] **Step 5: Build and run the Swift tests**

Run: `make -C mac build && swift test --package-path mac/Packages/DeskPixieCore`
Expected: `** BUILD SUCCEEDED **`; all tests pass. (`make build` runs xcodegen, which picks up the two new files.)

- [ ] **Step 6: Commit**

```bash
git add mac/DeskPixie
git commit -m "app: the roster chip under Pixie, with the list on hover (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```

---

### Task 8: Live check and docs

**Files:**
- Modify: `core/app/agentapp/live_pix_test.go` (read the roster while `pix claude` runs), `docs/pix.md` (the chip), `docs/superpowers/specs/2026-10-15-agent-roster-design.md` (status line)

**Interfaces:**
- Consumes: `agentapp.Agent.Roster()` (Task 5).

- [ ] **Step 1: Extend the live test.** In `TestLivePixSessionThroughTheHerdrCoordinator`, after `t.Log("ok   pix claude registered")`, the coordinator isn't up yet; after `t.Log("ok   coordinator replied")` add:

```go
	// 1b. The roster beside Pixie: the coordinator listed but not counted; your session counted,
	// with its terminal's bundle id (#91).
	var view agent.RosterView
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		view, _ = a.Roster().(agent.RosterView)
		if view.YourCount == 1 && len(view.Agents) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("roster: %+v", view)
		}
	}
	if view.AgentCount != 0 || view.Agents[0].Kind != "coordinator" || view.Yours[0].Label != "marmalade jar" {
		t.Fatalf("roster: %+v", view)
	}
	t.Log("ok   roster: coordinator listed, not counted; your session counted")
```

Add `"github.com/sihekuang/deskpixie/core/agent"` to the imports, and set `__CFBundleIdentifier=com.googlecode.iterm2` in `pix.Env` and assert `view.Yours[0].App == "com.googlecode.iterm2"`.

- [ ] **Step 2: Run it**

Run: `go -C core test -tags live -run LivePix -v -timeout 15m ./app/agentapp/`
Expected: PASS, with the `ok   roster: …` line. (Needs `claude` and `herdr` installed and logged in; it uses Pixie's smoke session, never the app's.)

- [ ] **Step 3: Docs.** In `docs/pix.md`, under "What Pixie can do to your sessions", add:

```markdown
## Seeing what's running

A chip under Pixie counts what's running: `⚙ 3 · ⌨ 2` is three of Pixie's agents (its workers;
the coordinator is never counted) and two of your `pix` sessions. Hover it for the list; click
a row to bring that agent forward (Pixie's in its Herdr/tmux tab, yours by bringing its
terminal app to the front). With nothing but the coordinator running, there is no chip.
```

In the spec, change the status line to `Status: implemented on branch agent-roster (#91).`

- [ ] **Step 4: Full suites**

Run: `go -C core test -race ./... && swift test --package-path mac/Packages/DeskPixieCore && make -C mac build`
Expected: all pass; build succeeds.

- [ ] **Step 5: Commit**

```bash
git add core/app/agentapp/live_pix_test.go docs
git commit -m "docs, live smoke: the roster beside Pixie (#91)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw"
```
