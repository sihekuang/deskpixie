package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sihekuang/deskpixie/core/settings"
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

// #216: the window checks the projects folder without changing anything, and Create makes it.
func TestProjectsFolderRPCChecksThenCreates(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	h := newHarness(t)
	h.call("settings.set", map[string]any{"field": "agent.projectsRoot", "value": "~/Documents/dp-rpc"})
	want := filepath.Join(home, "Documents", "dp-rpc")
	var check settings.ProjectsFolder
	if err := json.Unmarshal(h.call("setup.projectsFolder", nil), &check); err != nil {
		t.Fatal(err)
	}
	if check.State != settings.FolderMissing || check.Path != want ||
		check.Fallback != filepath.Join(h.dir, "agent", "projects") {
		t.Fatalf("%+v", check)
	}
	if _, err := os.Stat(want); err == nil {
		t.Fatal("the check made the folder")
	}
	var made settings.ProjectsFolder
	if err := json.Unmarshal(h.call("setup.createProjectsFolder", nil), &made); err != nil {
		t.Fatal(err)
	}
	if made.State != settings.FolderReady || made.Path != want {
		t.Fatalf("%+v", made)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Fatal("not created")
	}
}
