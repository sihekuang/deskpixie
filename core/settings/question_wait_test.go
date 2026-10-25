package settings

import (
	"testing"

	"github.com/sihekuang/deskpixie/core/pix"
)

// #268: how long a session's question waits for your answer in Pixie before its terminal shows
// it: 20 seconds by default, 0 to answer from Pixie by keys as before, at most pix's hold.
func TestQuestionWaitIsARestartAgentNumber(t *testing.T) {
	if got := Default().Agent.QuestionWaitSecs; got != 20 {
		t.Fatal(got)
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.Agent.QuestionWaitSecs != 20 {
		t.Fatal("an old settings.json gets the default:", c.Agent.QuestionWaitSecs, err)
	}
	f := FieldByPath("agent.questionWaitSecs")
	if f.Section != SectionSessions || f.Apply != ApplyRestart || f.Kind.Type != KindInt {
		t.Fatalf("%+v", f)
	}
	most := uint64(pix.MaxHold.Seconds())
	env := testEnv(t)
	for secs, ok := range map[uint64]bool{0: true, 20: true, most: true, most + 1: false} {
		c := Default()
		c.Agent.QuestionWaitSecs = secs
		got := problems(c, env)
		bad := len(got) == 1 && got[0][0] == "agent.questionWaitSecs"
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%d: %v", secs, got)
		}
	}
}
