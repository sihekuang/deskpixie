package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupProgramsFindsEachOnThePathOrInTheInstallDirs(t *testing.T) {
	have := map[string]bool{
		"/usr/bin/tmux":              true,
		"/opt/homebrew/bin/herdr":    true,
		"/Users/x/.local/bin/claude": true,
	}
	got := FindSetupPrograms("/Users/x", "/usr/bin:/bin", "", "", func(p string) bool { return have[p] })
	want := SetupPrograms{Herdr: "/opt/homebrew/bin/herdr", Tmux: "/usr/bin/tmux", Claude: "/Users/x/.local/bin/claude"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A configured program is the only place looked at: the session host would not use another.
func TestSetupProgramsTakesOnlyTheConfiguredHerdrAndTmux(t *testing.T) {
	have := map[string]bool{"/opt/homebrew/bin/herdr": true, "/usr/bin/tmux": true, "/custom/tmux": true}
	got := FindSetupPrograms("/Users/x", "/usr/bin", "/custom/herdr", "/custom/tmux", func(p string) bool { return have[p] })
	if got.Herdr != "" || got.Tmux != "/custom/tmux" {
		t.Fatalf("%+v", got)
	}
}

func TestSetupProgramsRPCAnswersEachProgram(t *testing.T) {
	h := newHarness(t)
	configured := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(configured, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.call("settings.set", map[string]any{"field": "agent.herdrPath", "value": configured})
	var reply map[string]string
	if err := json.Unmarshal(h.call("setup.programs", nil), &reply); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"herdr", "tmux", "claude", "codex"} {
		if _, ok := reply[k]; !ok {
			t.Fatalf("no %q in %v", k, reply)
		}
	}
	if reply["herdr"] != configured {
		t.Fatalf("herdr = %q, want the configured %q", reply["herdr"], configured)
	}
}
