package settings

// Ported from ai-mascot src-tauri/src/config.rs (24 tests, all ported).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func mustJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func parseConfig(t *testing.T, text string) Config {
	t.Helper()
	c, err := ParseConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tmpFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "settings.json")
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestVoiceDefaultsMatchTheSpec(t *testing.T) {
	v := Default().Voice
	if v.Provider != "openai-realtime" || v.Audio != "macos-vpio" || v.Model != "gpt-realtime" || v.Voice != "marin" {
		t.Fatalf("%+v", v)
	}
	if !v.Chimes || v.IdleTimeoutSecs != 7 || v.PersonaPath != nil {
		t.Fatalf("%+v", v)
	}
	if v.NoiseFilter {
		t.Fatal("the noise filter is off by default")
	}
	if v.NoiseModel != "hush" {
		t.Fatal("Hush is the default noise model")
	}
	if v.NoisyRoom {
		t.Fatal("noisy room is off by default")
	}
	if v.InputDevice != nil || v.OutputDevice != nil {
		t.Fatal("the system default mic and speaker")
	}
}

func TestSettingsWithoutAVoiceBlockGetDefaults(t *testing.T) {
	c := parseConfig(t, `{"hotkey":"ctrl+alt+Space"}`)
	if !reflect.DeepEqual(c.Voice, Default().Voice) {
		t.Fatalf("%+v", c.Voice)
	}
}

func TestVoiceBlockUsesCamelCase(t *testing.T) {
	j := mustJSON(t, Default())
	voice := j["voice"].(map[string]any)
	if voice["idleTimeoutSecs"] != float64(7) {
		t.Fatal(voice)
	}
	if v, ok := voice["personaPath"]; !ok || v != nil {
		t.Fatal("personaPath must be null")
	}
	if voice["noiseFilter"] != false || voice["noisyRoom"] != false {
		t.Fatal(voice)
	}
	if v, ok := voice["inputDevice"]; !ok || v != nil {
		t.Fatal("inputDevice must be null")
	}
	if v, ok := voice["outputDevice"]; !ok || v != nil {
		t.Fatal("outputDevice must be null")
	}
	on := parseConfig(t, `{"voice":{"noiseFilter":true}}`)
	if !on.Voice.NoiseFilter {
		t.Fatal("noiseFilter")
	}
	noisy := parseConfig(t, `{"voice":{"noisyRoom":true}}`)
	if !noisy.Voice.NoisyRoom {
		t.Fatal("noisyRoom")
	}
	pinned := parseConfig(t, `{"voice":{"inputDevice":"BuiltInMicrophoneDevice","outputDevice":"USB-1"}}`)
	if *pinned.Voice.InputDevice != "BuiltInMicrophoneDevice" || *pinned.Voice.OutputDevice != "USB-1" {
		t.Fatal(pinned.Voice)
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	c := Default()
	if c.Hotkey != "ctrl+alt+Space" || c.Pack != "bit" || c.Scale != 8 || c.Position != nil {
		t.Fatalf("%+v", c)
	}
	if c.ProximityFade != (Fade{Enabled: false, Radius: 150}) {
		t.Fatalf("%+v", c.ProximityFade)
	}
}

func TestMissingFileGivesDefaultsAndCreatesIt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "settings.json")
	if !Load(p).Equal(Default()) {
		t.Fatal("not defaults")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// Rust's corrupt_file_gives_defaults_and_is_rewritten, deliberately changed: rewriting a file
// with one typo lost every setting (final review I1). The defaults run in memory; the file is
// left for the user to fix, and LoadFile says why it can't be used.
func TestCorruptFileGivesDefaultsAndIsLeftAlone(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte("{ not json"), 0o644)
	c, err := LoadFile(p)
	if !c.Equal(Default()) || err == nil {
		t.Fatal("not defaults with an error", err)
	}
	if !Load(p).Equal(Default()) {
		t.Fatal("not defaults")
	}
	if text, _ := os.ReadFile(p); string(text) != "{ not json" {
		t.Fatalf("broken file was rewritten: %s", text)
	}
}

