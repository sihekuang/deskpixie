package app

// Ported: ai-mascot src-tauri/src/lib.rs `shared_tests` (4 of 4). lib.rs's other tests are
// Tauri-only and have no Go equivalent: display_tests (monitor work-area conversion: Swift
// Layout, S2), acl_tests (Tauri capability files), bundle_tests (tauri.conf.json, Info.plist and
// entitlements: S6). The rest of this file is Go-only: the composition (Applier routes, tray
// actions, dp_call methods, events) that Rust's actions.rs/live.rs/canvas/mod.rs did through
// Tauri and never unit-tested.

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

type event map[string]any

func TestMain(m *testing.M) {
	if os.Getenv("APP_TEST_LOG") == "" {
		log.SetOutput(io.Discard)
	}
	os.Exit(m.Run())
}

type harness struct {
	t       *testing.T
	app     *App
	dir     string // app support
	bundled string // bundled mascots
	methods map[string]func(json.RawMessage) (any, error)

	mu     sync.Mutex
	events []event
	runs   [][]string
}

type mapRegistrar map[string]func(json.RawMessage) (any, error)

func (m mapRegistrar) Register(method string, fn func(json.RawMessage) (any, error)) {
	m[method] = fn
}

// copyDir copies a pack folder (two flat files).
func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writePack writes Bit as pack id with the given mouth levels.
func writePack(t *testing.T, root, id string, levels int) {
	t.Helper()
	dir := filepath.Join(root, id)
	copyDir(t, filepath.Join(bundledMascots(t), "bit"), dir)
	m := read(t, filepath.Join(dir, "mascot.json"))
	m = strings.Replace(m, `"levels": 4`, `"levels": `+itoa(levels), 1)
	m = strings.Replace(m, `"name": "Bit"`, `"name": "`+strings.ToUpper(id[:1])+id[1:]+`"`, 1)
	write(t, filepath.Join(dir, "mascot.json"), m)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

type harnessOpt func(*Deps, *harness)

func withBundled(root string) harnessOpt {
	return func(d *Deps, h *harness) { d.Paths.BundledMascots = root; h.bundled = root }
}

func withSettings(edit func(*settings.Config)) harnessOpt {
	return func(d *Deps, h *harness) {
		c := settings.Default()
		edit(&c)
		if err := c.Save(filepath.Join(d.Paths.AppSupport, "settings.json")); err != nil {
			h.t.Fatal(err)
		}
	}
}

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), bundled: bundledMascots(t), methods: map[string]func(json.RawMessage) (any, error){}}
	ds, _ := filepath.Abs("../../design-system")
	d := Deps{
		Paths: Paths{AppSupport: h.dir, BundledMascots: h.bundled, DesignSystem: ds, Platform: "macos"},
		Emit: func(kind string, b []byte) {
			var ev event
			if err := json.Unmarshal(b, &ev); err != nil {
				t.Errorf("bad event %s: %v", b, err)
				return
			}
			if ev["kind"] != kind {
				t.Errorf("event kind %v pushed as %q", ev["kind"], kind)
			}
			h.mu.Lock()
			h.events = append(h.events, ev)
			h.mu.Unlock()
		},
		Secrets: voice.NewEnvSecretsWithLookup(nil, func(string) (string, bool) { return "", false }),
		Run: func(program string, args ...string) error {
			h.mu.Lock()
			h.runs = append(h.runs, append([]string{program}, args...))
			h.mu.Unlock()
			return nil
		},
		CanvasTick: 10 * time.Millisecond,
	}
	for _, o := range opts {
		o(&d, h)
	}
	a, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	h.app = a
	a.Register(mapRegistrar(h.methods))
	return h
}

func (h *harness) call(method string, args any) json.RawMessage {
	h.t.Helper()
	out, err := h.callErr(method, args)
	if err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
	return out
}

func (h *harness) callErr(method string, args any) (json.RawMessage, error) {
	h.t.Helper()
	fn, ok := h.methods[method]
	if !ok {
		h.t.Fatalf("method %s not registered", method)
	}
	if args == nil {
		args = struct{}{}
	}
	b, _ := json.Marshal(args)
	res, err := fn(b)
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(res)
	return out, nil
}

