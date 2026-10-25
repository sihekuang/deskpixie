package agent_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// #309: the stop button by a working agent, in the roster and the chat window.

func wantStopErr(t *testing.T, sent bool, err error, want string) {
	t.Helper()
	if sent || err == nil || err.Error() != want {
		t.Fatalf("got %v, %v; want %q", sent, err, want)
	}
}

func escapes(keys []agenttest.Keys, name string) int {
	n := 0
	for _, k := range keys {
		if k.Agent == name && slices.Equal(k.Keys, []string{"Escape"}) {
			n++
		}
	}
	return n
}

func TestStoppingTheCoordinatorCancelsTheTypedJobItWorksOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("a slow one"); err != nil {
			t.Fatal(err)
		}
		settle()
		sent, err := r.h.Interrupt(context.Background(), Coordinator)
		if !sent || err != nil {
			t.Fatal(sent, err)
		}
		settle()
		if n := escapes(r.host.Keys(), Coordinator); n != 1 {
			t.Fatal("one Esc for the coordinator", r.host.Keys())
		}
		if r.h.JobStatus()[0].Status != JobCancelled {
			t.Fatal(r.h.JobStatus())
		}
		if _, ok := r.host.StatusOf(Coordinator); !ok {
			t.Fatal("stopping never closes it")
		}
	})
}

func TestStoppingTheCoordinatorOnASpokenJobCancelsItToo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.Delegate("a spoken request", ""); err != nil {
			t.Fatal(err)
		}
		settle()
		if sent, err := r.h.Interrupt(context.Background(), Coordinator); !sent || err != nil {
			t.Fatal(sent, err)
		}
		settle()
		if n := escapes(r.host.Keys(), Coordinator); n != 1 {
			t.Fatal(r.host.Keys())
		}
		if st := r.h.JobStatus()[0].Status; st != JobCancelled {
			t.Fatal("a spoken request in the stopped turn is cancelled", st)
		}
	})
}

func TestStoppingAnIdleCoordinatorSendsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Input(EnsureCoordinatorInput())
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		sent, err := r.h.Interrupt(context.Background(), Coordinator)
		if sent || err != nil {
			t.Fatal("not working: a no-op", sent, err)
		}
		settle()
		if n := escapes(r.host.Keys(), Coordinator); n != 0 {
			t.Fatal(r.host.Keys())
		}
	})
}

func TestStoppingAnUnknownNameSaysItIsNotRunning(t *testing.T) {
	r := newRig(t)
	sent, err := r.h.Interrupt(context.Background(), "docs-writer")
	wantStopErr(t, sent, err, "Couldn't stop docs-writer: it isn't running anymore.")
	sent, err = r.h.Interrupt(context.Background(), Coordinator)
	wantStopErr(t, sent, err, "Couldn't stop the coordinator: it isn't running anymore.")
	sent, err = r.h.Interrupt(context.Background(), "")
	wantStopErr(t, sent, err, "Couldn't stop it.")
}

func TestStoppingWhenPixiesHostCantBeListedSaysWhy(t *testing.T) {
	r := newRig(t)
	r.host.FailList(true)
	sent, err := r.h.Interrupt(context.Background(), "focus-fix")
	wantStopErr(t, sent, err, "Pixie couldn't check on it. Try again.") // not a guess that it ended, nor herdr's words
}

func TestStoppingOneOfPixiesPanesPressesEscInIt(t *testing.T) {
	r, user := pixRig(t)
	r.host.StartAgent(context.Background(), AgentSpec{Name: "focus-fix"})
	r.host.SetStatus("focus-fix", PaneWorking)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 1 || len(user.Keys()) != 0 {
		t.Fatal(r.host.Keys(), user.Keys())
	}
	if len(r.host.Closed()) != 0 {
		t.Fatal("stopping never closes it", r.host.Closed())
	}
	// The fake's Esc ends the turn: a second click finds it idle.
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 1 {
		t.Fatal(r.host.Keys())
	}
}

