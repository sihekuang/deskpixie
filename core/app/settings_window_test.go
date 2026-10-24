package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// #288: Pixie's open_settings finds the setting with the settings search and has Swift show it
// (settings.open); the coordinator gets the same tool.
func TestOpenSettingsShowsTheSettingInTheWindow(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	tool, ok := h.app.voiceTools.Get("open_settings")
	if !ok {
		t.Fatal("Pixie has no open_settings")
	}
	out := tool.Invoke(context.Background(), json.RawMessage(`{"query":"noise filter"}`))
	if !strings.Contains(string(out.Output), `"setting":"Noise filter"`) || !strings.Contains(string(out.Output), `"page":"Voice"`) {
		t.Fatal(string(out.Output))
	}
	ev := h.waitFor("settings.open", nil, time.Second)
	if ev["path"] != "voice.noiseFilter" || ev["section"] != "Voice" || ev["label"] != "Noise filter" || ev["help"] == "" {
		t.Fatal(ev)
	}

	// The coordinator's page is named as the sidebar names it.
	out = tool.Invoke(context.Background(), json.RawMessage(`{"query":"coordinator effort"}`))
	if !strings.Contains(string(out.Output), `"page":"Coordinator"`) {
		t.Fatal(string(out.Output))
	}

	found := false
	for _, lt := range f.hooks.LocalTools {
		found = found || lt.Name() == "open_settings"
	}
	if !found {
		t.Fatal("the coordinator has no open_settings")
	}
}

func TestOpenSettingsWithNothingToShowJustOpensTheWindow(t *testing.T) {
	h := newHarness(t)
	tool, _ := h.app.voiceTools.Get("open_settings")
	for _, args := range []string{`{}`, `{"query":"zebra"}`, `{"query":"backend model"}`} {
		h.take()
		// backend model: GPT-Live's, hidden with the default voice service.
		_ = tool.Invoke(context.Background(), json.RawMessage(args))
		ev := h.waitFor("settings.open", nil, time.Second)
		if _, has := ev["path"]; has {
			t.Fatal(args, ev)
		}
	}
}
