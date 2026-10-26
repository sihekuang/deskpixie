package settings

import "testing"

// #340: agent.markForcedSends is on by default, a switch in Sessions, applied live through
// its own route.
func TestMarkForcedSendsIsALiveSwitchOnByDefault(t *testing.T) {
	if !Default().Agent.MarkForcedSends {
		t.Fatal("off by default")
	}
	f := FieldByPath("agent.markForcedSends")
	if f == nil || f.Kind.Type != KindBool || f.Apply != ApplyLive || f.Section != SectionSessions || f.Label == "" || f.Help == "" {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("agent.markForcedSends"); !ok || r != RouteForcedSends {
		t.Fatalf("route %v %v", r, ok)
	}
}
