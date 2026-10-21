package settings

// Ported from ai-mascot src-tauri/src/settings/validate.rs (14 tests, all ported), plus the
// hotkey grammar cases that Rust got from the global-hotkey crate.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testEnv is a fresh temp home holding Documents/Projects (the default projects root), on
// macOS, with the packs and registry names the defaults use.
func testEnv(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Documents", "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Env{
		Home:     home,
		Platform: Macos,
		Sources: OptionSources{
			Packs:     []Pair{{"bit", "Bit"}, {"dino", "Dino"}},
			Providers: []string{"openai-realtime", "gemini-live"},
			Audio:     []string{"macos-vpio"},
			Hosts:     []string{"herdr", "tmux"},
			Terminals: []string{"warp", "iterm"},
			Inputs:    []Pair{{"BuiltInMic", "MacBook Pro Microphone"}},
			Outputs:   []Pair{{"BuiltInSpeaker", "MacBook Pro Speakers"}},
		},
	}
}

func problems(c Config, env Env) [][2]string {
	var out [][2]string
	for _, e := range Validate(c, env) {
		out = append(out, [2]string{e.Path, e.Message})
	}
	return out
}

func valid(c Config, env Env) bool { return len(Validate(c, env)) == 0 }

func TestTheDefaultsAreValid(t *testing.T) {
	if p := problems(Default(), testEnv(t)); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestScaleIs1To16(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		n  uint32
		ok bool
	}{{1, true}, {16, true}, {0, false}, {17, false}} {
		c := Default()
		c.Scale = tc.n
		if valid(c, env) != tc.ok {
			t.Fatalf("scale %d", tc.n)
		}
	}
	c := Default()
	c.Scale = 17
	if p := problems(c, env); !reflect.DeepEqual(p, [][2]string{{"scale", RangeMessage(1, 16)}}) {
		t.Fatal(p)
	}
}

func TestWorkersMaxIs1To4(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		n  uint64
		ok bool
	}{{1, true}, {4, true}, {0, false}, {5, false}} {
		c := Default()
		c.Agent.Workers.Max = tc.n
		if valid(c, env) != tc.ok {
			t.Fatalf("max %d", tc.n)
		}
	}
}

func TestThePortIs1024To65535(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		n  uint16
		ok bool
	}{{1024, true}, {65535, true}, {1023, false}, {80, false}} {
		c := Default()
		c.Agent.Port = tc.n
		if valid(c, env) != tc.ok {
			t.Fatalf("port %d", tc.n)
		}
	}
}

func TestTheFadeRadiusIsAWholeNumberFrom0To1000(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		r  float64
		ok bool
	}{{0, true}, {1000, true}, {1001, false}, {10.5, false}, {-1, false}} {
		c := Default()
		c.ProximityFade.Radius = tc.r
		if valid(c, env) != tc.ok {
			t.Fatalf("radius %v", tc.r)
		}
	}
}

func TestSessionNamesFollowTheHerdrRule(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"a", true}, {"ai-mascot", true}, {"deskpixie", true}, {strings.Repeat("a", 48), true},
		{strings.Repeat("a", 49), false}, {"default", false}, {"Ai", false}, {"-a", false}, {"", false},
	} {
		c := Default()
		c.Agent.Session = tc.name
		if valid(c, env) != tc.ok {
			t.Fatalf("session %q", tc.name)
		}
	}
}

func TestTheHotkeyMustParse(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		combo string
		ok    bool
	}{{"ctrl+alt+Space", true}, {"shift+super+KeyK", true}, {"F5", true}, {"ctrl+alt+", false}, {"ctrl+Nope", false}, {"", false}} {
		c := Default()
		c.Hotkey = tc.combo
		if valid(c, env) != tc.ok {
			t.Fatalf("hotkey %q", tc.combo)
		}
	}
}

// Go-only: the rest of the global-hotkey grammar Rust relied on (tauri-plugin-global-shortcut
// parses with global_hotkey::hotkey::parse_hotkey).
func TestHotkeyGrammarFollowsGlobalHotkey(t *testing.T) {
	for _, tc := range []struct {
		combo string
		ok    bool
	}{
		{"CTRL+ALT+SPACE", true}, {"cmd+shift+k", true}, {"Option+Command+Digit1", true}, {"control+F20", true},
		{"CmdOrCtrl+KeyA", true}, {"ctrl + alt + Space", true}, {"ArrowUp", true}, {"ctrl+Escape", true},
		{"ctrl+alt", false}, {"ctrl", false}, {"ctrl+KeyA+KeyB", false}, {"ctrl+KeyA+shift", false},
		{"ctrl++KeyA", false}, {"F25", false}, {"hyper+KeyA", false},
	} {
		if err := CheckHotkey(tc.combo); (err == nil) != tc.ok {
			t.Fatalf("hotkey %q: %v", tc.combo, err)
		}
	}
}

