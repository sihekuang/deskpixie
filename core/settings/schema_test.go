package settings

// Ported from ai-mascot src-tauri/src/settings/schema.rs (19 tests, all ported).

import (
	"encoding/json"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// leaves collects the dotted paths of every leaf (non-object value) in v.
func leaves(v any, prefix string, out *[]string) {
	if m, ok := v.(map[string]any); ok {
		for k, child := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			leaves(child, p, out)
		}
		return
	}
	*out = append(*out, prefix)
}

func defaultTree(t *testing.T) any {
	t.Helper()
	return mustJSON(t, Default())
}

func TestEveryConfigLeafHasExactlyOneField(t *testing.T) {
	var configPaths []string
	leaves(defaultTree(t), "", &configPaths)
	sort.Strings(configPaths)
	var fieldPaths []string
	seen := map[string]bool{}
	for _, f := range Fields {
		if seen[f.Path] {
			t.Fatalf("a path is listed twice in Fields: %s", f.Path)
		}
		seen[f.Path] = true
		fieldPaths = append(fieldPaths, f.Path)
	}
	sort.Strings(fieldPaths)
	if !reflect.DeepEqual(configPaths, fieldPaths) {
		t.Fatalf("add a FieldSpec (or a Managed entry) for every Config field\nconfig %v\nfields %v", configPaths, fieldPaths)
	}
}

func TestEachKindMatchesTheDefaultValuesJSONType(t *testing.T) {
	tree := defaultTree(t)
	for _, f := range Fields {
		v, _ := lookup(tree, f.Path)
		_, isStr := v.(string)
		_, isBool := v.(bool)
		_, isNum := v.(float64)
		var fits bool
		switch f.Kind.Type {
		case KindBool:
			fits = isBool
		case KindInt:
			fits = isNum
		case KindChoice:
			fits = isStr || (f.Kind.Choices.Nullable() && v == nil)
		case KindHotkey:
			fits = isStr
		case KindText, KindPath:
			fits = isStr || (f.Kind.Optional && v == nil)
		case KindMultiChoice:
			arr, ok := v.([]any)
			fits = ok
			for _, e := range arr {
				if _, s := e.(string); !s {
					fits = false
				}
			}
		case KindManaged:
			fits = true
		}
		if !fits {
			t.Fatalf("%s is %v but its default is %v", f.Path, f.Kind.Type, v)
		}
	}
}

func TestEveryFieldPathExistsInConfig(t *testing.T) {
	tree := defaultTree(t)
	for _, f := range Fields {
		if _, ok := lookup(tree, f.Path); !ok {
			t.Fatalf("%s is not in Config", f.Path)
		}
	}
}

