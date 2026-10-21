package settings

import "testing"

// #225: the log size is an Advanced setting in MB, 2 by default, read on every write.
func TestLogMaxMBIsALiveAdvancedNumber(t *testing.T) {
	if got := Default().LogMaxMB; got != 2 {
		t.Fatal(got)
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.LogMaxMB != 2 {
		t.Fatal("an old settings.json gets the default:", c.LogMaxMB, err)
	}
	f := FieldByPath("logMaxMB")
	if f.Section != SectionAdvanced || f.Apply != ApplyLive || f.Kind.Type != KindInt {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("logMaxMB"); !ok || r != RouteReadEachTick {
		t.Fatal(r, ok)
	}
	env := testEnv(t)
	for mb, ok := range map[uint32]bool{0: false, 1: true, 2: true, 100: true, 101: false} {
		c := Default()
		c.LogMaxMB = mb
		got := problems(c, env)
		bad := len(got) == 1 && got[0][0] == "logMaxMB"
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%d: %v", mb, got)
		}
	}
}