func TestAMistypedFileIsLeftAlone(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"scale":"big","pack":"blob"}`), 0o644)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("no error")
	}
	if text, _ := os.ReadFile(p); string(text) != `{"scale":"big","pack":"blob"}` {
		t.Fatalf("rewritten: %s", text)
	}
}

func TestPartialFileFillsInDefaults(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"pack":"blob","proximityFade":{"radius":90}}`), 0o644)
	c := Load(p)
	if c.Pack != "blob" || c.Hotkey != "ctrl+alt+Space" {
		t.Fatalf("%+v", c)
	}
	if c.ProximityFade != (Fade{Enabled: false, Radius: 90}) {
		t.Fatalf("%+v", c.ProximityFade)
	}
}

func TestZeroScaleIsReplacedByDefault(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"scale":0}`), 0o644)
	if Load(p).Scale != 8 {
		t.Fatal("scale")
	}
}

func TestHugeScaleIsCapped(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"scale":4000}`), 0o644)
	if Load(p).Scale != MaxScale {
		t.Fatal("cap")
	}
	os.WriteFile(p, []byte(`{"scale":16}`), 0o644)
	if Load(p).Scale != MaxScale {
		t.Fatal("16")
	}
}

func TestEveryTraySizeIsWithinTheCap(t *testing.T) {
	for _, s := range SizePresets {
		if s.Scale < 1 || s.Scale > MaxScale {
			t.Fatalf("%+v", s)
		}
	}
}

func TestCanvasDefaultsMatchTheSpec(t *testing.T) {
	c := Default().Canvas
	if !c.AutoOpen || c.Keep != 50 || c.Window != nil {
		t.Fatalf("%+v", c)
	}
}

func TestSettingsWithoutACanvasBlockGetDefaults(t *testing.T) {
	c := parseConfig(t, `{"pack":"bit"}`)
	if !reflect.DeepEqual(c.Canvas, Default().Canvas) {
		t.Fatalf("%+v", c.Canvas)
	}
}

func TestCanvasBlockUsesCamelCase(t *testing.T) {
	j := mustJSON(t, Default())
	want := map[string]any{"autoOpen": true, "keep": float64(50), "window": nil}
	if !reflect.DeepEqual(j["canvas"], want) {
		t.Fatal(j["canvas"])
	}
}

func TestCanvasWindowRoundTrips(t *testing.T) {
	p := tmpFile(t)
	c := Default()
	c.Canvas.Window = &CanvasWindow{X: 80, Y: 60.5, Width: 960, Height: 720}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Load(p).Canvas, c.Canvas) {
		t.Fatal("canvas")
	}
}

func TestAgentDefaultsMatchTheSpec(t *testing.T) {
	j := mustJSON(t, Default())
	// DeskPixie rename (spec §6): the default session is "deskpixie", not "ai-mascot".
	want := map[string]any{
		"enabled": true, "sessionHost": "herdr", "session": "deskpixie", "terminal": "warp",
		"herdrPath": nil, "tmuxPath": nil, "coordinatorKind": "claude", "coordinatorEffort": "low", "model": nil, "codexModel": nil, "warm": false, "port": float64(48217), "keepPanesOnQuit": true,
		"holdOpenSecs": float64(180), "permissionTimeoutSecs": float64(120), "notifyPerMinute": float64(4), "narration": true, "openConversationForUpdates": false, "alwaysForceSend": false,
		"markForcedSends":  true, // #340
		"questionWaitSecs": float64(20), "editor": "auto", "projectsRoot": "~/Documents/deskpixie",
		"workers": map[string]any{"enabled": false, "max": float64(4), "kinds": []any{"claude", "codex"}, "speakResults": false},
	}
	if !reflect.DeepEqual(j["agent"], want) {
		t.Fatalf("%v", j["agent"])
	}
}