func TestTheLiveListIsPinned(t *testing.T) {
	want := []string{"hotkey", "pack", "scale", "replyMode", "alwaysShowCaption", "proximityFade.enabled", "proximityFade.radius",
		"voice.noiseFilter", "voice.inputDevice", "voice.outputDevice", "canvas.autoOpen",
		"notifications.bubble.enabled", "notifications.bubble.popSecs", "notifications.bubble.lines", "notifications.awayAfterMins"}
	if got := LivePaths(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestTheNoiseFilterIsALiveMacOnlySwitch(t *testing.T) {
	f := FieldByPath("voice.noiseFilter")
	if f.Section != SectionVoice || f.Kind.Type != KindBool || f.Apply != ApplyLive {
		t.Fatalf("%+v", f)
	}
	if !reflect.DeepEqual(f.Platforms, mac) || f.Label != "Noise filter" {
		t.Fatalf("%+v", f)
	}
}

func TestTheNoiseModelIsAMacOnlyRestartChoiceOfHushOrDfn3(t *testing.T) {
	f := FieldByPath("voice.noiseModel")
	if f.Section != SectionVoice || f.Kind.Type != KindChoice || f.Kind.Choices.Source != SourceStatic || f.Apply != ApplyRestart {
		t.Fatalf("%+v", f)
	}
	if !reflect.DeepEqual(f.Kind.Choices.Static, NoiseModels) || !reflect.DeepEqual(f.Platforms, mac) {
		t.Fatalf("%+v", f)
	}
	var values []string
	for _, o := range NoiseModels {
		values = append(values, o.Value)
	}
	if !reflect.DeepEqual(values, []string{"hush", "dfn3"}) {
		t.Fatal(values)
	}
}

func TestTheMicAndSpeakerAreLiveMacOnlyDeviceChoices(t *testing.T) {
	mic := FieldByPath("voice.inputDevice")
	if mic.Section != SectionVoice || mic.Kind.Type != KindChoice || mic.Kind.Choices.Source != SourceInputDevices || mic.Apply != ApplyLive {
		t.Fatalf("%+v", mic)
	}
	if mic.Label != "Microphone" || !reflect.DeepEqual(mic.Platforms, mac) {
		t.Fatalf("%+v", mic)
	}
	if !strings.Contains(mic.Help, "Bluetooth mics are slow to start") {
		t.Fatal(mic.Help)
	}
	speaker := FieldByPath("voice.outputDevice")
	if speaker.Section != SectionVoice || speaker.Kind.Type != KindChoice || speaker.Kind.Choices.Source != SourceOutputDevices || speaker.Apply != ApplyLive {
		t.Fatalf("%+v", speaker)
	}
	if speaker.Label != "Speaker" || !reflect.DeepEqual(speaker.Platforms, mac) {
		t.Fatalf("%+v", speaker)
	}
	var order []string
	for _, f := range Fields {
		if f.Section == SectionVoice {
			order = append(order, f.Path)
		}
	}
	at := func(p string) int {
		for i, o := range order {
			if o == p {
				return i
			}
		}
		return -1
	}
	nf := at("voice.noiseFilter")
	if at("voice.noiseModel") != nf+1 || at("voice.inputDevice") != nf+2 || at("voice.outputDevice") != nf+3 {
		t.Fatal(order)
	}
	if !(Choices{Source: SourceInputDevices}).Nullable() || !(Choices{Source: SourceOutputDevices}).Nullable() {
		t.Fatal("devices are nullable")
	}
	if (Choices{Source: SourcePacks}).Nullable() || (Choices{Source: SourceAudioBackends}).Nullable() {
		t.Fatal("packs/audio are not nullable")
	}
}

func pairsOf(opts []OptionView) [][2]string {
	var out [][2]string
	for _, o := range opts {
		out = append(out, [2]string{o.Value, o.Label})
	}
	return out
}

func TestDeviceOptionsStartWithTheSystemDefault(t *testing.T) {
	src := OptionSources{
		Inputs:  []Pair{{"BuiltInMic", "MacBook Pro Microphone"}, {"AA-BB", "AirPods Pro (Bluetooth)"}},
		Outputs: []Pair{{"BuiltInSpeaker", "MacBook Pro Speakers"}},
	}
	mics := Options(Choices{Source: SourceInputDevices}, src)
	want := [][2]string{{"", "System Default"}, {"BuiltInMic", "MacBook Pro Microphone"}, {"AA-BB", "AirPods Pro (Bluetooth)"}}
	if !reflect.DeepEqual(pairsOf(mics), want) {
		t.Fatal(pairsOf(mics))
	}
	for _, o := range mics {
		if !reflect.DeepEqual(o.Platforms, AllPlatforms) {
			t.Fatal(o)
		}
	}
	var speakers []string
	for _, o := range Options(Choices{Source: SourceOutputDevices}, src) {
		speakers = append(speakers, o.Label)
	}
	if !reflect.DeepEqual(speakers, []string{"System Default", "MacBook Pro Speakers"}) {
		t.Fatal(speakers)
	}
	if n := len(Options(Choices{Source: SourceInputDevices}, OptionSources{})); n != 1 {
		t.Fatal("no devices: only the default")
	}
}

func TestASavedDeviceThatIsNotConnectedStaysListed(t *testing.T) {
	listed := []Pair{{"BuiltInMic", "MacBook Pro Microphone"}}
	if got := WithSaved(listed, nil, MissingMic); !reflect.DeepEqual(got, listed) {
		t.Fatal(got)
	}
	if got := WithSaved(listed, ptr(""), MissingMic); !reflect.DeepEqual(got, listed) {
		t.Fatal(got)
	}
	if got := WithSaved(listed, ptr("BuiltInMic"), MissingMic); !reflect.DeepEqual(got, listed) {
		t.Fatal("connected: listed once", got)
	}
	out := WithSaved(listed, ptr("USB-1"), MissingMic)
	if out[1] != (Pair{"USB-1", "Saved microphone — not connected"}) {
		t.Fatal(out)
	}
	if len(listed) != 1 {
		t.Fatal("the input slice must not be changed")
	}
	if WithSaved(nil, ptr("USB-1"), MissingSpeaker)[0].Label != "Saved speaker — not connected" {
		t.Fatal("speaker")
	}
}

func TestNoisyRoomIsARestartSwitchOnEveryPlatform(t *testing.T) {
	f := FieldByPath("voice.noisyRoom")
	if f.Section != SectionVoice || f.Kind.Type != KindBool || f.Apply != ApplyRestart {
		t.Fatalf("%+v", f)
	}
	if !reflect.DeepEqual(f.Platforms, AllPlatforms) || f.Label != "Noisy room" {
		t.Fatalf("%+v", f)
	}
}

func TestGeometryIsManaged(t *testing.T) {
	var managed []string
	for _, f := range Fields {
		if f.Kind.Type == KindManaged {
			managed = append(managed, f.Path)
		}
	}
	if !reflect.DeepEqual(managed, []string{"position", "canvas.window"}) {
		t.Fatal(managed)
	}
}

func TestNumberBoundsComeFromTheSharedConstants(t *testing.T) {
	if k := FieldByPath("scale").Kind; !reflect.DeepEqual(k, Kind{Type: KindInt, Min: 1, Max: MaxScale, Menu: true}) {
		t.Fatalf("%+v", k)
	}
	if k := FieldByPath("agent.workers.max").Kind; !reflect.DeepEqual(k, Kind{Type: KindInt, Min: 1, Max: MaxWorkers, Menu: true}) {
		t.Fatalf("%+v", k)
	}
	if k := FieldByPath("agent.port").Kind; !reflect.DeepEqual(k, Kind{Type: KindInt, Min: 1024, Max: 65535}) {
		t.Fatalf("%+v", k)
	}
	if FieldByPath("nope") != nil {
		t.Fatal("nope")
	}
}

func TestPointersFollowTheDottedPath(t *testing.T) {
	tree := map[string]any{"agent": map[string]any{"workers": map[string]any{"max": 3.0}}, "hotkey": "x"}
	if v, ok := lookup(tree, "agent.workers.max"); !ok || v != 3.0 {
		t.Fatal(v)
	}
	if v, ok := lookup(tree, "hotkey"); !ok || v != "x" {
		t.Fatal(v)
	}
	if _, ok := lookup(tree, "agent.nope"); ok {
		t.Fatal("missing")
	}
}

func TestRegistryOptionsCarryLabelsAndPlatforms(t *testing.T) {
	src := OptionSources{
		Packs:     []Pair{{"bit", "Bit"}},
		Terminals: []string{"warp", "iterm", "kitty"},
	}
	terminals := Options(Choices{Source: SourceTerminals}, src)
	if !reflect.DeepEqual(terminals[0], OptionView{Value: "warp", Label: "Warp", Platforms: []Platform{Macos}}) {
		t.Fatal(terminals[0])
	}
	if terminals[1].Label != "iTerm" {
		t.Fatal(terminals[1])
	}
	if !reflect.DeepEqual(terminals[2], OptionView{Value: "kitty", Label: "kitty", Platforms: AllPlatforms}) {
		t.Fatal(terminals[2])
	}
	packs := Options(Choices{Source: SourcePacks}, src)
	if !reflect.DeepEqual(packs, []OptionView{{Value: "bit", Label: "Bit", Platforms: AllPlatforms}}) {
		t.Fatal(packs)
	}
	var kinds []string
	for _, o := range Options(Choices{Source: SourceStatic, Static: WorkerKinds}, src) {
		kinds = append(kinds, o.Value)
	}
	if !reflect.DeepEqual(kinds, []string{"claude", "codex"}) {
		t.Fatal(kinds)
	}
	audio := Options(Choices{Source: SourceAudioBackends}, OptionSources{Audio: []string{"macos-vpio"}})
	if !reflect.DeepEqual(audio[0].Platforms, []Platform{Macos}) {
		t.Fatal(audio)
	}
}

func TestAKnownNameThisBuildLacksIsListedAsAvailableNowhere(t *testing.T) {
	audio := Options(Choices{Source: SourceAudioBackends}, OptionSources{})
	want := []OptionView{{Value: "macos-vpio", Label: "macOS voice processing", Platforms: []Platform{}}}
	if !reflect.DeepEqual(audio, want) {
		t.Fatal(audio)
	}
}

func TestGeminiLiveIsAnOfferedVoiceProvider(t *testing.T) {
	// Rust read crate::voice::provider_names(); the registry is P14's, so the names it will
	// register are listed here.
	src := OptionSources{Providers: []string{ProviderOpenAI, ProviderGemini}}
	providers := Options(Choices{Source: SourceVoiceProviders}, src)
	var gemini, openai *OptionView
	for i := range providers {
		switch providers[i].Value {
		case "gemini-live":
			gemini = &providers[i]
		case "openai-realtime":
			openai = &providers[i]
		}
	}
	if gemini == nil || gemini.Label != "Gemini Live" || !reflect.DeepEqual(gemini.Platforms, AllPlatforms) {
		t.Fatal("gemini-live is offered", gemini)
	}
	if openai == nil || !reflect.DeepEqual(openai.Platforms, AllPlatforms) {
		t.Fatal(openai)
	}
}

func TestViewsLeaveOutManagedFieldsAndResolveOptions(t *testing.T) {
	src := OptionSources{Packs: []Pair{{"dino", "Dino"}}}
	views := Views(src)
	n := 0
	for _, f := range Fields {
		if f.Kind.Type != KindManaged {
			n++
		}
	}
	if len(views) != n {
		t.Fatal(len(views))
	}
	for _, v := range views {
		if v.Path == "position" || v.Path == "canvas.window" {
			t.Fatal(v.Path)
		}
		if v.Path == "pack" && !reflect.DeepEqual(v.Kind.Options, Options(Choices{Source: SourcePacks}, src)) {
			t.Fatal(v.Kind)
		}
	}
}

func TestAViewSerializesForThePage(t *testing.T) {
	var max, kinds *FieldView
	views := Views(OptionSources{})
	for i := range views {
		switch views[i].Path {
		case "agent.workers.max":
			max = &views[i]
		case "agent.workers.kinds":
			kinds = &views[i]
		}
	}
	got := mustJSON(t, max)
	want := map[string]any{
		"path": "agent.workers.max", "section": "Workers", "label": "Most workers at once",
		"help":  "Up to four workers run at the same time.",
		"kind":  map[string]any{"type": "int", "min": float64(1), "max": float64(4), "menu": true},
		"apply": "restart", "platforms": []any{"macos", "windows", "linux"},
	}
	if !reflect.DeepEqual(got, want) {
		b, _ := json.Marshal(got)
		t.Fatal(string(b))
	}
	if mustJSON(t, kinds)["kind"].(map[string]any)["type"] != "multiChoice" {
		t.Fatal(kinds)
	}
}

func TestTheCurrentPlatformIsOneOfTheThree(t *testing.T) {
	p := CurrentPlatform()
	found := false
	for _, q := range AllPlatforms {
		found = found || q == p
	}
	if !found {
		t.Fatal(p)
	}
	if runtime.GOOS == "darwin" && p != Macos {
		t.Fatal(p)
	}
}

func TestNotificationFieldsAreInTheNotificationsSection(t *testing.T) {
	for _, p := range []string{"notifications.debounceSecs", "notifications.bubble.enabled", "notifications.bubble.popSecs",
		"notifications.bubble.lines", "notifications.awayAfterMins", "agent.notifyPerMinute"} {
		f := FieldByPath(p)
		if f == nil || f.Section != SectionNotifications {
			t.Fatalf("%s: %+v", p, f)
		}
	}
}

func TestTheTmuxSettingsNameTheSupportedVersion(t *testing.T) {
	for _, path := range []string{"agent.sessionHost", "agent.tmuxPath"} {
		if f := FieldByPath(path); f == nil || !strings.Contains(f.Help, "tmux 3.2 or newer") {
			t.Fatalf("%s: %+v", path, f)
		}
	}
	if f := FieldByPath("agent.tmuxPath"); f.Section != SectionAdvanced || f.Apply != ApplyRestart {
		t.Fatalf("%+v", f)
	}
	// The session name is the host's, whichever host it is.
	if f := FieldByPath("agent.session"); strings.Contains(f.Help, "Herdr") {
		t.Fatal(f.Help)
	}
}

func TestBothSessionHostsAreChoices(t *testing.T) {
	opts := Options(Choices{Source: SourceSessionHosts}, OptionSources{Hosts: []string{"herdr", "tmux"}})
	var got [][2]string
	for _, o := range opts {
		got = append(got, [2]string{o.Value, o.Label})
	}
	if !reflect.DeepEqual(got, [][2]string{{"herdr", "Herdr"}, {"tmux", "tmux"}}) {
		t.Fatal(got)
	}
}

func TestTheHerdrSettingsNameTheSupportedVersions(t *testing.T) {
	for _, path := range []string{"agent.sessionHost", "agent.herdrPath"} {
		if f := FieldByPath(path); !strings.Contains(f.Help, "Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)") {
			t.Fatalf("%s: %q", path, f.Help)
		}
	}
}

func TestHandOffWhenUnsureField(t *testing.T) {
	f := FieldByPath("voice.handOffWhenUnsure")
	if f == nil {
		t.Fatal("voice.handOffWhenUnsure has no field")
	}
	if f.Section != SectionVoice || f.Apply != ApplyRestart || f.Kind.Type != KindBool {
		t.Fatalf("%+v", f)
	}
	if f.Label != "Hand off when Pixie doesn't know" ||
		f.Help != "When Pixie can't answer a question or request itself, it hands it to the coordinator instead of saying it doesn't know." {
		t.Fatalf("%q / %q", f.Label, f.Help)
	}
}

func TestCheckClaimsField(t *testing.T) {
	f := FieldByPath("voice.checkClaims")
	if f == nil {
		t.Fatal("voice.checkClaims has no field")
	}
	if f.Section != SectionVoice || f.Apply != ApplyRestart || f.Kind.Type != KindBool {
		t.Fatalf("%+v", f)
	}
	if f.Label != "Check Pixie's claims" ||
		f.Help != "When Pixie says it did something, like setting a timer or handing a task off, the app checks it really did and corrects it if not." {
		t.Fatalf("%q / %q", f.Label, f.Help)
	}
	// It sits right after voice.handOffWhenUnsure.
	for i, g := range Fields {
		if g.Path == "voice.handOffWhenUnsure" && (i+1 >= len(Fields) || Fields[i+1].Path != "voice.checkClaims") {
			t.Fatal("voice.checkClaims is not next to voice.handOffWhenUnsure")
		}
	}
}

// #85: every visibility rule names another real field with at least one value, and a value
// a static choice can't hold is a typo.
func TestVisibilityConditionsNameRealFields(t *testing.T) {
	n := 0
	for _, f := range Fields {
		for _, c := range f.VisibleWhen {
			n++
			target := FieldByPath(c.Key)
			if target == nil {
				t.Fatalf("%s: visibleWhen names unknown field %q", f.Path, c.Key)
			}
			if c.Key == f.Path {
				t.Fatalf("%s: visibleWhen names itself", f.Path)
			}
			if target.Kind.Type == KindManaged {
				t.Fatalf("%s: visibleWhen names managed field %q", f.Path, c.Key)
			}
			if len(c.In) == 0 {
				t.Fatalf("%s: visibleWhen %q has no values", f.Path, c.Key)
			}
			if target.Kind.Type == KindChoice && target.Kind.Choices.Source == SourceStatic && !target.Kind.Choices.Open {
				for _, v := range c.In {
					found := false
					for _, o := range target.Kind.Choices.Static {
						found = found || o.Value == v
					}
					if !found {
						t.Fatalf("%s: %q is not a choice of %s", f.Path, v, c.Key)
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no visibility rules")
	}
}

func TestTheGPTLiveFieldsShowOnlyForGPTLiveRightUnderTheVoiceService(t *testing.T) {
	provider := Condition{Key: "voice.provider", In: []string{ProviderOpenAILive}}
	if got := FieldByPath("voice.liveDelegation").VisibleWhen; !reflect.DeepEqual(got, []Condition{provider}) {
		t.Fatal(got)
	}
	want := []Condition{provider, {Key: "voice.liveDelegation", In: []string{LiveDelegationBackend}}}
	if got := FieldByPath("voice.liveBackendModel").VisibleWhen; !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	var voice []string
	for _, f := range Fields {
		if f.Section == SectionVoice {
			voice = append(voice, f.Path)
		}
	}
	if !reflect.DeepEqual(voice[:4], []string{"voice.provider", "voice.liveDelegation", "voice.liveBackendModel", "voice.audio"}) {
		t.Fatal(voice)
	}
	for _, p := range []string{"voice.liveDelegation", "voice.liveBackendModel"} {
		if strings.Contains(FieldByPath(p).Help, "Only for") {
			t.Fatal(p, FieldByPath(p).Help)
		}
	}
}

func TestVisibleWhenSerializesOnlyWhenSet(t *testing.T) {
	views := Views(OptionSources{})
	for i := range views {
		got := mustJSON(t, views[i])
		switch views[i].Path {
		case "voice.liveBackendModel":
			want := []any{
				map[string]any{"key": "voice.provider", "in": []any{"openai-live"}},
				map[string]any{"key": "voice.liveDelegation", "in": []any{"backend"}},
			}
			if !reflect.DeepEqual(got["visibleWhen"], want) {
				t.Fatal(got["visibleWhen"])
			}
		case "voice.provider":
			if _, ok := got["visibleWhen"]; ok {
				t.Fatal(got)
			}
		}
	}
}

// #114: announcing finished pix sessions is off by default and read at startup.
func TestSessionTurnSettings(t *testing.T) {
	on := FieldByPath("notifications.sessionTurns")
	if on == nil || on.Section != SectionNotifications || on.Kind.Type != KindBool || on.Apply != ApplyRestart ||
		on.Label != "Announce finished sessions" {
		t.Fatalf("%+v", on)
	}
	min := FieldByPath("notifications.sessionTurnsMinSeconds")
	if min == nil || min.Section != SectionNotifications || min.Apply != ApplyRestart ||
		min.Label != "Only turns longer than (seconds)" {
		t.Fatalf("%+v", min)
	}
	if d := Default().Notifications; d.SessionTurns || d.SessionTurnsMinSeconds != 30 {
		t.Fatalf("%+v", d)
	}
}
