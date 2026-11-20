package proc

import (
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

func TestServerEnvScrubsDropsExtendsPathAndSorts(t *testing.T) {
	parent := []agent.EnvVar{
		{Key: "ZED", Value: "z"}, {Key: "ANTHROPIC_API_KEY", Value: "x"}, {Key: "OPENAI_API_KEY", Value: "x"},
		{Key: "CLAUDECODE", Value: "1"}, {Key: "TMUX", Value: "/tmp/sock"}, {Key: "PATH", Value: "/usr/bin:/opt/homebrew/bin"},
		{Key: "HOME", Value: "/Users/me"},
	}
	got := ServerEnv(parent, "/Users/me", []string{"TMUX"}, agent.EnvVar{Key: "EXTRA", Value: "1"})
	want := []agent.EnvVar{
		{Key: "EXTRA", Value: "1"}, {Key: "HOME", Value: "/Users/me"},
		{Key: "PATH", Value: "/usr/bin:/opt/homebrew/bin:/Users/me/.local/bin:/usr/local/bin"},
		{Key: "ZED", Value: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if got := ServerEnv(nil, "/h", nil); got[0].Value != "/usr/bin:/bin:/usr/sbin:/sbin:/h/.local/bin:/opt/homebrew/bin:/usr/local/bin" {
		t.Fatalf("%v", got)
	}
}

func TestFindProgramPrefersConfiguredThenPathThenInstallDirs(t *testing.T) {
	exists := func(p string) bool { return p == "/a/tmux" || p == "/opt/homebrew/bin/tmux" }
	if p, ok := FindProgram("tmux", "/x/tmux", "/a", nil, exists); ok || p != "/x/tmux" {
		t.Fatal(p, ok)
	}
	if p, ok := FindProgram("tmux", "", "/b:/a", []string{"/opt/homebrew/bin"}, exists); !ok || p != "/a/tmux" {
		t.Fatal(p, ok)
	}
	if p, ok := FindProgram("tmux", "", "/b", []string{"/opt/homebrew/bin"}, exists); !ok || p != "/opt/homebrew/bin/tmux" {
		t.Fatal(p, ok)
	}
	if _, ok := FindProgram("tmux", "", "", nil, exists); ok {
		t.Fatal("none")
	}
}

func TestBothAgentTokenNamesAreScrubbed(t *testing.T) {
	for _, k := range []string{"PIXIE_AGENT_TOKEN", "MASCOT_AGENT_TOKEN"} {
		if !Scrubbed(k) {
			t.Fatalf("%s passed on", k)
		}
	}
	got := ServerEnv([]agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "a"}, {Key: "MASCOT_AGENT_TOKEN", Value: "b"}, {Key: "HOME", Value: "/h"}}, "/h", nil)
	for _, e := range got {
		if e.Key == "PIXIE_AGENT_TOKEN" || e.Key == "MASCOT_AGENT_TOKEN" {
			t.Fatalf("%v", got)
		}
	}
}

// #461: DeskPixie started from inside a pix session must not hand its PIX_* on to Herdr's or
// tmux's server: a pix resumed in one of its tabs would see PIX_SESSION and run unwrapped. Only
// a real PIX_ prefix: PIXIE_* stays as it is (its token aside, scrubbed by name).
func TestPixVariablesAreScrubbed(t *testing.T) {
	for _, k := range []string{"PIX_SESSION", "PIX_SOCK", "PIX_WHERE", "PIX_KEYLOG"} {
		if !Scrubbed(k) {
			t.Fatalf("%s passed on", k)
		}
	}
	for _, k := range []string{"PIX", "PIXIE_HOME", "DESKPIXIE_PIX_SOCK", "XPIX_SESSION"} {
		if Scrubbed(k) {
			t.Fatalf("%s scrubbed", k)
		}
	}
}
