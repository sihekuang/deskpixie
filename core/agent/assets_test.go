package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Not a Rust test: guards the embedded persona (Rust include_str!).
func TestDelegationPersonaIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(DelegationPersona, "Delegating to the coordinator:\n") || !strings.Contains(DelegationPersona, `"Coordinator: allowed" or "Coordinator: denied"`) {
		t.Fatal(DelegationPersona)
	}
}

func TestTheDelegationPersonaUsesTheAgentAsPixiesMemory(t *testing.T) {
	if !strings.Contains(DelegationPersona, "call recall") || !strings.Contains(DelegationPersona, "Recent work with the coordinator") {
		t.Fatal(DelegationPersona)
	}
}

// #57: Pixie delegates by default; only a short, explicit list stays with Pixie.
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

// #57: lines that let Pixie hedge, ask first or keep work must not come back.
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

func TestPixiesPersonaHandsOffByDefault(t *testing.T) {
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

// #71: the app's hand-off notices are explained to Pixie.
func TestTheDelegationPersonaExplainsHandOffNotices(t *testing.T) {
	for _, prefix := range []string{voice.HandedOffPrefix, voice.CantHandOffPrefix} {
		if !strings.Contains(DelegationPersona, `Lines starting "`+strings.TrimSuffix(prefix, " ")+`"`) {
			t.Errorf("the delegation persona doesn't explain %q lines", prefix)
		}
	}
}

// #76: the voice is Pixie and the agent it hands work to is the coordinator, named the same
// way everywhere, so "hand it to the coordinator" means delegate and Pixie never thinks it is
// the coordinator.
func TestPixiesPromptsNameTheCoordinator(t *testing.T) {
	persona, delegation := voice.DefaultPersona(), DelegationPersona
	if !strings.HasPrefix(persona, "You are Pixie,") {
		t.Errorf("the persona doesn't name Pixie: %q", persona[:min(60, len(persona))])
	}
	for name, text := range map[string]string{"persona": persona, "delegation": delegation} {
		if !strings.Contains(text, "the coordinator") {
			t.Errorf("%s doesn't name the coordinator", name)
		}
		for _, bad := range []string{"bigger brain", "You are Bit", "Bit's"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s still says %q", name, bad)
			}
		}
		if regexp.MustCompile(`\bBit\b`).MatchString(text) {
			t.Errorf("%s still calls Pixie Bit", name)
		}
		if regexp.MustCompile(`"Agent[ :]`).MatchString(text) {
			t.Errorf("%s still explains an old \"Agent ...\" line", name)
		}
	}
	for _, phrase := range []string{
		"The coordinator is a capable agent working in the background.",
		"You are not the coordinator",
		"when the user says to hand something to the coordinator, or to ask, tell or give it to the coordinator (or the agent, or Claude), call delegate.",
	} {
		if !strings.Contains(delegation, phrase) {
			t.Errorf("the delegation rules don't say %q", phrase)
		}
	}
	for _, d := range []string{Delegate{}.Description(), Jobs{}.Description(), AnswerJob{}.Description(), Recall{}.Description()} {
		if !strings.Contains(d, "coordinator") || strings.Contains(d, "the agent") || strings.Contains(d, "bigger brain") {
			t.Errorf("a tool description doesn't name the coordinator: %q", d)
		}
	}
	if !strings.HasPrefix(Delegate{}.Description(), "Hand a task to the coordinator") {
		t.Error(Delegate{}.Description())
	}
}

// #83: a failed local tool (the weather lookup that can't find the place) is checked with the
// coordinator, not answered with "I can't".
func TestAFailedLocalToolIsCheckedWithTheCoordinator(t *testing.T) {
	if !strings.Contains(DelegationPersona, "If one of your own tools fails or finds nothing") ||
		!strings.Contains(DelegationPersona, `call delegate with "Check: "`) {
		t.Fatal("delegation rules lack the failed-tool fallback")
	}
}

// #103: after a check's answer Pixie decides whether to ask for more; the app keeps the
// conversation open only when it does.
func TestPixieDecidesWhetherToAskForMoreAfterACheck(t *testing.T) {
	for _, phrase := range []string{`End with "Anything else?" only if they probably will`, "the app ends the conversation after it"} {
		if !strings.Contains(DelegationPersona, phrase) {
			t.Fatal(phrase)
		}
	}
}

// #157: several things in one breath. Independent ones are separate calls; steps that come
// in an order, or a change of mind, are one call in the user's order with nothing taken back.
func TestTheDelegationPersonaKeepsStepsInOrderAndDropsWhatWasTakenBack(t *testing.T) {
	for _, phrase := range []string{
		"One delegate call per independent task.",
		"call delegate once",
		"in the order the user wants",
		"Never hand off a step the user put off or took back",
		"never make two calls that contradict each other",
		"don't mention what they took back",
		"When one breath has both, an unrelated thing is still its own call",
		"Unrelated things stay separate calls even when joined by \"and\", \"also\" or \"separately\"",
		`"First write a program that checks the SpaceX stock price, then merge the build."`,
	} {
		if !strings.Contains(DelegationPersona, phrase) {
			t.Errorf("delegation.md does not say %q", phrase)
		}
	}
}