func (h *harness) action(id string) {
	h.t.Helper()
	h.call("app.action", map[string]string{"id": id})
}

func (h *harness) state() AppState {
	h.t.Helper()
	var s AppState
	if err := json.Unmarshal(h.call("app.state", nil), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// snap is the part of settings.Snapshot the tests read (FieldView does not decode back).
type snap struct {
	Values   map[string]any  `json:"values"`
	Warning  *string         `json:"warning"`
	SetError json.RawMessage `json:"setError"`
}

func (h *harness) snapshot() snap {
	h.t.Helper()
	var s snap
	if err := json.Unmarshal(h.call("settings.snapshot", nil), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) set(field string, value any) snap {
	h.t.Helper()
	var s snap
	if err := json.Unmarshal(h.call("settings.set", map[string]any{"field": field, "value": value}), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// saved is settings.json as it is on disk.
func (h *harness) saved() settings.Config {
	h.t.Helper()
	c, err := settings.ReadConfig(filepath.Join(h.dir, "settings.json"))
	if err != nil || c == nil {
		h.t.Fatalf("settings.json: %v", err)
	}
	return *c
}

// handEdit rewrites settings.json the way a user would.
func (h *harness) handEdit(edit func(*settings.Config)) {
	h.t.Helper()
	c := h.saved()
	edit(&c)
	if err := c.Save(filepath.Join(h.dir, "settings.json")); err != nil {
		h.t.Fatal(err)
	}
}

// take returns and forgets the events so far.
func (h *harness) take() []event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.events
	h.events = nil
	return out
}

func (h *harness) kinds(evs []event, kind string) []event {
	var out []event
	for _, e := range evs {
		if e["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

// waitFor waits until an event of kind matching match arrives (events before it are consumed).
func (h *harness) waitFor(kind string, match func(event) bool, within time.Duration) event {
	h.t.Helper()
	deadline := time.Now().Add(within)
	var seen []event
	for time.Now().Before(deadline) {
		evs := h.take()
		for i, e := range evs {
			seen = append(seen, e)
			if e["kind"] == kind && (match == nil || match(e)) {
				h.mu.Lock()
				h.events = append(append([]event{}, evs[i+1:]...), h.events...)
				h.mu.Unlock()
				return e
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("no %s event; saw %v", kind, seen)
	return nil
}

// --- lib.rs shared_tests ---

func TestSelectingAPackRemembersIt(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "bit", 4)
	writePack(t, root, "zed", 4)
	h := newHarness(t, withBundled(root))
	h.take()
	h.action("pack:zed")
	if s := h.state(); s.Pack != "zed" {
		t.Fatalf("current %q", s.Pack)
	}
	if h.saved().Pack != "zed" {
		t.Fatal("not saved")
	}
	if len(h.kinds(h.take(), "pack")) != 1 {
		t.Fatal("no pack event")
	}
	if _, err := h.callErr("app.action", map[string]string{"id": "pack:gone"}); err == nil {
		t.Error("an unknown pack is refused")
	}
	if h.saved().Pack != "zed" || h.state().Pack != "zed" {
		t.Fatal("unknown pack changed the choice")
	}
}

func TestReloadingWithoutTheChosenPackKeepsTheSavedChoice(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "bit", 4)
	writePack(t, root, "zed", 4)
	h := newHarness(t, withBundled(root), withSettings(func(c *settings.Config) { c.Pack = "zed" }))
	if h.state().Pack != "zed" {
		t.Fatal(h.state().Pack)
	}
	if err := os.RemoveAll(filepath.Join(root, "zed")); err != nil {
		t.Fatal(err)
	}
	h.take()
	h.action("reload")
	packEvs := h.kinds(h.take(), "pack")
	if len(packEvs) != 1 || packEvs[0]["pack"].(map[string]any)["name"] != "Bit" {
		t.Fatalf("shows the fallback: %v", packEvs)
	}
	if h.state().Pack != "bit" {
		t.Fatal("current pack is the fallback")
	}
	if h.saved().Pack != "zed" {
		t.Fatal("choice kept")
	}
	// When the pack comes back, it is chosen again.
	writePack(t, root, "zed", 4)
	h.action("reload")
	if h.state().Pack != "zed" {
		t.Fatal(h.state().Pack)
	}
}

type fakeMeter struct {
	mu   sync.Mutex
	seen []int
}

func (f *fakeMeter) SetMouthLevels(n int) {
	f.mu.Lock()
	f.seen = append(f.seen, n)
	f.mu.Unlock()
}

func TestTheVoiceLoopMetersThePackActuallyShown(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "bit", 4)
	writePack(t, root, "zed", 2)
	h := newHarness(t, withBundled(root))
	meter := &fakeMeter{}
	h.app.setMeter(meter)
	h.action("pack:zed")
	// Reload without zed: the fallback (bit) is shown, so its range is metered.
	if err := os.RemoveAll(filepath.Join(root, "zed")); err != nil {
		t.Fatal(err)
	}
	h.action("reload")
	writePack(t, root, "zed", 2)
	h.action("reload")
	meter.mu.Lock()
	defer meter.mu.Unlock()
	if want := []int{2, 4, 2}; !equalInts(meter.seen, want) {
		t.Fatalf("metered %v, want %v", meter.seen, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReloadingWithNoPacksShowsNothing(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "bit", 4)
	h := newHarness(t, withBundled(root))
	if err := os.RemoveAll(filepath.Join(root, "bit")); err != nil {
		t.Fatal(err)
	}
	h.take()
	h.action("reload")
	if len(h.kinds(h.take(), "pack")) != 0 {
		t.Fatal("pack event without packs")
	}
	if h.saved().Pack != "bit" {
		t.Fatal("choice kept")
	}
}

// --- startup ---

func TestStartupEmitsDevicesAndTrayAndInstallsTheCanvasGuide(t *testing.T) {
	mic := "BuiltInMicrophoneDevice"
	h := newHarness(t, withSettings(func(c *settings.Config) { c.Voice.InputDevice = &mic }))
	evs := h.take()
	dev := h.kinds(evs, "devices")
	if len(dev) != 1 || dev[0]["input"] != mic {
		t.Fatalf("devices: %v", dev)
	}
	if _, ok := dev[0]["output"]; ok {
		t.Error("absent output is omitted")
	}
	if len(h.kinds(evs, "tray")) == 0 {
		t.Error("no tray event")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "canvas", "CANVAS.md")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "canvas", "pages")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "mascots")); err != nil {
		t.Error("user mascots folder:", err)
	}
}

func TestStartupPrunesOldPages(t *testing.T) {
	dir := t.TempDir()
	pages := filepath.Join(dir, "canvas", "pages")
	for i, slug := range []string{"a", "b", "c"} {
		p := filepath.Join(pages, slug+".html")
		write(t, p, "<title>"+slug+"</title>")
		at := time.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	c := settings.Default()
	c.Canvas.Keep = 2
	if err := c.Save(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(d *Deps, h *harness) { d.Paths.AppSupport = dir; h.dir = dir })
	if _, err := os.Stat(filepath.Join(pages, "a.html")); !os.IsNotExist(err) {
		t.Fatal("oldest page kept")
	}
	recent := h.state().CanvasRecent
	if len(recent) != 2 || recent[0].Slug != "c" || recent[1].Title != "b" {
		t.Fatalf("recent %+v", recent)
	}
}

func TestAppStateHasEveryListAndTheSettingsValues(t *testing.T) {
	h := newHarness(t, withSettings(func(c *settings.Config) { c.Scale = 6; c.ProximityFade.Enabled = false }))
	raw := string(h.call("app.state", nil))
	for _, k := range []string{`"packs":[`, `"canvasRecent":[`, `"jobs":[`, `"statusLines":[`} {
		if !strings.Contains(raw, k) {
			t.Errorf("%s missing in %s", k, raw)
		}
	}
	s := h.state()
	if s.State != "idle" || s.Scale != 6 || s.Fade || s.Talking || s.Hotkey != "ctrl+alt+Space" || s.Pack != "bit" {
		t.Fatalf("%+v", s)
	}
	if len(s.Packs) != 4 || s.Packs[0].ID != "bit" || s.Packs[0].Name != "Bit" {
		t.Fatalf("packs %+v", s.Packs)
	}
	if s.Agent.Enabled {
		t.Error("the agent menu stays hidden without an agent")
	}
}

func TestPackJSONCarriesScaleAndHitMask(t *testing.T) {
	h := newHarness(t, withSettings(func(c *settings.Config) { c.Scale = 5 }))
	var v struct {
		Pack      map[string]any `json:"pack"`
		SheetPath string         `json:"sheetPath"`
		Scale     int            `json:"scale"`
		HitMask   []byte         `json:"hitMask"`
	}
	if err := json.Unmarshal(h.app.PackJSON(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Pack["name"] != "Bit" || v.Scale != 5 || len(v.HitMask) != 32*32/8 || !filepath.IsAbs(v.SheetPath) {
		t.Fatalf("%+v", v)
	}
}

// --- tray actions ---

func TestStateActionAnnouncesAndAOneShotReturnsToIdle(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.action("state:thinking")
	if h.state().State != "thinking" {
		t.Fatal(h.state().State)
	}
	if _, err := h.callErr("app.action", map[string]string{"id": "state:dancing"}); err == nil {
		t.Error("unknown state accepted")
	}
	h.action("state:happy")
	h.waitFor("state", func(e event) bool { return e["state"] == "happy" }, time.Second)
	// Bit's happy plays once: 9 frames at 8 fps, then the app mirrors the player's return.
	h.waitFor("state", func(e event) bool { return e["state"] == "idle" }, 3*time.Second)
	if h.state().State != "idle" {
		t.Fatal(h.state().State)
	}
}

func TestSizeActionSavesAndResendsThePack(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.action("size:12")
	if h.saved().Scale != 12 || h.state().Scale != 12 {
		t.Fatal("not saved")
	}
	evs := h.take()
	p := h.kinds(evs, "pack")
	if len(p) != 1 || p[0]["scale"] != float64(12) || p[0]["hitMask"] == nil || p[0]["sheetPath"] == nil {
		t.Fatalf("pack events %v", p)
	}
	if len(h.kinds(evs, "settings.changed")) != 1 || len(h.kinds(evs, "tray")) == 0 {
		t.Fatalf("events %v", evs)
	}
	h.action("size:12")
	if len(h.kinds(h.take(), "pack")) != 0 {
		t.Error("the same size again resends")
	}
}

func TestFadeActionTogglesTheSavedSetting(t *testing.T) {
	h := newHarness(t)
	h.action("fade")
	if h.saved().ProximityFade.Enabled || h.state().Fade {
		t.Fatal("fade still on")
	}
	h.action("fade")
	if !h.saved().ProximityFade.Enabled {
		t.Fatal("fade still off")
	}
}

func TestResetPositionForgetsThePositionAndMovesBit(t *testing.T) {
	h := newHarness(t)
	h.call("mascot.moved", map[string]float64{"x": 10, "y": 20})
	if p := h.saved().Position; p == nil || p[0] != 10 || p[1] != 20 {
		t.Fatalf("position %v", p)
	}
	h.take()
	h.action("reset-position")
	if h.saved().Position != nil {
		t.Fatal("position kept")
	}
	pos := h.kinds(h.take(), "position")
	if len(pos) != 1 || len(pos[0]) != 1 {
		t.Fatalf("position event %v (no x/y = default spot)", pos)
	}
}

func TestTalkActionAnimatesTheMouth(t *testing.T) {
	h := newHarness(t)
	h.action("talk")
	if !h.state().Talking {
		t.Fatal("not talking")
	}
	h.waitFor("mouth", func(e event) bool { return e["level"] != nil && e["level"].(float64) > 0 }, 2*time.Second)
	h.action("talk")
	if h.state().Talking {
		t.Fatal("still talking")
	}
	h.waitFor("mouth", func(e event) bool { return e["level"] == float64(0) }, 2*time.Second)
}

func TestCanvasFolderActionOpensThePagesFolder(t *testing.T) {
	h := newHarness(t)
	h.action("canvas:folder")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.runs) != 1 || h.runs[0][0] != "open" || h.runs[0][1] != filepath.Join(h.dir, "canvas", "pages") {
		t.Fatalf("runs %v", h.runs)
	}
}

func TestCanvasOpenShowsTheEmptyStateWithoutPages(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.action("canvas:open")
	ev := h.waitFor("canvas.open", nil, time.Second)
	if ev["url"] != EmptyPage {
		t.Fatalf("%v", ev)
	}
}

func TestANewPageIsShownAndBitCelebrates(t *testing.T) {
	h := newHarness(t)
	h.take()
	write(t, filepath.Join(h.dir, "canvas", "pages", "q3.html"), "<!doctype html><title>Q3 revenue</title><link rel=stylesheet href=/ds/tokens.css>")
	ev := h.waitFor("canvas.open", nil, 5*time.Second)
	if ev["url"] != "canvas://localhost/pages/q3.html" || ev["title"] != "Q3 revenue" {
		t.Fatalf("%v", ev)
	}
	h.waitFor("state", func(e event) bool { return e["state"] == "happy" }, time.Second)
	recent := h.state().CanvasRecent
	if len(recent) != 1 || recent[0].Slug != "q3" {
		t.Fatalf("recent %+v", recent)
	}
	h.take()
	h.action("canvas:page:q3")
	if ev := h.waitFor("canvas.open", nil, time.Second); ev["url"] != "canvas://localhost/pages/q3.html" {
		t.Fatalf("%v", ev)
	}
}

func TestUnknownActionsFail(t *testing.T) {
	h := newHarness(t)
	if _, err := h.callErr("app.action", map[string]string{"id": "nope"}); err == nil {
		t.Fatal("nope accepted")
	}
	// Without an agent (Deps.Agent nil) the agent ids are logged no-ops.
	h.action("agent:open")
}

// --- dp_call methods ---

func TestCanvasResolveServesTheDesignSystemAndRefusesTraversal(t *testing.T) {
	h := newHarness(t)
	var r struct {
		Status      int               `json:"status"`
		ContentType string            `json:"contentType"`
		File        string            `json:"file"`
		Headers     map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(h.call("canvas.resolve", map[string]string{"path": "/ds/tokens.css"}), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != 200 || !strings.HasPrefix(r.ContentType, "text/css") || r.File == "" || r.Headers["Content-Security-Policy"] == "" {
		t.Fatalf("%+v", r)
	}
	r.File = ""
	if err := json.Unmarshal(h.call("canvas.resolve", map[string]string{"path": "/ds/../settings.json"}), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != 404 || r.File != "" {
		t.Fatalf("%+v", r)
	}
}

func TestCanvasGeometryIsSavedAsTheCanvasWindow(t *testing.T) {
	h := newHarness(t)
	h.call("canvas.geometry", map[string]float64{"x": 1, "y": 2, "width": 800, "height": 600})
	w := h.saved().Canvas.Window
	if w == nil || *w != (settings.CanvasWindow{X: 1, Y: 2, Width: 800, Height: 600}) {
		t.Fatalf("%+v", w)
	}
}

func TestMascotClickIsANoOpWithoutAnAgent(t *testing.T) {
	h := newHarness(t)
	h.call("mascot.click", nil)
}

func TestAudioMethodsReachTheRuntime(t *testing.T) {
	h := newHarness(t)
	h.call("audio.deviceChanged", nil)
	h.call("audio.failed", map[string]string{"reason": "cannot start audio: -10875"})
}

func TestHotkeyWithoutAKeyShowsTheStatusLine(t *testing.T) {
	h := newHarness(t)
	h.app.Hotkey()
	h.waitFor("state", func(e event) bool { return e["state"] == "error" }, 3*time.Second)
	lines := h.state().StatusLines
	if len(lines) != 1 || lines[0] != "Voice: no API key (set OPENAI_API_KEY)" {
		t.Fatalf("%v", lines)
	}
}

func TestAnUnknownProviderShowsTheSetupFailureOnTheHotkey(t *testing.T) {
	h := newHarness(t, withSettings(func(c *settings.Config) { c.Voice.Provider = "nope" }))
	if h.app.Runtime() != nil {
		t.Fatal("runtime built for an unknown provider")
	}
	h.app.Hotkey()
	h.waitFor("state", func(e event) bool { return e["state"] == "error" }, time.Second)
	if lines := h.state().StatusLines; len(lines) != 1 || lines[0] != `Voice: unknown provider "nope"` {
		t.Fatalf("%v", lines)
	}
	h.waitFor("state", func(e event) bool { return e["state"] == "idle" }, 3*time.Second)
}

// --- settings wiring (the Applier) ---

func TestPackAndScaleSettingsResendThePack(t *testing.T) {
	h := newHarness(t)
	h.take()
	if s := h.set("pack", "dino"); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	p := h.kinds(h.take(), "pack")
	if len(p) != 1 || p[0]["pack"].(map[string]any)["name"] != "Dino" {
		t.Fatalf("%v", p)
	}
	h.set("scale", 4)
	p = h.kinds(h.take(), "pack")
	if len(p) != 1 || p[0]["scale"] != float64(4) {
		t.Fatalf("%v", p)
	}
	if h.state().Scale != 4 || h.state().Pack != "dino" {
		t.Fatalf("%+v", h.state())
	}
}

func TestFadeSettingsTellSwiftToReread(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.set("proximityFade.radius", 200)
	if len(h.kinds(h.take(), "settings.changed")) == 0 {
		t.Fatal("no settings.changed")
	}
}

func TestTheNoiseFilterSettingReachesTheCleaner(t *testing.T) {
	h := newHarness(t)
	if h.app.cleaner.Enabled() {
		t.Fatal("on at start")
	}
	h.set("voice.noiseFilter", true)
	if !h.app.cleaner.Enabled() {
		t.Fatal("a Live change reaches the mic path")
	}
}

func TestDeviceChoicesReachSwiftAsADevicesEvent(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.set("voice.outputDevice", "AirPods-UID")
	d := h.kinds(h.take(), "devices")
	if len(d) != 1 || d[0]["output"] != "AirPods-UID" {
		t.Fatalf("%v", d)
	}
}

func TestPositionNullResetsThroughSettings(t *testing.T) {
	h := newHarness(t)
	h.call("mascot.moved", map[string]float64{"x": 3, "y": 4})
	h.take()
	if s := h.set("position", nil); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	if h.saved().Position != nil || len(h.kinds(h.take(), "position")) != 1 {
		t.Fatal("not reset")
	}
}

func TestAutoOpenSettingReachesTheController(t *testing.T) {
	h := newHarness(t)
	h.set("canvas.autoOpen", false)
	h.take()
	write(t, filepath.Join(h.dir, "canvas", "pages", "x.html"), "<title>X</title>")
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(h.state().CanvasRecent) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(h.state().CanvasRecent) != 1 {
		t.Fatal("not updated")
	}
	if len(h.kinds(h.take(), "canvas.open")) != 0 {
		t.Fatal("shown with autoOpen off")
	}
}

func TestRestartEmitsTheRestartEvent(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.call("settings.restart", nil)
	if len(h.kinds(h.take(), "restart")) != 1 {
		t.Fatal("no restart event")
	}
}

// --- hotkey ---

func TestTheSetPathNeverAsksSwiftToBindAgain(t *testing.T) {
	h := newHarness(t)
	h.take()
	if s := h.set("hotkey", "ctrl+alt+K"); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	if len(h.kinds(h.take(), "hotkey")) != 0 {
		t.Fatal("Swift bound it before settings.set")
	}
	if h.saved().Hotkey != "ctrl+alt+K" || h.state().Hotkey != "ctrl+alt+K" {
		t.Fatal("not saved")
	}
}

func TestAHandEditedHotkeyIsSentToSwift(t *testing.T) {
	h := newHarness(t)
	h.handEdit(func(c *settings.Config) { c.Hotkey = "ctrl+alt+J" })
	h.take()
	h.snapshot()
	hk := h.kinds(h.take(), "hotkey")
	if len(hk) != 1 || hk[0]["combo"] != "ctrl+alt+J" {
		t.Fatalf("%v", hk)
	}
	if h.state().Hotkey != "ctrl+alt+J" {
		t.Fatal(h.state().Hotkey)
	}
}

func TestAHandEditedHotkeySwiftCannotBindIsNotAdopted(t *testing.T) {
	h := newHarness(t)
	h.handEdit(func(c *settings.Config) { c.Hotkey = "cmd+Q" })
	h.snapshot()
	h.take()
	h.call("settings.hotkeyFailed", map[string]string{"combo": "cmd+Q"})
	s := h.snapshot()
	if s.Warning == nil || !strings.Contains(*s.Warning, "(cmd+Q) can't be used; keeping ctrl+alt+Space") {
		t.Fatalf("warning %v", s.Warning)
	}
	if s.Values["hotkey"] != "ctrl+alt+Space" || h.state().Hotkey != "ctrl+alt+Space" {
		t.Fatalf("adopted: %v", s.Values["hotkey"])
	}
	evs := h.take()
	if len(h.kinds(evs, "hotkey")) != 0 {
		t.Fatal("refused combo sent again")
	}
	if len(h.kinds(evs, "settings.changed")) == 0 {
		t.Fatal("the window is not told")
	}
	if len(h.state().StatusLines) != 0 {
		t.Fatal("the old hotkey still works: no status line")
	}
	// The file keeps the hand edit (Rust: Reload never writes).
	if h.saved().Hotkey != "cmd+Q" {
		t.Fatal(h.saved().Hotkey)
	}
}

func TestAStartupHotkeyFailureShowsTheTrayLineUntilARebind(t *testing.T) {
	h := newHarness(t)
	h.call("settings.hotkeyFailed", map[string]string{"combo": "ctrl+alt+Space"})
	if lines := h.state().StatusLines; len(lines) != 1 || lines[0] != "Hotkey unavailable: ctrl+alt+Space" {
		t.Fatalf("%v", lines)
	}
	h.set("hotkey", "ctrl+alt+K")
	if len(h.state().StatusLines) != 0 {
		t.Fatal("line kept after a successful bind")
	}
}

// --- timer alarm ---

type fakeVoice struct {
	mu       sync.Mutex
	open     bool
	injected []string
	sig      chan voice.VoiceSignal
}

func (f *fakeVoice) IsOpen() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.open }
func (f *fakeVoice) Inject(n voice.Notice) {
	f.mu.Lock()
	f.injected = append(f.injected, n.Text)
	f.mu.Unlock()
}
func (f *fakeVoice) Subscribe() <-chan voice.VoiceSignal { return f.sig }

func TestATimerDuringAConversationIsSaidElseNotified(t *testing.T) {
	type note struct{ title, body string }
	notes := make(chan note, 4)
	v := &fakeVoice{sig: make(chan voice.VoiceSignal, 4)}
	alarm := &TimerAlarm{
		Voice:  func() VoiceLink { return v },
		Notify: func(title, body string) { notes <- note{title, body} },
	}
	alarm.Fire("pasta", "5 minutes")
	if n := <-notes; n != (note{"Timer done", "pasta (5 minutes)"}) {
		t.Fatalf("%+v", n)
	}
	v.mu.Lock()
	v.open = true
	v.mu.Unlock()
	alarm.Fire("tea", "3 minutes")
	v.mu.Lock()
	inj := append([]string(nil), v.injected...)
	v.mu.Unlock()
	if len(inj) != 1 || inj[0] != "Timer done: tea (3 minutes)." {
		t.Fatalf("%v", inj)
	}
	select {
	case n := <-notes:
		t.Fatalf("notified while it may still be said: %+v", n)
	case <-time.After(50 * time.Millisecond):
	}
	v.sig <- voice.VoiceSignal{Kind: voice.SigClosed, Undelivered: []voice.Notice{{Text: inj[0]}}}
	select {
	case n := <-notes:
		if n.body != "tea (3 minutes)" {
			t.Fatal(n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("undelivered notice not notified")
	}
}

func TestTheNotifyEventPlaysGlass(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.app.notify("Timer done", "pasta (5 minutes)")
	n := h.kinds(h.take(), "notify")
	if len(n) != 1 || n[0]["title"] != "Timer done" || n[0]["body"] != "pasta (5 minutes)" || n[0]["sound"] != "glass" {
		t.Fatalf("%v", n)
	}
}

// --- recording dir ---

func TestRecordingsGoToAppDataOnlyWithVoiceDebug(t *testing.T) {
	t.Setenv("VOICE_DEBUG", "1")
	h := newHarness(t)
	if got := h.app.recordDir; got != filepath.Join(h.dir, "recordings") {
		t.Fatal(got)
	}
}
