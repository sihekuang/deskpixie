package agent

import "testing"

func TestManualCauseOrder(t *testing.T) {
	for _, c := range []struct {
		model      string
		lists, org bool
		want       ManualCause
	}{
		{"claude-sonnet-4-5", true, true, CauseOldModel},
		{"claude-opus-4-1", true, false, CauseOldModel},
		{"", false, false, CauseOldClaude},
		{"sonnet", false, true, CauseOldClaude},
		{"", true, true, CauseOrgOff},
		{"", true, false, CauseUnavailable},
		{"some-future-model", true, false, CauseUnavailable}, // unknown models are never blamed
	} {
		if got := ManualCauseOf(c.model, c.lists, c.org); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
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
