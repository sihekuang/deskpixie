package agent

import "testing"

func TestManualCauseOrder(t *testing.T) {
	for _, c := range []struct {
		model  string
		claude ClaudeAuto
		org    bool
		want   ManualCause
	}{
		{"claude-sonnet-4-5", ClaudeListsAuto, true, CauseOldModel},
		{"claude-opus-4-1", ClaudeListsAuto, false, CauseOldModel},
		{"", ClaudeLacksAuto, false, CauseOldClaude},
		{"sonnet", ClaudeLacksAuto, true, CauseOldClaude},
		{"", ClaudeListsAuto, true, CauseOrgOff},
		{"", ClaudeListsAuto, false, CauseUnavailable},
		{"some-future-model", ClaudeListsAuto, false, CauseUnavailable}, // unknown models are never blamed
	} {
		if got := ManualCauseOf(c.model, c.claude, c.org); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// A probe that couldn't tell (claude not found, an error, a timeout) never blames Claude Code:
// the cause falls through to the organization or "unavailable right now".
func TestAnUnknownProbeNeverBlamesClaudeCode(t *testing.T) {
	if got := ManualCauseOf("", ClaudeAutoUnknown, false); got != CauseUnavailable {
		t.Errorf("unknown, org on: %v", got)
	}
	if got := ManualCauseOf("", ClaudeAutoUnknown, true); got != CauseOrgOff {
		t.Errorf("unknown, org off: %v", got)
	}
	if got := ManualCauseOf("claude-opus-4-1", ClaudeAutoUnknown, false); got != CauseOldModel {
		t.Errorf("unknown, old model: %v", got)
	}
}

func TestModelSupportsAuto(t *testing.T) {
	for m, ok := range map[string]bool{
		"": true, "opus": true, "sonnet": true, "haiku": true, "claude-opus-4-6": true, "claude-sonnet-4-6": true,
		"claude-opus-5-5": true, "claude-haiku-5-5": true, "claude-fable-5-1": true,
		"claude-sonnet-4-5": false, "claude-opus-4-1": false, "claude-haiku-4-5": false, "claude-3-7-sonnet": false,
		"gpt-5": true, "who-knows": true,
	} {
		if ModelSupportsAuto(m) != ok {
			t.Errorf("%q", m)
		}
	}
}

func TestManualReasonWording(t *testing.T) {
	if CauseOldModel.Reason() != "its model is too old for auto mode" || CauseUnavailable.Reason() == "" {
		t.Fatal("wording")
	}
}
