package settings

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// #290: every page is short enough to scan, in groups that stay together.

// pageRows counts what page section shows on platform p with the defaults: the visible fields
// with a control, and the extra rows.
func pageRows(section Section, p Platform) int {
	values := tree(Default())
	n := 0
	for _, f := range Fields {
		if f.Section == section && f.Kind.Type != KindManaged && onPlatform(f.Platforms, p) && Visible(f, values) {
			n++
		}
	}
	for _, e := range ExtraViews(p) {
		if e.Section == section {
			n++
		}
	}
	return n
}

func TestNoPageHasMoreThanFifteenRows(t *testing.T) {
	for _, p := range AllPlatforms {
		for _, s := range Sections {
			if n := pageRows(s, p); n == 0 || n > MaxPageRows {
				t.Errorf("%s on %s: %d rows", s, p, n)
			}
		}
	}
	// GPT-Live's two fields still fit.
	c := Default()
	c.Voice.Provider = ProviderOpenAILive
	values, n := tree(c), 0
	for _, f := range Fields {
		if f.Section == SectionVoice && onPlatform(f.Platforms, Macos) && Visible(f, values) {
			n++
		}
	}
	if n+2 > MaxPageRows { // + the two API keys
		t.Fatal(n + 2)
	}
}

func TestEveryFieldIsOnAPageInAGroupThatStaysTogether(t *testing.T) {
	pages := map[Section]int{}
	for i, s := range Sections {
		pages[s] = i
	}
	type key struct {
		s Section
		g string
	}
	seen := map[key]bool{}
	last := key{}
	page := 0
	for _, f := range Fields {
		at, ok := pages[f.Section]
		if !ok || f.Group == "" {
			t.Fatalf("%s: page %q group %q", f.Path, f.Section, f.Group)
		}
		if at < page {
			t.Fatalf("%s: page %s comes after a later page", f.Path, f.Section)
		}
		page = at
		k := key{f.Section, f.Group}
		if k != last && seen[k] {
			t.Fatalf("%s: group %q of %s is split", f.Path, f.Group, f.Section)
		}
		seen[k], last = true, k
	}
	// An extra row ends a group that has fields, or makes its own.
	for _, e := range Extras {
		if _, ok := pages[e.Section]; !ok || e.Group == "" {
			t.Fatal(e)
		}
	}
}

func TestEnabledWhenNamesASwitchOnTheSamePage(t *testing.T) {
	n := 0
	for _, f := range Fields {
		for _, c := range f.EnabledWhen {
			n++
			sw := FieldByPath(c.Key)
			if sw == nil || sw.Kind.Type != KindBool || sw.Section != f.Section || !slices.Equal(c.In, []string{"true"}) {
				t.Fatalf("%s: %+v", f.Path, c)
			}
		}
	}
	if n == 0 {
		t.Fatal("no field depends on a switch")
	}
	// Under a switch that's off: the workers' settings, the coordinator's.
	for _, p := range []string{"agent.workers.max", "agent.projectsRoot", "agent.coordinatorKind", "notifications.bubble.popSecs", "voice.noiseModel"} {
		if len(FieldByPath(p).EnabledWhen) == 0 {
			t.Fatal(p)
		}
	}
}

func TestANumbersUnitIsShownNextToIt(t *testing.T) {
	views := Views(OptionSources{})
	for _, v := range views {
		if v.Path != "voice.idleTimeoutSecs" {
			continue
		}
		b, _ := json.Marshal(v.Kind)
		if string(b) != `{"type":"int","min":1,"max":600,"menu":false,"unit":"seconds"}` || v.Label != "Silence timeout" {
			t.Fatal(string(b), v.Label)
		}
	}
	// No label carries its unit any more.
	for _, f := range Fields {
		for _, u := range []string{"(seconds)", "(minutes", "(MB)"} {
			if contains(f.Label, u) {
				t.Fatal(f.Label)
			}
		}
	}
}

func TestTheSnapshotPlacesTheExtraRows(t *testing.T) {
	got := ExtraViews(Macos)
	want := []ExtraView{
		{"resetPosition", SectionGeneral, "Look"},
		{"key.openai", SectionVoice, "Voice service"},
		{"key.gemini", SectionVoice, "Voice service"},
		{"restartCoordinator", SectionCoordinator, "Coordinator"},
		{"pix", SectionSessions, "Sessions you start with pix"},
		{"openFile", SectionAdvanced, "Files"},
		{"exportLogs", SectionAdvanced, "Files"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	b, _ := json.Marshal(got[0])
	if string(b) != `{"id":"resetPosition","section":"General","group":"Look"}` {
		t.Fatal(string(b))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
