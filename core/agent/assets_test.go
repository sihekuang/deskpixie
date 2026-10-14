package agent

import (
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
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

// #57: Bit delegates by default; only a short, explicit list stays with Bit.
func TestTheDelegationPersonaDelegatesByDefault(t *testing.T) {
	for _, phrase := range []string{
		"Delegating is your default",
		"Everything else is delegated.",
		"Examples. Delegate:",
		"Keep: \"how are you\"",
		"Don't hesitate and don't ask first.",
		"Never ask permission to hand something off.",
		"does not hand it off",
		"never answer a new request from it instead of delegating",
		"get_time, get_weather, calculate, timer and media",
	} {
		if !strings.Contains(DelegationPersona, phrase) {
			t.Fatal(phrase)
		}
	}
}

// #57: lines that let Bit hedge, ask first or keep work must not come back.
func TestTheDelegationGuidanceHasNoHedgingLines(t *testing.T) {
	texts := map[string]string{"persona": voice.DefaultPersona(), "delegation": DelegationPersona, "delegate": Delegate{}.Description()}
	for name, text := range texts {
		for _, bad := range []string{
			"lean toward",
			"too vague to hand off",
			"if you still need something from the user",
			"Use it for any real work",
		} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(bad)) {
				t.Errorf("%s still says %q", name, bad)
			}
		}
	}
}

func TestTheDelegateToolSaysItIsTheDefault(t *testing.T) {
	d := Delegate{}.Description()
	for _, phrase := range []string{
		"This is the default for every request",
		"When unsure, call it",
		"don't try it yourself first or ask whether to",
		"get_time, get_weather, calculate, timer and media",
		"one short acknowledgement",
	} {
		if !strings.Contains(d, phrase) {
			t.Fatal(phrase)
		}
	}
}

func TestBitsPersonaHandsOffByDefault(t *testing.T) {
	p := voice.DefaultPersona()
	if !strings.Contains(p, "If you have a delegate tool, handing off is your default") ||
		!strings.Contains(p, "don't ask whether to hand it off") {
		t.Fatal(p)
	}
}

// #57: the recent-work block is background; it must not stand in for delegating new work.
func TestTheRecentWorkBlockSendsNewRequestsToDelegate(t *testing.T) {
	for _, h := range []string{memoryHeader, notesHeader} {
		if !strings.Contains(h, "follow-ups still go to delegate") {
			t.Fatal(h)
		}
	}
}
