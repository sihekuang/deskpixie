package app

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/chat"
)

// #309: agent.interrupt, the stop button by a working agent.

func TestInterruptSendsTheRowsNameAndAnswersAtOnce(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.interrupt", map[string]string{"name": "docs-writer", "surface": "chat"}); string(out) != `{"sent":true}` {
		t.Fatal(string(out))
	}
	if !slices.Equal(f.interrupted, []string{"docs-writer@chat"}) {
		t.Fatal(f.interrupted)
	}
}

// wantRefusal: err is the core's own sentence, marked "refused" for the shell to show as it is.
func wantRefusal(t *testing.T, err error, want string) {
	t.Helper()
	var r *chat.Refusal
	if !errors.As(err, &r) || r.Code() != "refused" || err.Error() != want {
		t.Fatalf("%#v", err)
	}
}

func TestInterruptRefusesInItsOwnWords(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}, interruptErr: errors.New("Couldn't stop it.")}
	h := newHarness(t, withAgent(f))
	_, err := h.callErr("agent.interrupt", map[string]string{"name": ""})
	wantRefusal(t, err, "Couldn't stop it.")
}

func TestInterruptWithoutAnAgentSaysTheCoordinatorIsOff(t *testing.T) {
	h := newHarness(t)
	_, err := h.callErr("agent.interrupt", map[string]string{"name": "docs-writer"})
	wantRefusal(t, err, "The coordinator is off.")
}

func TestAFailedStopIsAnEvent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	f.hooks.StopFailed("docs-writer", "roster", "Couldn't stop docs-writer: it isn't running anymore.")
	e := h.waitFor("agent.stopFailed", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"kind":"agent.stopFailed","name":"docs-writer","surface":"roster","text":"Couldn't stop docs-writer: it isn't running anymore."}` {
		t.Fatal(string(b))
	}
}