func TestStoppingYourSessionHasPixPressEsc(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	if sent, err := r.h.Interrupt(context.Background(), "focus-claude"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(user.Keys(), "focus-claude"); n != 1 || len(user.Closed()) != 0 {
		t.Fatal(user.Keys(), user.Closed())
	}
	if n := escapes(r.host.Keys(), "focus-claude"); n != 0 {
		t.Fatal("not Pixie's host", r.host.Keys())
	}
}

func TestStoppingYourIdleSessionSendsNothing(t *testing.T) {
	r, user := pixRig(t)
	if sent, err := r.h.Interrupt(context.Background(), "focus-claude"); sent || err != nil {
		t.Fatal(sent, err)
	}
	if len(user.Keys()) != 0 {
		t.Fatal("Esc at an idle prompt is never sent", user.Keys())
	}
}

func TestStoppingYourSessionThatEndedSaysSo(t *testing.T) {
	r, user := pixRig(t)
	r.h.Interrupt(context.Background(), "focus-claude") // idle: listed, nothing sent
	user.Kill("focus-claude")
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop login bug: it isn't running anymore.") // as the list last had it
	// One the list never named: by the name asked for.
	sent, err = r.h.Interrupt(context.Background(), "docs-claude")
	wantStopErr(t, sent, err, "Couldn't stop docs-claude: it isn't running anymore.")
}

func TestStoppingYourSessionPixCantReachSaysSo(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.unreachable = true
	user.mu.Unlock()
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop login bug: its pix can't be reached; it may have been closed.")
	if len(user.Keys()) != 0 {
		t.Fatal(user.Keys())
	}
}

// The stop button says "Stop login bug": so does its failure, not pix's "focus-claude". A
// session with no description is "<tool> in <repo>", as the list has it.
func TestAStopsFailureNamesYourSessionAsTheListDoes(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.keysErr = HostError{Kind: HostOther, Detail: "Codex is waiting for you."}
	user.mu.Unlock()
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop login bug: Codex is waiting for you.")
	if got := (&UserSession{Tool: "claude", Repo: "focus"}).Label(); got != "claude in focus" {
		t.Fatal(got)
	}
	// Nothing to name it by: "", so a failure falls back to pix's name.
	if got := (&UserSession{}).Label(); got != "" {
		t.Fatal(got)
	}
}

func TestStoppingYourSessionSaysWhyPixRefused(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	for _, c := range []struct {
		err  error
		want string
	}{
		// #306: pix refuses keys while a startup menu only you should answer is up.
		{HostError{Kind: HostOther, Detail: "Codex is waiting for you to answer its update question."},
			"Couldn't stop login bug: Codex is waiting for you to answer its update question."},
		{ErrNoAgent("focus-claude"), "Couldn't stop login bug: it isn't running anymore."},
		// Last: a timeout holds the next stop back (its Esc may still land).
		{HostError{Kind: HostTimeout, Detail: "pix session focus-claude"}, "Couldn't stop login bug: it didn't answer in time."},
	} {
		user.mu.Lock()
		user.keysErr = c.err
		user.mu.Unlock()
		sent, err := r.h.Interrupt(context.Background(), "focus-claude")
		wantStopErr(t, sent, err, c.want)
	}
}

func TestTheRosterOffersStopOnlyWhileWorkingAndReachable(t *testing.T) {
	list := []AgentInfo{
		{Name: Coordinator, Status: PaneWorking},
		{Name: "focus-fix", Status: PaneIdle},
		{Name: "docs", Status: PaneWorking},
		{Name: "api", Status: PaneWorking, Origin: OriginUser, User: &UserSession{Tool: "claude", Reachable: true}},
		{Name: "gone", Status: PaneWorking, Origin: OriginUser, User: &UserSession{Tool: "claude"}},
		{Name: "asks", Status: PaneBlocked, Origin: OriginUser, User: &UserSession{Tool: "claude", Reachable: true}},
	}
	v := BuildRoster(list, nil, "herdr")
	got := map[string]bool{}
	for _, row := range append(v.Agents, v.Yours...) {
		got[row.Name] = row.Stoppable
	}
	want := map[string]bool{Coordinator: true, "focus-fix": false, "docs": true, "api": true, "gone": false, "asks": false}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: stoppable %v, want %v", n, got[n], w)
		}
	}
}

func TestAStopThatFailsForNoHostsReasonSaysTryAgain(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.keysErr = errors.New("dial unix /tmp/pix.sock: connect: connection refused")
	user.mu.Unlock()
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Pixie couldn't check on it. Try again.")
}

// workingPane is one of Pixie's panes, working, that takes an Esc and keeps working (as one
// slow to show its interrupt).
func workingPane(t *testing.T) *rtRig {
	r := newRig(t)
	r.host.StartAgent(context.Background(), AgentSpec{Name: "focus-fix"})
	r.host.SetStatus("focus-fix", PaneWorking)
	r.host.Inject(agenttest.IgnoreEscape)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	return r
}

func TestASecondStopSoonAfterSendsNoSecondEsc(t *testing.T) {
	r := workingPane(t)
	for range 2 { // the chat and the roster, or a double click
		if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
			t.Fatal("a success, sending nothing", sent, err)
		}
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 1 {
		t.Fatal("a second Esc opens Claude's rewind menu", r.host.Keys())
	}
}

func TestAStopBeforeTheStatusChangedSendsNoSecondEsc(t *testing.T) {
	r := workingPane(t)
	r.clock.Advance(StopRepeatMs - 1)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 1 {
		t.Fatal("still working since the last stop: no second Esc", r.host.Keys())
	}
	// Seen idle, then working on a new turn: that one can be stopped at once.
	r.host.SetStatus("focus-fix", PaneIdle)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); sent || err != nil {
		t.Fatal(sent, err)
	}
	r.host.SetStatus("focus-fix", PaneWorking)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 2 {
		t.Fatal(r.host.Keys())
	}
}