func TestSettingsWithoutAnAgentBlockGetDefaults(t *testing.T) {
	c := parseConfig(t, `{"pack":"bit"}`)
	if !reflect.DeepEqual(c.Agent, Default().Agent) {
		t.Fatal("agent")
	}
	c = parseConfig(t, `{"agent":{"terminal":"iterm"}}`)
	if c.Agent.Terminal != "iterm" || c.Agent.Port != 48217 {
		t.Fatalf("%+v", c.Agent)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	p := tmpFile(t)
	c := Default()
	c.Position = &[2]float64{120.5, 300}
	c.Pack = "blob"
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	if !Load(p).Equal(c) {
		t.Fatal("round trip")
	}
}

func TestUnknownKeysSurviveASave(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"pack":"blob","futureThing":{"a":1}}`), 0o644)
	c := Load(p)
	var ft map[string]any
	json.Unmarshal(c.Extra["futureThing"], &ft)
	if ft["a"] != float64(1) {
		t.Fatal(c.Extra)
	}
	c.Scale = 4
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(p)
	var saved map[string]any
	json.Unmarshal(text, &saved)
	if saved["futureThing"].(map[string]any)["a"] != float64(1) || saved["scale"] != float64(4) || saved["pack"] != "blob" {
		t.Fatal(saved)
	}
}

func TestASaveKeepsTheFilePermissionsAndLeavesNoTempFile(t *testing.T) {
	p := tmpFile(t)
	Default().Save(p)
	os.Chmod(p, 0o600)
	c := Default()
	c.Scale = 5
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if got := dirNames(t, filepath.Dir(p)); !reflect.DeepEqual(got, []string{"settings.json"}) {
		t.Fatal(got)
	}
	if Load(p).Scale != 5 {
		t.Fatal("scale")
	}
}

func TestASymlinkedSettingsFileStaysASymlinkAndItsTargetIsUpdated(t *testing.T) {
	p := tmpFile(t)
	realDir := filepath.Join(filepath.Dir(p), "dotfiles")
	os.MkdirAll(realDir, 0o755)
	real := filepath.Join(realDir, "settings.json")
	Default().Save(real)
	if err := os.Symlink(real, p); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Scale = 5
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Lstat(p)
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if link, _ := os.Readlink(p); link != real {
		t.Fatal(link)
	}
	if Load(real).Scale != 5 {
		t.Fatal("target not updated")
	}
	if got := dirNames(t, realDir); !reflect.DeepEqual(got, []string{"settings.json"}) {
		t.Fatalf("no temp file left beside the target: %v", got)
	}
}

func TestAFailedSaveLeavesNoTempFile(t *testing.T) {
	p := tmpFile(t)
	// settings.json is a folder holding a file: the rename over it fails.
	os.MkdirAll(filepath.Join(p, "inside"), 0o755)
	if err := Default().Save(p); err == nil {
		t.Fatal("expected an error")
	}
	if got := dirNames(t, filepath.Dir(p)); !reflect.DeepEqual(got, []string{"settings.json"}) {
		t.Fatal(got)
	}
}

func TestManagedGeometryComesFromTheLiveConfig(t *testing.T) {
	window := &CanvasWindow{X: 1, Y: 2, Width: 800, Height: 600}
	live := Default()
	live.Position = &[2]float64{10, 20}
	live.Canvas.Window = window
	edited := Default()
	edited.Scale = 4
	merged := edited.WithManagedFrom(live)
	if merged.Scale != 4 || *merged.Position != [2]float64{10, 20} || *merged.Canvas.Window != *window {
		t.Fatalf("%+v", merged)
	}
}

func TestNormalizedFixesTheScaleLikeLoad(t *testing.T) {
	for _, tc := range []struct{ in, want uint32 }{{0, 8}, {999, MaxScale}, {5, 5}} {
		c := Default()
		c.Scale = tc.in
		if got := c.Normalized().Scale; got != tc.want {
			t.Fatalf("%d → %d", tc.in, got)
		}
	}
}

func TestNormalizedTreatsBlankOptionalPathsAsUnset(t *testing.T) {
	c := Default()
	c.Voice.HomeCity = ptr("  ")
	c.Voice.PersonaPath = ptr("")
	c.Agent.HerdrPath = ptr("  ")
	c.Agent.TmuxPath = ptr(" ")
	n := c.Normalized()
	if n.Voice.PersonaPath != nil {
		t.Fatal("persona")
	}
	if n.Voice.HomeCity != nil {
		t.Fatal("a blank home city means not set")
	}
	if n.Agent.HerdrPath != nil {
		t.Fatal("herdr")
	}
	if n.Agent.TmuxPath != nil {
		t.Fatal("tmux")
	}
	c = Default()
	c.Voice.HomeCity = ptr("  Leeds ")
	if got := c.Normalized().Voice.HomeCity; got == nil || *got != "Leeds" {
		t.Fatal("Leeds")
	}
	c = Default()
	c.Voice.PersonaPath = ptr("/p.md")
	if got := c.Normalized().Voice.PersonaPath; got == nil || *got != "/p.md" {
		t.Fatal("persona kept")
	}
}

// Go-only: the case-sensitive top-level keys serde uses; a differently cased key is an unknown
// key, not the field.
func TestTopLevelKeysAreCaseSensitive(t *testing.T) {
	c := parseConfig(t, `{"Scale":3}`)
	if c.Scale != 8 {
		t.Fatal("Scale must not set scale")
	}
	if _, ok := c.Extra["Scale"]; !ok {
		t.Fatal("kept as unknown")
	}
}

// voice.shortTermMemory was removed (Pixie's memory now comes from the coordinator): a settings
// file that still has it loads with every other setting kept, and the next save drops it.
func TestTheRemovedShortTermMemoryKeyIsIgnored(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"pack":"blob","voice":{"provider":"gemini-live","shortTermMemory":true,"chimes":false}}`), 0o644)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Pack != "blob" || c.Voice.Provider != "gemini-live" || c.Voice.Chimes {
		t.Fatalf("%+v", c)
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(p)
	if strings.Contains(string(text), "shortTermMemory") {
		t.Fatal(string(text))
	}
	for _, f := range Fields {
		if f.Path == "voice.shortTermMemory" {
			t.Fatal("still in the settings schema")
		}
	}
}

