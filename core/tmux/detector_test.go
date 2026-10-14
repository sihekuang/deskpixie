package tmux

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

type events struct {
	mu  sync.Mutex
	got []agent.PaneEvent
}

func (e *events) emit(ev agent.PaneEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev)
}

// take returns and clears what was emitted.
func (e *events) take() []agent.PaneEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.got
	e.got = nil
	return out
}

func status(name string, s agent.PaneStatus) agent.PaneEvent {
	return agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: name, Status: s}
}

func exited(name string) agent.PaneEvent {
	return agent.PaneEvent{Kind: agent.PaneEventExited, Agent: name}
}

func wantEvents(t *testing.T, e *events, want ...agent.PaneEvent) {
	t.Helper()
	if got := e.take(); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

const (
	codexIdle    = "╭──────────────╮\n│ >_ OpenAI Codex (v0.162.1) │\n╰──────────────╯\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	codexWorking = "• Working (12s • esc to interrupt)\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	codexTrust   = "> You are in /repo\n\n  Do you trust the contents of this directory?\n  1. Yes, continue\n  2. No, quit\n\n  Press enter to continue · esc quit"
	codexMcp     = "  Allow the pixie MCP server to run tool \"report_progress\"?\n  1. Yes\n  2. No"
	// A Codex kept running over the #68 rename still names the server "mascot".
	codexMcpLegacy = "  Allow the mascot MCP server to run tool \"report_progress\"?\n  1. Yes\n  2. No"
	codexCommand   = "  Would you like to run the following command?\n  $ rm -rf build\n  › 1. Yes, proceed\n    2. No"
	claudeIdle     = "╭───────────────╮\n│ >             │\n╰───────────────╯\n  ? for shortcuts"
	claudeAuto     = "────────\n❯ \n────────\n  ⏵⏵ auto mode on (shift+tab to cycle)"
	claudeBusy     = "✻ Pondering… (3s · esc to interrupt)\n╭───────────────╮\n│ >             │\n╰───────────────╯\n  ? for shortcuts"
	claudeTrust    = " Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit"
	claudeTrust2   = " Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.\n\n Claude Code'll be able to read, edit, and execute files here.\n\n Security guide\n\n ❯ 1. Yes, I trust this folder\n   2. No, exit"
	claudeAsk      = " Bash command\n   rm -rf build\n Do you want to proceed?\n ❯ 1. Yes\n   2. No"
	blank          = "\n\n\n"
)

func TestScreensAreClassifiedWithTheControllersMarkers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   agent.AgentKind
		screen string
		want   agent.PaneStatus
	}{
		{"codex idle", agent.Codex, codexIdle, agent.PaneIdle},
		{"codex working keeps its placeholder", agent.Codex, codexWorking, agent.PaneWorking},
		{"codex trust is no busy pane", agent.Codex, codexTrust, agent.PaneBlocked},
		{"codex mcp approval", agent.Codex, codexMcp, agent.PaneBlocked},
		{"codex mcp approval, pre-#68 server name", agent.Codex, codexMcpLegacy, agent.PaneBlocked},
		{"codex command approval", agent.Codex, codexCommand, agent.PaneBlocked},
		{"codex starting", agent.Codex, "Booting MCP server: pixie", agent.PaneUnknown},
		{"codex blank", agent.Codex, blank, agent.PaneUnknown},
		{"claude idle", agent.Claude, claudeIdle, agent.PaneIdle},
		{"claude idle in auto mode", agent.Claude, claudeAuto, agent.PaneIdle},
		{"claude working", agent.Claude, claudeBusy, agent.PaneWorking},
		{"claude trust", agent.Claude, claudeTrust, agent.PaneBlocked},
		{"claude trust (newer text)", agent.Claude, claudeTrust2, agent.PaneBlocked},
		{"claude permission prompt", agent.Claude, claudeAsk, agent.PaneBlocked},
		{"claude blank", agent.Claude, "", agent.PaneUnknown},
		// A busy marker far above the bottom is history, not the current state.
		{"old busy line", agent.Codex, "esc to interrupt\n1\n2\n3\n4\n5\n6\n7\n8\n› Ask Codex to do anything", agent.PaneIdle},
	} {
		if got := Classify(tc.kind, tc.screen); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAStartIsDoneAtTheInputPromptOrADialog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   agent.AgentKind
		screen string
		want   agent.PaneStatus
		ok     bool
	}{
		{"claude prompt", agent.Claude, claudeIdle, agent.PaneIdle, true},
		{"claude auto mode", agent.Claude, claudeAuto, agent.PaneIdle, true},
		{"claude trust dialog counts", agent.Claude, claudeTrust2, agent.PaneBlocked, true},
		{"claude still loading", agent.Claude, "Claude Code v2.1", agent.PaneUnknown, false},
		{"codex ready marker", agent.Codex, codexIdle, agent.PaneIdle, true},
		{"codex trust (the runtime refuses it)", agent.Codex, codexTrust, agent.PaneBlocked, true},
		// "for shortcuts" alone isn't Codex's Ready marker.
		{"codex not ready", agent.Codex, "  ? for shortcuts", agent.PaneUnknown, false},
	} {
		got, ok := StartedScreen(tc.kind, tc.screen)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHookStatesAreReportedWhenTheyChange(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Track("%1", "coordinator", agent.Claude)
	// Nothing before the start finished.
	d.Hook("coordinator", agent.PaneWorking)
	wantEvents(t, e)
	// The hook's status wins over the start screen's.
	if got := d.Started("%1", agent.PaneIdle); got != agent.PaneWorking {
		t.Fatal(got)
	}
	wantEvents(t, e, status("coordinator", agent.PaneWorking))
	for _, step := range []struct {
		in   agent.PaneStatus
		want []agent.PaneEvent
	}{
		{agent.PaneWorking, nil},
		{agent.PaneIdle, []agent.PaneEvent{status("coordinator", agent.PaneIdle)}},
		{agent.PaneIdle, nil},
		{agent.PaneBlocked, []agent.PaneEvent{status("coordinator", agent.PaneBlocked)}},
		{agent.PaneUnknown, nil},
		{agent.PaneWorking, []agent.PaneEvent{status("coordinator", agent.PaneWorking)}},
	} {
		d.Hook("coordinator", step.in)
		wantEvents(t, e, step.want...)
	}
	if d.Status("%1") != agent.PaneWorking {
		t.Fatal(d.Status("%1"))
	}
	// A hook for an agent the detector doesn't know is dropped.
	d.Hook("w-9", agent.PaneIdle)
	wantEvents(t, e)
}

func TestAnIdleHookedClaudeAgentsScreenIsNotRead(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Adopt("%3", "w-claude", agent.Claude) // adopted after a restart: no hook yet
	d.Adopt("%4", "w-codex", agent.Codex)
	if got := d.ScreenPanes(); !reflect.DeepEqual(got, []string{"%3", "%4"}) {
		t.Fatal(got)
	}
	d.Screen("%3", claudeBusy)
	wantEvents(t, e, status("w-claude", agent.PaneWorking))
	d.Hook("w-claude", agent.PaneIdle)
	wantEvents(t, e, status("w-claude", agent.PaneIdle))
	if got := d.ScreenPanes(); !reflect.DeepEqual(got, []string{"%4"}) {
		t.Fatal(got)
	}
	d.Screen("%3", claudeBusy) // a late read: ignored
	wantEvents(t, e)
}

func TestCodexStatusFollowsItsScreen(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Track("%2", "w-codex", agent.Codex)
	d.Screen("%2", codexWorking) // not started yet: nothing
	wantEvents(t, e)
	d.Started("%2", agent.PaneIdle)
	wantEvents(t, e, status("w-codex", agent.PaneIdle))
	for _, step := range []struct {
		screen string
		want   []agent.PaneEvent
	}{
		{codexIdle, nil},
		{codexWorking, []agent.PaneEvent{status("w-codex", agent.PaneWorking)}},
		{codexWorking, nil},
		{"redrawing…", nil}, // unknown keeps the last status
		{codexMcp, []agent.PaneEvent{status("w-codex", agent.PaneBlocked)}},
		{codexWorking, []agent.PaneEvent{status("w-codex", agent.PaneWorking)}},
		{codexIdle, []agent.PaneEvent{status("w-codex", agent.PaneIdle)}},
	} {
		d.Screen("%2", step.screen)
		wantEvents(t, e, step.want...)
	}
}

func TestAnExitIsReportedOnceAndNeverReplayed(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Track("%1", "coordinator", agent.Claude)
	d.Started("%1", agent.PaneIdle)
	d.Track("%2", "w-a", agent.Codex)
	d.Started("%2", agent.PaneIdle)
	e.take()
	snap := d.Snapshot()
	// A pane tracked after the snapshot is left alone even though the list doesn't show it.
	d.Track("%3", "w-b", agent.Codex)
	d.Started("%3", agent.PaneIdle)
	e.take()
	rows := []Row{{Window: "coordinator", Pane: "%1", Dead: true}, {Window: "w-a", Pane: "%2"}}
	d.Listed(snap, rows)
	wantEvents(t, e, exited("coordinator"))
	d.Listed(snap, rows) // the dead pane is still listed: nothing more
	wantEvents(t, e)
	d.Hook("coordinator", agent.PaneIdle) // nothing after an exit
	wantEvents(t, e)
	d.Gone("%1")
	wantEvents(t, e)
	// A vanished window is an exit too.
	d.Listed(d.Snapshot(), []Row{{Window: "w-b", Pane: "%3"}})
	wantEvents(t, e, exited("w-a"))
	// The whole session gone: every agent exited.
	d.Listed(d.Snapshot(), nil)
	wantEvents(t, e, exited("w-b"))
	if d.Len() != 0 {
		t.Fatal(d.Len())
	}
}

func TestAStartingAgentIsNotReportedExitedAndAForgottenOneIsSilent(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Track("%5", "w-a", agent.Claude)
	d.Listed(d.Snapshot(), nil) // StartAgent reports its own failure
	wantEvents(t, e)
	d.Gone("%5")
	wantEvents(t, e)
	d.Track("%6", "w-a", agent.Claude)
	d.Started("%6", agent.PaneIdle)
	e.take()
	d.Forget("%6") // replaced: its name lives on
	d.Listed([]Generation{{"%6", 2}}, nil)
	wantEvents(t, e)
	// Closing a started agent reports its exit once.
	d.Track("%7", "w-b", agent.Claude)
	d.Started("%7", agent.PaneBlocked)
	wantEvents(t, e, status("w-b", agent.PaneBlocked))
	d.Gone("%7")
	d.Gone("%7")
	wantEvents(t, e, exited("w-b"))
}

func TestHooksGoToTheNewestPaneOfAName(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	d.Adopt("%1", "w-a", agent.Claude)
	d.Adopt("%9", "w-a", agent.Claude)
	d.Hook("w-a", agent.PaneWorking)
	if d.Status("%9") != agent.PaneWorking || d.Status("%1") != agent.PaneUnknown {
		t.Fatal(d.Status("%9"), d.Status("%1"))
	}
	if d.Adopt("%9", "w-a", agent.Claude) {
		t.Fatal("already tracked")
	}
}

func TestAHookedClaudeTurnEndsOnItsScreenAfterEscapeOrAfterTheGrace(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	now := time.Unix(1000, 0)
	d.SetClock(func() time.Time { return now }, 5*time.Second)
	d.Track("%1", "coordinator", agent.Claude)
	d.Started("%1", agent.PaneIdle)
	d.Hook("coordinator", agent.PaneWorking)
	wantEvents(t, e, status("coordinator", agent.PaneIdle), status("coordinator", agent.PaneWorking))
	if got := d.ScreenPanes(); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatal("a hooked agent in a turn is still watched", got)
	}
	// Busy screens say nothing new; an idle one ends the turn only after an Escape...
	d.Screen("%1", claudeBusy)
	d.Screen("%1", claudeIdle)
	wantEvents(t, e)
	d.Interrupted("coordinator")
	d.Screen("%1", claudeBusy) // Escape not handled yet
	wantEvents(t, e)
	d.Screen("%1", claudeIdle)
	wantEvents(t, e, status("coordinator", agent.PaneIdle))
	if got := d.ScreenPanes(); len(got) != 0 {
		t.Fatal("an idle hooked agent's screen is not read", got)
	}
	// ...or once it has shown idle for the whole grace (a turn that failed without a Stop).
	d.Hook("coordinator", agent.PaneBlocked)
	wantEvents(t, e, status("coordinator", agent.PaneBlocked))
	d.Screen("%1", claudeIdle)
	now = now.Add(4 * time.Second)
	d.Screen("%1", claudeIdle)
	wantEvents(t, e)
	d.Screen("%1", claudeBusy) // the streak breaks
	now = now.Add(2 * time.Second)
	d.Screen("%1", claudeIdle)
	wantEvents(t, e)
	now = now.Add(5 * time.Second)
	d.Screen("%1", claudeIdle)
	wantEvents(t, e, status("coordinator", agent.PaneIdle))
	// A hook clears an Escape that did not end anything.
	d.Interrupted("coordinator")
	d.Hook("coordinator", agent.PaneWorking)
	wantEvents(t, e, status("coordinator", agent.PaneWorking))
	d.Screen("%1", claudeIdle)
	wantEvents(t, e)
	// Escape to an agent not hooked: nothing special (its screen is read anyway).
	d.Track("%2", "w-a", agent.Claude)
	d.Interrupted("w-a")
}