func TestAStopGoesAgainOnceTheLastWasIgnoredForLong(t *testing.T) {
	r := workingPane(t) // it ignores the Esc and keeps working
	r.clock.Advance(StopRepeatMs)
	if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(r.host.Keys(), "focus-fix"); n != 2 {
		t.Fatal("the button never stays dead", r.host.Keys())
	}
}

func TestAStopOfACoordinatorBackAtWorkWithinTheRostersDebounceGoes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("a slow one"); err != nil {
			t.Fatal(err)
		}
		settle()
		if sent, err := r.h.Interrupt(context.Background(), Coordinator); !sent || err != nil {
			t.Fatal(sent, err)
		}
		// Its Esc idles it, and the cancel note sets it working again: far inside the roster's
		// debounce, so only the status events show it changed.
		settle()
		r.host.SetStatus(Coordinator, PaneWorking)
		settle()
		if st, _ := r.host.StatusOf(Coordinator); st != PaneWorking {
			t.Fatal(st)
		}
		if sent, err := r.h.Interrupt(context.Background(), Coordinator); !sent || err != nil {
			t.Fatal(sent, err)
		}
		settle()
		if n := escapes(r.host.Keys(), Coordinator); n != 2 {
			t.Fatal("a real second stop is never swallowed", r.host.Keys())
		}
	})
}

func TestAStopThatTimedOutHoldsTheNextUntilTheStatusChanges(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.keysErr = HostError{Kind: HostTimeout}
	user.mu.Unlock()
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop login bug: it didn't answer in time.")
	user.mu.Lock()
	user.keysErr = nil
	user.mu.Unlock()
	r.clock.Advance(10 * StopRepeatMs)
	// Its Esc may still land when the session catches up: a retry sends no second one, and
	// says why rather than nothing.
	sent, err = r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop login bug: it hasn't answered the last stop yet.")
	if n := escapes(user.Keys(), "focus-claude"); n != 0 {
		t.Fatal(user.Keys())
	}
	user.SetStatus("focus-claude", PaneIdle)
	r.h.Interrupt(context.Background(), "focus-claude")
	user.SetStatus("focus-claude", PaneWorking)
	if sent, err := r.h.Interrupt(context.Background(), "focus-claude"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(user.Keys(), "focus-claude"); n != 1 {
		t.Fatal(user.Keys())
	}
}

func TestAFailedStopDoesNotHoldBackTheNext(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.keysErr = HostError{Kind: HostOther, Detail: "Codex is waiting for you to answer its update question."}
	user.mu.Unlock()
	if _, err := r.h.Interrupt(context.Background(), "focus-claude"); err == nil {
		t.Fatal("it refused")
	}
	user.mu.Lock()
	user.keysErr = nil
	user.mu.Unlock()
	if sent, err := r.h.Interrupt(context.Background(), "focus-claude"); !sent || err != nil {
		t.Fatal(sent, err)
	}
	if n := escapes(user.Keys(), "focus-claude"); n != 1 {
		t.Fatal(user.Keys())
	}
}

func TestStoppingPixiesWorkerTellsTheCoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Input(EnsureCoordinatorInput())
		settle()
		r.host.StartAgent(context.Background(), AgentSpec{Name: "focus-fix"})
		r.host.SetStatus("focus-fix", PaneWorking)
		// Not one of Pixie's workers: only the Esc, nothing for the coordinator.
		if sent, err := r.h.Interrupt(context.Background(), "focus-fix"); !sent || err != nil {
			t.Fatal(sent, err)
		}
		settle()
		for _, p := range r.host.Prompts() {
			if strings.Contains(p.Text, "The user stopped") {
				t.Fatal(p)
			}
		}
	})
}

func TestASendThatRunsOutOfTimeIsATimeout(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, fmt.Errorf("herdr: send keys: %w", context.Canceled)} {
		r, user := pixRig(t)
		user.SetStatus("focus-claude", PaneWorking)
		user.mu.Lock()
		user.keysErr = cause
		user.mu.Unlock()
		sent, err := r.h.Interrupt(context.Background(), "focus-claude")
		wantStopErr(t, sent, err, "Couldn't stop login bug: it didn't answer in time.")
		user.mu.Lock()
		user.keysErr = nil
		user.mu.Unlock()
		sent, err = r.h.Interrupt(context.Background(), "focus-claude")
		wantStopErr(t, sent, err, "Couldn't stop login bug: it hasn't answered the last stop yet.")
		if n := escapes(user.Keys(), "focus-claude"); n != 0 {
			t.Fatal(user.Keys())
		}
	}
}
