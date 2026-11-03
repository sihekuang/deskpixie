package app

import (
	"errors"
	"slices"
	"testing"
)

// #431: agent.setSessionOff, the switch on your session's row in the roster.

func TestSetSessionOffReachesTheAgent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.setSessionOff", map[string]any{"name": "api-claude", "off": true}); string(out) != `{"off":true}` {
		t.Fatal(string(out))
	}
	h.call("agent.setSessionOff", map[string]any{"name": "api-claude", "off": false})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.switched, []string{"api-claude:off", "api-claude:on"}) {
		t.Fatal(f.switched)
	}
}

func TestSetSessionOffRefusesInItsOwnWords(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}, switchErr: errors.New("No session x.")}
	h := newHarness(t, withAgent(f))
	_, err := h.callErr("agent.setSessionOff", map[string]any{"name": "x", "off": true})
	wantRefusal(t, err, "No session x.")
}

func TestSetSessionOffWithoutAnAgentSaysTheCoordinatorIsOff(t *testing.T) {
	h := newHarness(t)
	_, err := h.callErr("agent.setSessionOff", map[string]any{"name": "api-claude", "off": true})
	wantRefusal(t, err, "The coordinator is off.")
}
