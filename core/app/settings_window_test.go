package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/tools"
)

// coordinatorTool is the coordinator's local tool name, or fails.
func coordinatorTool(t *testing.T, f *fakeAgent, name string) tools.Tool {
	t.Helper()
	for _, lt := range f.hooks.LocalTools {
		if lt.Name() == name {
			return lt
		}
	}
	t.Fatalf("the coordinator has no %s", name)
	return nil
}

// invoke runs a local tool and gives its output.
func invoke(t *testing.T, tool tools.Tool, args string) string {
	t.Helper()
	out, _, err := tool.Invoke(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// #288: the coordinator's open_settings finds the setting with the settings search and has Swift
// show it (settings.open). Pixie doesn't have it: it hands settings questions to the coordinator.
func TestOpenSettingsShowsTheSettingInTheWindow(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	if _, ok := h.app.voiceTools.Get("open_settings"); ok {
		t.Fatal("Pixie has open_settings; only the coordinator should")
	}
	tool := coordinatorTool(t, f, "open_settings")
	out := invoke(t, tool, `{"query":"noise filter"}`)
	if !strings.Contains(out, `"setting":"Noise filter"`) || !strings.Contains(out, `"page":"Voice"`) {
		t.Fatal(out)
	}
	ev := h.waitFor("settings.open", nil, time.Second)
	if ev["path"] != "voice.noiseFilter" || ev["section"] != "Voice" || ev["label"] != "Noise filter" || ev["help"] == "" {
		t.Fatal(ev)
	}

	// The coordinator's page is named as the sidebar names it.
	out = invoke(t, tool, `{"query":"coordinator effort"}`)
	if !strings.Contains(out, `"page":"Coordinator"`) {
		t.Fatal(out)
	}
}

func TestOpenSettingsWithNothingToShowJustOpensTheWindow(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	tool := coordinatorTool(t, f, "open_settings")
	for _, args := range []string{`{}`, `{"query":"zebra"}`, `{"query":"backend model"}`} {
		h.take()
		// backend model: GPT-Live's, hidden with the default voice service.
		_ = invoke(t, tool, args)
		ev := h.waitFor("settings.open", nil, time.Second)
		if _, has := ev["path"]; has {
			t.Fatal(args, ev)
		}
	}
}
