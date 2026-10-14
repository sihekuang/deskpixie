package agent

import (
	"strings"
	"testing"
)

// Not a Rust test: guards the embedded persona (Rust include_str!).
func TestDelegationPersonaIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(DelegationPersona, "Delegating to the agent:\n") || !strings.Contains(DelegationPersona, `"Agent: allowed" or "Agent: denied"`) {
		t.Fatal(DelegationPersona)
	}
}

func TestTheDelegationPersonaUsesTheAgentAsBitsMemory(t *testing.T) {
	if !strings.Contains(DelegationPersona, "call recall") || !strings.Contains(DelegationPersona, "Recent work with the agent") {
		t.Fatal(DelegationPersona)
	}
}
