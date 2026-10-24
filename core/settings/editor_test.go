package settings

import (
	"testing"

	"github.com/sihekuang/deskpixie/core/richtext"
)

// #277: where a file named in Pixie's bubbles opens. Automatic by default (the IDE the session
// runs in, else the file's default app), read on every click.
func TestEditorIsALiveAgentChoice(t *testing.T) {
	if got := Default().Agent.Editor; got != richtext.EditorAuto {
		t.Fatal(got)
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.Agent.Editor != richtext.EditorAuto {
		t.Fatal("an old settings.json gets the default:", c.Agent.Editor, err)
	}
	f := FieldByPath("agent.editor")
	if f.Section != SectionAgent || f.Apply != ApplyLive || f.Kind.Type != KindChoice {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("agent.editor"); !ok || r != RouteReadEachTick {
		t.Fatal(r, ok)
	}
	if len(Editors) != len(richtext.Editors) {
		t.Fatal("every editor the core can open is a choice", Editors)
	}
	env := testEnv(t)
	for value, ok := range map[string]bool{richtext.EditorAuto: true, richtext.EditorZed: true, "notepad": false} {
		c := Default()
		c.Agent.Editor = value
		got := problems(c, env)
		bad := len(got) == 1 && got[0][0] == "agent.editor"
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%s: %v", value, got)
		}
	}
}
