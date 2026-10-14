package settings

// Ported from ai-mascot src-tauri/src/settings/live.rs (2 tests, both ported). The applier
// itself is P14's; the route table lives here so it is checked against Fields.

import "testing"

func TestEveryLiveFieldHasARouteAndRestartFieldsHaveNone(t *testing.T) {
	for _, f := range Fields {
		_, ok := RouteFor(f.Path)
		switch {
		case f.Kind.Type == KindManaged:
		case f.Apply == ApplyLive && !ok:
			t.Fatalf("%s is Live but has no route", f.Path)
		case f.Apply == ApplyRestart && ok:
			t.Fatalf("%s is Restart but has a route", f.Path)
		}
	}
	if r, ok := RouteFor("position"); !ok || r != RouteResetPosition {
		t.Fatal("position")
	}
	if _, ok := RouteFor("canvas.window"); ok {
		t.Fatal("canvas.window")
	}
}

func TestEachLiveFieldTakesTheTrayPath(t *testing.T) {
	for path, want := range map[string]Route{
		"hotkey":                RouteHotkey,
		"pack":                  RoutePack,
		"scale":                 RouteScale,
		"proximityFade.enabled": RouteReadEachTick,
		"proximityFade.radius":  RouteReadEachTick,
		"canvas.autoOpen":       RouteAutoOpen,
		"voice.noiseFilter":     RouteNoiseFilter,
		"voice.inputDevice":     RouteAudioDevices,
		"voice.outputDevice":    RouteAudioDevices,
	} {
		if r, ok := RouteFor(path); !ok || r != want {
			t.Fatalf("%s → %v", path, r)
		}
	}
}
