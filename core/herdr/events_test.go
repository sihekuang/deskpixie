package herdr

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Port of events.rs tests (20).

func coordinatorOnP2() *Agents {
	a := NewAgents()
	a.Insert("w1:p2", "coordinator")
	return a
}

func st(agentName string, s agent.PaneStatus) agent.PaneEvent {
	return agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: agentName, Status: s}
}

func exitedEv(agentName string) agent.PaneEvent {
	return agent.PaneEvent{Kind: agent.PaneEventExited, Agent: agentName}
}

func closed(p string) RawEvent { return RawEvent{Kind: RawClosed, Pane: p} }
func exitedRaw(p string) RawEvent {
	return RawEvent{Kind: RawExited, Pane: p}
}
func statusRaw(p string, s agent.PaneStatus) RawEvent {
	return RawEvent{Kind: RawStatus, Pane: p, Status: s}
}

func wantNone(t *testing.T, ev agent.PaneEvent, ok bool, msg string) {
	t.Helper()
	if ok {
		t.Fatalf("%s: got %+v, want none", msg, ev)
	}
}

func wantSome(t *testing.T, ev agent.PaneEvent, ok bool, want agent.PaneEvent) {
	t.Helper()
	if !ok || ev != want {
		t.Fatalf("got %+v (%v), want %+v", ev, ok, want)
	}
}

func wantEvents(t *testing.T, got []agent.PaneEvent, want []agent.PaneEvent, msg string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %+v, want %+v", msg, got, want)
	}
}

func TestTheCapturedStreamBecomesStatusChangesThenOneExit(t *testing.T) {
	a := coordinatorOnP2()
	var got []agent.PaneEvent
	for _, line := range rustLines(fixture(t, "events.jsonl")) {
		if raw, ok := ParseLine(line); ok {
			if ev, ok := a.Translate(raw); ok {
				got = append(got, ev)
			}
		}
	}
	wantEvents(t, got, []agent.PaneEvent{
		st("coordinator", agent.PaneIdle),
		st("coordinator", agent.PaneBlocked),
		st("coordinator", agent.PaneDone),
		exitedEv("coordinator"),
	}, "stream")
}

func TestAReplayedCloseOfAForgottenPaneIsDropped(t *testing.T) {
	a := coordinatorOnP2()
	if _, ok := a.Translate(closed("w1:p2")); !ok {
		t.Fatal("first close")
	}
	ev, ok := a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "the journal replays it on resubscribe")
}

func TestExitThenCloseReportsOneExit(t *testing.T) {
	a := coordinatorOnP2()
	if _, ok := a.Translate(exitedRaw("w1:p2")); !ok {
		t.Fatal("exit")
	}
	ev, ok := a.Translate(exitedRaw("w1:p2"))
	wantNone(t, ev, ok, "replayed exit")
	ev, ok = a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "already reported")
	if len(a.Panes()) != 0 {
		t.Fatal(a.Panes())
	}
}

func TestOtherPanesAreIgnored(t *testing.T) {
	a := coordinatorOnP2()
	ev, ok := a.Translate(statusRaw("w1:p1", agent.PaneIdle))
	wantNone(t, ev, ok, "status")
	ev, ok = a.Translate(closed("w1:p1"))
	wantNone(t, ev, ok, "close")
}

func TestAReusedPaneIDIsANewAgent(t *testing.T) {
	a := coordinatorOnP2()
	a.Translate(closed("w1:p2"))
	a.Insert("w1:p2", "coordinator")
	if _, ok := a.Translate(exitedRaw("w1:p2")); !ok {
		t.Fatal("exit of the new agent")
	}
}

func TestAReplayedJournalDoesNotHitAnAgentOnAReusedPane(t *testing.T) {
	a := coordinatorOnP2()
	a.Translate(closed("w1:p2"))
	a.Insert("w1:p2", "reviewer")
	a.BeginReplay(1000)
	ev, ok := a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "close")
	ev, ok = a.Translate(statusRaw("w1:p2", agent.PaneDone))
	wantNone(t, ev, ok, "status")
	ev, ok = a.Translate(exitedRaw("w1:p2"))
	wantNone(t, ev, ok, "exit")
	a.EndReplay()
	ev, ok = a.Translate(closed("w1:p2"))
	wantSome(t, ev, ok, exitedEv("reviewer"))
	ev, ok = a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "second live close")
}

func TestAForgottenReplayEndsByItself(t *testing.T) {
	a := coordinatorOnP2()
	a.BeginReplay(1000)
	ev, ok := a.TranslateAt(closed("w1:p2"), 2999)
	wantNone(t, ev, ok, "still replaying")
	ev, ok = a.TranslateAt(closed("w1:p2"), 3000)
	wantSome(t, ev, ok, exitedEv("coordinator"))
}

func TestPaneOfPrefersTheLatestAgentOfThatName(t *testing.T) {
	a := NewAgents()
	a.Insert("w1:p2", "coordinator")
	a.Insert("w1:p5", "coordinator")
	if p, ok := a.PaneOf("coordinator"); !ok || p != "w1:p5" {
		t.Fatal(p)
	}
}

func TestUnrelatedLinesParseToNone(t *testing.T) {
	if _, ok := ParseLine(`{"id":"evt_1","result":{"type":"subscription_started"}}`); ok {
		t.Fatal("subscription_started")
	}
	if _, ok := ParseLine("not json"); ok {
		t.Fatal("junk")
	}
}

func listed(pane string, status *agent.PaneStatus) []Listed {
	if status == nil {
		return []Listed{{Pane: pane}}
	}
	return []Listed{{Pane: pane, Status: *status, HasAgent: true}}
}

