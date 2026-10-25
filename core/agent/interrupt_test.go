package agent_test

import (
	"context"
	"slices"
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

func TestStoppingTheCoordinatorOnAnotherTurnInterruptsItAndKeepsTheJob(t *testing.T) {
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
		if st := r.h.JobStatus()[0].Status; st == JobCancelled {
			t.Fatal("only typed jobs are cancelled; a spoken one stays open", st)
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
	wantStopErr(t, sent, err, "Couldn't stop that: it has no name.")
}

func TestStoppingWhenPixiesHostCantBeListedSaysWhy(t *testing.T) {
	r := newRig(t)
	r.host.FailList(true)
	sent, err := r.h.Interrupt(context.Background(), "focus-fix")
	if sent || err == nil || err.Error() == "Couldn't stop focus-fix: it isn't running anymore." {
		t.Fatal("the listing's failure, not a guess that it ended", sent, err)
	}
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
	user.Kill("focus-claude")
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop focus-claude: it isn't running anymore.")
}

func TestStoppingYourSessionPixCantReachSaysSo(t *testing.T) {
	r, user := pixRig(t)
	user.SetStatus("focus-claude", PaneWorking)
	user.mu.Lock()
	user.unreachable = true
	user.mu.Unlock()
	sent, err := r.h.Interrupt(context.Background(), "focus-claude")
	wantStopErr(t, sent, err, "Couldn't stop focus-claude: its pix can't be reached; it may have been closed.")
	if len(user.Keys()) != 0 {
		t.Fatal(user.Keys())
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
			"Couldn't stop focus-claude: Codex is waiting for you to answer its update question."},
		{HostError{Kind: HostTimeout, Detail: "pix session focus-claude"}, "Couldn't stop focus-claude: it didn't answer in time."},
		{ErrNoAgent("focus-claude"), "Couldn't stop focus-claude: it isn't running anymore."},
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