// A folder below home that doesn't exist yet is fine: the agent creates it at start (#216).
func TestTheProjectsRootIsAFolderBelowHomeExistingOrNot(t *testing.T) {
	env := testEnv(t)
	outside := os.TempDir()
	for _, tc := range []struct {
		root string
		ok   bool
	}{{"~/Documents/Projects", true}, {"~/Documents/deskpixie", true}, {"~/missing/deeper", true}, {"~", false},
		{outside, false}, {filepath.Join(outside, "new-folder"), false}, {"", false}, {"~other/code", false}} {
		c := Default()
		c.Agent.Workers.Enabled = true // the on-disk check
		c.Agent.ProjectsRoot = tc.root
		if valid(c, env) != tc.ok {
			t.Fatalf("projectsRoot %q", tc.root)
		}
	}
	c := Default()
	c.Agent.Workers.Enabled = true // the on-disk check
	c.Agent.ProjectsRoot = outside
	if p := problems(c, env); !reflect.DeepEqual(p, [][2]string{{"agent.projectsRoot", BadProjectsRoot}}) {
		t.Fatal(p)
	}
}

// With workers off only the name is checked: nothing on disk is looked at.
func TestWithWorkersOffOnlyTheProjectsRootsNameIsChecked(t *testing.T) {
	env := testEnv(t)
	if err := os.WriteFile(filepath.Join(env.Home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for raw, ok := range map[string]bool{"~/notes.txt": true, "~/anything/new": true, "~": false, "/elsewhere": false, "": false} {
		c := Default()
		c.Agent.Workers.Enabled = false
		c.Agent.ProjectsRoot = raw
		if valid(c, env) != ok {
			t.Errorf("projectsRoot %q: want valid=%v", raw, ok)
		}
	}
}

// A missing folder behind a link out of home isn't below home, whatever its name says.
func TestAMissingProjectsRootBehindALinkOutOfHomeIsRefused(t *testing.T) {
	env := testEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(env.Home, "away")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.Home, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(env.Home, "inside"), filepath.Join(env.Home, "near")); err != nil {
		t.Fatal(err)
	}
	for raw, ok := range map[string]bool{"~/away/sub": false, "~/away/a/b": false, "~/near/sub": true} {
		c := Default()
		c.Agent.Workers.Enabled = true // the on-disk check
		c.Agent.ProjectsRoot = raw
		if valid(c, env) != ok {
			t.Errorf("projectsRoot %q: want valid=%v", raw, ok)
		}
	}
}

func TestAFileIsNotAProjectsRoot(t *testing.T) {
	env := testEnv(t)
	os.WriteFile(filepath.Join(env.Home, "notes.txt"), []byte("x"), 0o644)
	c := Default()
	c.Agent.Workers.Enabled = true // the on-disk check
	c.Agent.ProjectsRoot = "~/notes.txt"
	if p := problems(c, env); !reflect.DeepEqual(p, [][2]string{{"agent.projectsRoot", BadProjectsRoot}}) {
		t.Fatal(p)
	}
}

func TestAProjectsRootSymlinkThatLeavesHomeIsRefused(t *testing.T) {
	env := testEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(env.Home, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Agent.Workers.Enabled = true // the on-disk check
	c.Agent.ProjectsRoot = "~/elsewhere"
	found := problems(c, env)
	if !reflect.DeepEqual(found, [][2]string{{"agent.projectsRoot", BadProjectsRoot}}) {
		t.Fatal(found)
	}
	if strings.Contains(found[0][1], "/") {
		t.Fatal("no path in the message")
	}
}

func TestPersonaAndHerdrPathsMustBeFilesWhenSet(t *testing.T) {
	env := testEnv(t)
	file := filepath.Join(env.Home, "persona.md")
	os.WriteFile(file, []byte("You are Pixie."), 0o644)
	c := Default()
	c.Voice.PersonaPath = ptr(file)
	c.Agent.HerdrPath = ptr(file)
	c.Agent.TmuxPath = ptr(file)
	if !valid(c, env) {
		t.Fatal(problems(c, env))
	}
	c.Voice.PersonaPath = ptr(filepath.Join(env.Home, "nope.md"))
	c.Agent.HerdrPath = ptr(filepath.Join(env.Home, "Documents"))
	c.Agent.TmuxPath = ptr(filepath.Join(env.Home, "nope"))
	want := [][2]string{{"voice.personaPath", NoFile}, {"agent.herdrPath", NoFile}, {"agent.tmuxPath", NoFile}}
	if p := problems(c, env); !reflect.DeepEqual(p, want) {
		t.Fatal(p)
	}
}

func TestEnabledWorkersNeedAKind(t *testing.T) {
	env := testEnv(t)
	c := Default()
	c.Agent.Workers.Kinds = []string{}
	if !valid(c, env) {
		t.Fatal("off workers may have no kind")
	}
	c.Agent.Workers.Enabled = true
	if p := problems(c, env); len(p) == 0 || p[0][0] != "agent.workers.kinds" {
		t.Fatal(p)
	}
	c.Agent.Workers.Kinds = []string{"claude"}
	if !valid(c, env) {
		t.Fatal(problems(c, env))
	}
}

func TestADeviceIsTheDefaultOrAnyUIDConnectedOrNot(t *testing.T) {
	env := testEnv(t)
	c := Default()
	if !valid(c, env) {
		t.Fatal("null is System Default")
	}
	c.Voice.InputDevice = ptr("BuiltInMic")
	c.Voice.OutputDevice = ptr("BuiltInSpeaker")
	if !valid(c, env) {
		t.Fatal(problems(c, env))
	}
	// A hand edit naming a device that isn't connected (and isn't the saved one) is kept.
	c.Voice.InputDevice = ptr("hand-edited-unplugged")
	c.Voice.OutputDevice = ptr("")
	if !valid(c, env) {
		t.Fatal(problems(c, env))
	}
}

func TestChoicesMustBeAvailableOnThisPlatform(t *testing.T) {
	env := testEnv(t)
	c := Default()
	c.Pack = "zed"
	if p := problems(c, env); !reflect.DeepEqual(p, [][2]string{{"pack", NotAChoice}}) {
		t.Fatal(p)
	}
	linux := testEnv(t)
	linux.Platform = Linux
	found := problems(Default(), linux)
	has := func(want [2]string) bool {
		for _, f := range found {
			if f == want {
				return true
			}
		}
		return false
	}
	if !has([2]string{"voice.audio", NotOnPlatform}) || !has([2]string{"agent.terminal", NotOnPlatform}) {
		t.Fatal(found)
	}
	c = Default()
	c.Agent.Workers.Kinds = []string{"claude", "gpt"}
	if p := problems(c, env); !reflect.DeepEqual(p, [][2]string{{"agent.workers.kinds", NotAChoice}}) {
		t.Fatal(p)
	}
}

func TestNotificationRanges(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct {
		name string
		set  func(c *Config, n uint64)
		n    uint64
		ok   bool
	}{
		{"bubble lines 11", func(c *Config, n uint64) { c.Notifications.Bubble.Lines = n }, 11, false},
		{"bubble lines 10", func(c *Config, n uint64) { c.Notifications.Bubble.Lines = n }, 10, true},
		{"debounce 0", func(c *Config, n uint64) { c.Notifications.DebounceSecs = n }, 0, true},
		{"debounce 301", func(c *Config, n uint64) { c.Notifications.DebounceSecs = n }, 301, false},
		{"pop 0", func(c *Config, n uint64) { c.Notifications.Bubble.PopSecs = n }, 0, false},
		{"away 61", func(c *Config, n uint64) { c.Notifications.AwayAfterMins = n }, 61, false},
	} {
		c := Default()
		tc.set(&c, tc.n)
		if valid(c, env) != tc.ok {
			t.Fatal(tc.name)
		}
	}
}

func TestTmuxIsAValidSessionHost(t *testing.T) {
	env := testEnv(t)
	c := Default()
	c.Agent.SessionHost = "tmux"
	if !valid(c, env) {
		t.Fatal(problems(c, env))
	}
	c.Agent.SessionHost = "screen"
	if valid(c, env) {
		t.Fatal("not a host")
	}
}