func ps(s agent.PaneStatus) *agent.PaneStatus { return &s }

// seenCoordinator: the coordinator on w1:p2, seen alive (listed with an agent detected).
func seenCoordinator() *Agents {
	a := coordinatorOnP2()
	known := a.Generations()
	a.Reconcile(known, listed("w1:p2", ps(agent.PaneIdle)))
	return a
}

func TestReconcileReportsAVanishedAgentOnce(t *testing.T) {
	a := seenCoordinator()
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, nil), []agent.PaneEvent{exitedEv("coordinator")}, "vanished")
	wantEvents(t, a.Reconcile(known, nil), nil, "already reported")
	if len(a.Panes()) != 0 {
		t.Fatal(a.Panes())
	}
}

func TestReconcileReportsAPaneWhoseAgentIsGoneOnceAndKeepsIt(t *testing.T) {
	a := seenCoordinator()
	known := a.Generations()
	noAgent := listed("w1:p2", nil)
	wantEvents(t, a.Reconcile(known, noAgent), []agent.PaneEvent{exitedEv("coordinator")}, "gone")
	wantEvents(t, a.Reconcile(known, noAgent), nil, "already reported")
	ev, ok := a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "the later close is not a second exit")
}

func TestReconcileNeverEndsAnAgentNotYetSeenAlive(t *testing.T) {
	a := coordinatorOnP2()
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, nil), nil, "still launching: not listed yet")
	wantEvents(t, a.Reconcile(known, listed("w1:p2", nil)), nil, "still launching: no agent detected yet")
	if p, ok := a.PaneOf("coordinator"); !ok || p != "w1:p2" {
		t.Fatal(p)
	}
	if _, ok := a.Translate(closed("w1:p2")); !ok {
		t.Fatal("a live close still ends it")
	}
}

func TestALiveStatusCountsAsSeenAlive(t *testing.T) {
	a := coordinatorOnP2()
	a.Translate(statusRaw("w1:p2", agent.PaneIdle))
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, nil), []agent.PaneEvent{exitedEv("coordinator")}, "seen")
}

func TestReconcileDoesNotRepeatAnExitAlreadyReported(t *testing.T) {
	a := seenCoordinator()
	if _, ok := a.Translate(exitedRaw("w1:p2")); !ok {
		t.Fatal("exit")
	}
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, nil), nil, "repeat")
}

func TestReconcileSendsAStatusOnlyWhenItDiffersFromTheLastSent(t *testing.T) {
	a := coordinatorOnP2()
	ev, ok := a.Translate(statusRaw("w1:p2", agent.PaneIdle))
	wantSome(t, ev, ok, st("coordinator", agent.PaneIdle))
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, listed("w1:p2", ps(agent.PaneIdle))), nil, "same")
	wantEvents(t, a.Reconcile(known, listed("w1:p2", ps(agent.PaneBlocked))), []agent.PaneEvent{st("coordinator", agent.PaneBlocked)}, "changed")
	wantEvents(t, a.Reconcile(known, listed("w1:p2", ps(agent.PaneBlocked))), nil, "same again")
}

func TestReconcileSkipsAPaneReusedAfterTheSnapshot(t *testing.T) {
	a := seenCoordinator()
	known := a.Generations()
	a.Insert("w1:p2", "reviewer")
	wantEvents(t, a.Reconcile(known, nil), nil, "reviewer was not known when agent.list was asked")
	if p, ok := a.PaneOf("reviewer"); !ok || p != "w1:p2" {
		t.Fatal(p)
	}
}

func TestReplacingAnAgentOnAReusedPaneEndsTheOldOneOnce(t *testing.T) {
	a := coordinatorOnP2()
	ev, ok := a.Replace("w1:p2", "reviewer")
	wantSome(t, ev, ok, exitedEv("coordinator")) // even if never seen alive
	if p, ok := a.PaneOf("reviewer"); !ok || p != "w1:p2" {
		t.Fatal(p)
	}
	ev, ok = a.Replace("w1:p3", "other")
	wantNone(t, ev, ok, "new pane")
	a.Translate(closed("w1:p3"))
	ev, ok = a.Replace("w1:p3", "third")
	wantNone(t, ev, ok, "the old agent's close was already reported")
}

func TestSubscriptionCoversClosesExitsAndEachNamedPane(t *testing.T) {
	assertJSON(t, SubscribeParams([]string{"w1:p3", "w1:p2"}), `{"subscriptions":[
		{"type":"pane.closed"},{"type":"pane.exited"},
		{"type":"pane.agent_status_changed","pane_id":"w1:p2"},
		{"type":"pane.agent_status_changed","pane_id":"w1:p3"}]}`)
}

func TestMarkSeenLetsReconcileEndAVanishedAgent(t *testing.T) {
	a := coordinatorOnP2()
	a.MarkSeen("w1:p2")
	known := a.Generations()
	wantEvents(t, a.Reconcile(known, nil), []agent.PaneEvent{exitedEv("coordinator")}, "seen")
}

func TestAForgottenPaneReportsNothing(t *testing.T) {
	a := coordinatorOnP2()
	a.Forget("w1:p2")
	ev, ok := a.Translate(closed("w1:p2"))
	wantNone(t, ev, ok, "forgotten")
	if len(a.Panes()) != 0 {
		t.Fatal(a.Panes())
	}
}

// Guard for the lines helper used above (Rust str::lines).
func TestRustLinesHelper(t *testing.T) {
	if got := strings.Join(rustLines("a\r\nb\n\nc\n"), "|"); got != "a|b||c" {
		t.Fatal(got)
	}
}