func TestSettingsWithoutANotificationsBlockGetDefaults(t *testing.T) {
	c := parseConfig(t, `{"pack":"bit"}`)
	want := NotificationSettings{DebounceSecs: 20, Bubble: BubbleSettings{Enabled: true, PopSecs: 6, Lines: 5}, AwayAfterMins: 5, SessionTurns: true, SessionTurnsMinSeconds: 30}
	if !reflect.DeepEqual(c.Notifications, want) {
		t.Fatalf("%+v", c.Notifications)
	}
}

func TestNotificationsBlockUsesCamelCase(t *testing.T) {
	j := mustJSON(t, Default())
	want := map[string]any{"debounceSecs": float64(20), "awayAfterMins": float64(5),
		"sessionTurns": true, "sessionTurnsMinSeconds": float64(30),
		"bubble": map[string]any{"enabled": true, "popSecs": float64(6), "lines": float64(5)}}
	if !reflect.DeepEqual(j["notifications"], want) {
		t.Fatal(j["notifications"])
	}
}

func TestHandOffWhenUnsureIsOnByDefault(t *testing.T) {
	if !Default().Voice.HandOffWhenUnsure {
		t.Fatal("voice.handOffWhenUnsure defaults to true")
	}
	if v := mustJSON(t, Default())["voice"].(map[string]any)["handOffWhenUnsure"]; v != true {
		t.Fatalf("handOffWhenUnsure in JSON: %v", v)
	}
	if !parseConfig(t, `{"voice":{"chimes":false}}`).Voice.HandOffWhenUnsure {
		t.Fatal("a voice block without the key keeps the default")
	}
	if parseConfig(t, `{"voice":{"handOffWhenUnsure":false}}`).Voice.HandOffWhenUnsure {
		t.Fatal("false is read")
	}
}

func TestCheckClaimsIsOnByDefault(t *testing.T) {
	if !Default().Voice.CheckClaims {
		t.Fatal("voice.checkClaims defaults to true")
	}
	if v := mustJSON(t, Default())["voice"].(map[string]any)["checkClaims"]; v != true {
		t.Fatalf("checkClaims in JSON: %v", v)
	}
	if !parseConfig(t, `{"voice":{"chimes":false}}`).Voice.CheckClaims {
		t.Fatal("a voice block without the key keeps the default")
	}
	if parseConfig(t, `{"voice":{"checkClaims":false}}`).Voice.CheckClaims {
		t.Fatal("false is read")
	}
}
