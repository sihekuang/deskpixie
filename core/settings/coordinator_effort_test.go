package settings

import (
	"reflect"
	"testing"
)

// #177: the coordinator thinks at low effort by default, so a hand-off goes out sooner.

func TestTheCoordinatorEffortDefaultsToLow(t *testing.T) {
	if d := Default(); d.Agent.CoordinatorEffort != "low" {
		t.Fatalf("%q", d.Agent.CoordinatorEffort)
	}
	// A settings file from before #177 has no key: it loads as low.
	c, err := ParseConfig([]byte(`{"agent":{"enabled":true}}`))
	if err != nil || c.Agent.CoordinatorEffort != "low" {
		t.Fatal(c.Agent.CoordinatorEffort, err)
	}
}

func TestTheCoordinatorEffortPickerOffersTheLevels(t *testing.T) {
	f := FieldByPath("agent.coordinatorEffort")
	if f == nil || f.Section != SectionCoordinator || f.Apply != ApplyRestart {
		t.Fatalf("%+v", f)
	}
	var values []string
	for _, o := range f.Kind.Choices.Static {
		values = append(values, o.Value)
	}
	if f.Kind.Type != KindChoice || f.Kind.Choices.Open || !reflect.DeepEqual(values, []string{"", "low", "medium", "high"}) {
		t.Fatalf("%+v", f.Kind)
	}
	c := Default()
	c.Agent.CoordinatorEffort = "max"
	found := false
	for _, e := range Validate(c, Env{Home: t.TempDir(), Platform: Macos}) {
		found = found || e.Path == "agent.coordinatorEffort" && e.Message == NotAChoice
	}
	if !found {
		t.Fatal("an unknown effort is not flagged")
	}
}
