package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// #309: agent.interrupt, the stop button by a working agent.

func TestInterruptCallsTheAgentWithTheRowsName(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.interrupt", map[string]string{"name": "docs-writer"}); !strings.Contains(string(out), `"interrupted":true`) {
		t.Fatal(string(out))
	}
	f.interruptIdle = true
	if out := h.call("agent.interrupt", map[string]string{"name": "coordinator"}); !strings.Contains(string(out), `"interrupted":false`) {
		t.Fatal("not working: nothing sent, no error", string(out))
	}
	if !slices.Equal(f.interrupted, []string{"docs-writer", "coordinator"}) {
		t.Fatal(f.interrupted)
	}
}

func TestInterruptPassesTheAgentsReasonOn(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}, interruptErr: errors.New("Couldn't stop docs-writer: it isn't running anymore.")}
	h := newHarness(t, withAgent(f))
	if _, err := h.callErr("agent.interrupt", map[string]string{"name": "docs-writer"}); err == nil || err.Error() != "Couldn't stop docs-writer: it isn't running anymore." {
		t.Fatal(err)
	}
}

func TestInterruptWithoutAnAgentSaysTheCoordinatorIsOff(t *testing.T) {
	h := newHarness(t)
	if _, err := h.callErr("agent.interrupt", map[string]string{"name": "docs-writer"}); err == nil || err.Error() != "The coordinator is off." {
		t.Fatal(err)
	}
}
