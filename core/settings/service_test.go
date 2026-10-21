package settings

// Ported from ai-mascot src-tauri/src/settings/service.rs (23 tests, all ported).

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// memStore is settings.json and the running config, in memory.
type memStore struct {
	now   Config
	file  *Config
	err   error
	fail  bool
	saves int
}

func newMemStore(c Config) *memStore {
	f := c.Clone()
	return &memStore{now: c.Clone(), file: &f}
}

func (m *memStore) Current() Config { return m.now.Clone() }

func (m *memStore) Load() (*Config, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.file == nil {
		return nil, nil
	}
	c := m.file.Clone()
	return &c, nil
}

func (m *memStore) Save(c Config) error {
	if m.fail {
		return errors.New("disk full")
	}
	m.saves++
	merged := c.WithManagedFrom(m.now)
	m.now = merged.Clone()
	m.file = &merged
	return nil
}

func (m *memStore) Adopt(c Config) { m.now = c.WithManagedFrom(m.now) }

func (m *memStore) setFile(c Config) { m.err = nil; m.file = &c }

// recorder records each apply as (path, the hotkey in the config it was given).
type recorder struct {
	calls  [][2]string
	refuse []string
}

func (r *recorder) Apply(path string, c Config) error {
	r.calls = append(r.calls, [2]string{path, c.Hotkey})
	for _, p := range r.refuse {
		if p == path {
			return errors.New("taken")
		}
	}
	return nil
}

func (r *recorder) paths() []string {
	out := []string{}
	for _, c := range r.calls {
		out = append(out, c[0])
	}
	return out
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func errorsOf(t *testing.T, err error) [][2]string {
	t.Helper()
	var se *SetError
	if !errors.As(err, &se) || se.Kind != SetInvalid {
		t.Fatalf("expected Invalid, got %v", err)
	}
	var out [][2]string
	for _, e := range se.Errors {
		out = append(out, [2]string{e.Path, e.Message})
	}
	return out
}

func saveErr(msg string) error { return &SetError{Kind: SetSave, Message: msg} }

func sameErr(a, b error) bool { return reflect.DeepEqual(a, b) }

func one(path, msg string) [][2]string { return [][2]string{{path, msg}} }

func TestADeviceChoiceAppliesLiveAndSystemDefaultIsNull(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "voice.inputDevice", raw(`"BuiltInMic"`)); err != nil {
		t.Fatal(err)
	}
	if d := store.Current().Voice.InputDevice; d == nil || *d != "BuiltInMic" {
		t.Fatal(d)
	}
	if !reflect.DeepEqual(rec.paths(), []string{"voice.inputDevice"}) {
		t.Fatal(rec.paths())
	}
	if len(svc.RestartPending()) != 0 {
		t.Fatal("the next conversation uses it")
	}
	if err := svc.SetField(env, "voice.inputDevice", raw(`""`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Voice.InputDevice != nil {
		t.Fatal("System Default is stored as null")
	}
	if err := svc.SetField(env, "voice.outputDevice", raw(`null`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.paths(), []string{"voice.inputDevice", "voice.inputDevice"}) {
		t.Fatal("null to null changes nothing", rec.paths())
	}
	if e := errorsOf(t, svc.SetField(env, "voice.outputDevice", raw(`3`))); !reflect.DeepEqual(e, one("voice.outputDevice", "Expected text")) {
		t.Fatal(e)
	}
}

func TestADisconnectedSavedDeviceDoesNotBlockOtherSaves(t *testing.T) {
	c := Default()
	c.Voice.InputDevice = ptr("unplugged")
	store, rec := newMemStore(c), &recorder{}
	svc := NewService(store, rec)
	env := testEnv(t)
	env.Sources.Inputs = WithSaved(env.Sources.Inputs, ptr("unplugged"), MissingMic)
	if err := svc.SetField(env, "voice.chimes", raw(`false`)); err != nil {
		t.Fatal(err)
	}
	if d := store.Current().Voice.InputDevice; d == nil || *d != "unplugged" {
		t.Fatal("kept until the user picks another")
	}
}

func TestAHandEditedDisconnectedDeviceIsAdoptedAndDoesNotBlockSaves(t *testing.T) {
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	edited := Default()
	edited.Voice.OutputDevice = ptr("some-other-unplugged-uid")
	store.setFile(edited)
	if r := svc.Reload(); r != ReloadChanged {
		t.Fatal(r)
	}
	if !reflect.DeepEqual(rec.paths(), []string{"voice.outputDevice"}) {
		t.Fatal("applied live", rec.paths())
	}
	// env lists only the saved device it knew about before; this one isn't listed at all.
	env := testEnv(t)
	if err := svc.SetField(env, "voice.chimes", raw(`false`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetField(env, "voice.inputDevice", raw(`"another-unplugged-uid"`)); err != nil {
		t.Fatal(err)
	}
	if d := store.Current().Voice.OutputDevice; d == nil || *d != "some-other-unplugged-uid" {
		t.Fatal(d)
	}
}

func TestAValidLiveSetSavesAndApplies(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "scale", raw(`4`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Scale != 4 || store.saves != 1 || !reflect.DeepEqual(rec.paths(), []string{"scale"}) {
		t.Fatal(store.saves, rec.paths())
	}
	if len(svc.RestartPending()) != 0 {
		t.Fatal(svc.RestartPending())
	}
	svc.SetField(env, "scale", raw(`4`))
	if store.saves != 1 {
		t.Fatal("an unchanged value is not saved again")
	}
}

func TestARestartSetSavesAndMarksPending(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "agent.workers.enabled", raw(`true`)); err != nil {
		t.Fatal(err)
	}
	if !store.Current().Agent.Workers.Enabled || store.saves != 1 {
		t.Fatal("not saved")
	}
	if len(rec.paths()) != 0 {
		t.Fatal("restart fields are not applied live")
	}
	if !reflect.DeepEqual(svc.RestartPending(), []string{"agent.workers.enabled"}) {
		t.Fatal(svc.RestartPending())
	}
	svc.SetField(env, "agent.workers.enabled", raw(`false`))
	if len(svc.RestartPending()) != 0 {
		t.Fatal("back to the startup value: no restart needed")
	}
}

func TestAnInvalidSetSavesNothing(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	for _, tc := range []struct {
		path, value string
		want        [][2]string
	}{
		{"scale", `17`, one("scale", RangeMessage(1, 16))},
		{"scale", `"big"`, one("scale", Whole)},
		{"agent.port", `70000`, one("agent.port", RangeMessage(1024, 65535))},
		{"voice.idleTimeoutSecs", `-1`, one("voice.idleTimeoutSecs", RangeMessage(1, 600))},
		{"voice.model", `"  "`, one("voice.model", Empty)},
		{"pack", `"zed"`, one("pack", NotAChoice)},
		{"nope", `1`, one("nope", "Unknown setting")},
		{"canvas.window", `null`, one("canvas.window", "This setting is managed by the app")},
	} {
		if e := errorsOf(t, svc.SetField(env, tc.path, raw(tc.value))); !reflect.DeepEqual(e, tc.want) {
			t.Fatalf("%s=%s: %v", tc.path, tc.value, e)
		}
	}
	if store.saves != 0 || len(rec.paths()) != 0 || !store.Current().Equal(Default()) {
		t.Fatal("something changed")
	}
}

func TestOldErrorsElsewhereDoNotBlockOtherChanges(t *testing.T) {
	env := testEnv(t)
	start := Default()
	start.Agent.Port = 80
	start.Agent.ProjectsRoot = "/outside-home"
	store, rec := newMemStore(start), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "scale", raw(`6`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Scale != 6 {
		t.Fatal("scale")
	}
	if e := errorsOf(t, svc.SetField(env, "agent.port", raw(`81`))); !reflect.DeepEqual(e, one("agent.port", RangeMessage(1024, 65535))) {
		t.Fatal(e)
	}
	if e := errorsOf(t, svc.SetField(env, "agent.projectsRoot", raw(`"/outside-home-too"`))); len(e) != 1 {
		t.Fatal(e)
	}
	if err := svc.SetField(env, "agent.port", raw(`8080`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Agent.Port != 8080 {
		t.Fatal("port")
	}
}

func TestACrossFieldErrorIsReportedUnderItsOwnField(t *testing.T) {
	env := testEnv(t)
	start := Default()
	start.Agent.Workers.Kinds = []string{}
	store, rec := newMemStore(start), &recorder{}
	svc := NewService(store, rec)
	e := errorsOf(t, svc.SetField(env, "agent.workers.enabled", raw(`true`)))
	if !reflect.DeepEqual(e, one("agent.workers.kinds", "Pick at least one worker kind to turn workers on")) {
		t.Fatal(e)
	}
	if store.Current().Agent.Workers.Enabled || store.saves != 0 {
		t.Fatal("saved")
	}
}

func TestATakenHotkeyKeepsTheOldOne(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{refuse: []string{"hotkey"}}
	svc := NewService(store, rec)
	if e := errorsOf(t, svc.SetField(env, "hotkey", raw(`"ctrl+shift+KeyK"`))); !reflect.DeepEqual(e, one("hotkey", InUse)) {
		t.Fatal(e)
	}
	if store.saves != 0 || store.Current().Hotkey != "ctrl+alt+Space" {
		t.Fatal("changed")
	}
}

func TestTheHotkeyIsBoundBeforeTheSaveAndRestoredIfTheSaveFails(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "hotkey", raw(`"ctrl+shift+KeyK"`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.calls, [][2]string{{"hotkey", "ctrl+shift+KeyK"}}) || store.Current().Hotkey != "ctrl+shift+KeyK" {
		t.Fatal(rec.calls)
	}
	store.fail = true
	if err := svc.SetField(env, "hotkey", raw(`"ctrl+shift+KeyJ"`)); !sameErr(err, saveErr("disk full")) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.calls[1:], [][2]string{{"hotkey", "ctrl+shift+KeyJ"}, {"hotkey", "ctrl+shift+KeyK"}}) {
		t.Fatal(rec.calls)
	}
	if store.Current().Hotkey != "ctrl+shift+KeyK" {
		t.Fatal("hotkey")
	}
}

func TestAFailedSaveLeavesTheOldValues(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	store.fail = true
	svc := NewService(store, rec)
	if err := svc.SetField(env, "scale", raw(`4`)); !sameErr(err, saveErr("disk full")) {
		t.Fatal(err)
	}
	if store.Current().Scale != 8 || len(rec.paths()) != 0 {
		t.Fatal("changed")
	}
}

func TestUnknownKeysSurvive(t *testing.T) {
	env := testEnv(t)
	start := Default()
	start.Extra = map[string]json.RawMessage{"futureThing": raw(`{"a":1}`)}
	store, rec := newMemStore(start), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "scale", raw(`5`)); err != nil {
		t.Fatal(err)
	}
	if string(store.Current().Extra["futureThing"]) != `{"a":1}` {
		t.Fatal(store.Current().Extra)
	}
	if string(store.file.Extra["futureThing"]) != `{"a":1}` {
		t.Fatal("kept in the file")
	}
	values := svc.Snapshot(env).Values
	if _, ok := values["futureThing"]; ok {
		t.Fatal("but not shown to the page", values)
	}
	if values["scale"] != json.Number("5") {
		t.Fatal(values["scale"])
	}
}

func TestASaveAfterAHandEditKeepsTheHandEdit(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	edited := Default()
	edited.Scale = 5
	store.setFile(edited)
	if r := svc.Reload(); r != ReloadChanged {
		t.Fatal(r)
	}
	if err := svc.SetField(env, "pack", raw(`"dino"`)); err != nil {
		t.Fatal(err)
	}
	if store.file.Scale != 5 || store.file.Pack != "dino" {
		t.Fatal("the hand edit is merged, not overwritten")
	}
}

func TestAFileBrokenSinceTheLastLookRefusesEveryChange(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	store.err = errors.New("expected value at line 1 column 1")
	if r := svc.Reload(); r != ReloadFileError {
		t.Fatal(r)
	}
	broken := saveErr(FileBroken)
	if err := svc.SetField(env, "scale", raw(`4`)); !sameErr(err, broken) {
		t.Fatal(err)
	}
	if err := svc.SetField(env, "position", raw(`null`)); !sameErr(err, broken) {
		t.Fatal(err)
	}
	if len(rec.paths()) != 0 {
		t.Fatal("nothing applied, not even Reset position")
	}
	if store.saves != 0 {
		t.Fatal("saved")
	}
	if r := svc.Reload(); r != ReloadUnchanged {
		t.Fatal("the same error is not news on the next focus")
	}
}

func TestAHandEditedHotkeyThatCannotBeBoundIsNotAdopted(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{refuse: []string{"hotkey"}}
	svc := NewService(store, rec)
	edited := Default()
	edited.Hotkey = "ctrl+shift+KeyK"
	edited.Scale = 5
	store.setFile(edited)
	if r := svc.Reload(); r != ReloadChanged {
		t.Fatal(r)
	}
	if store.Current().Hotkey != "ctrl+alt+Space" {
		t.Fatal("the bound hotkey stays the config's hotkey")
	}
	if store.Current().Scale != 5 {
		t.Fatal("the rest of the edit is adopted")
	}
	w := svc.Snapshot(env).Warning
	if w == nil || !strings.Contains(*w, "ctrl+shift+KeyK") {
		t.Fatal("the page is told", w)
	}
	if r := svc.Reload(); r != ReloadUnchanged {
		t.Fatal("not retried on every focus")
	}
	n := 0
	for _, p := range rec.paths() {
		if p == "hotkey" {
			n++
		}
	}
	if n != 1 {
		t.Fatal(rec.paths())
	}
}

func TestAReloadWarningSurvivesTheSaveThatFollowsIt(t *testing.T) {
	// settings.set: reload, then SetField, in one call.
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{refuse: []string{"hotkey"}}
	svc := NewService(store, rec)
	edited := Default()
	edited.Hotkey = "ctrl+shift+KeyK"
	store.setFile(edited)
	if r := svc.Reload(); r != ReloadChanged {
		t.Fatal(r)
	}
	if err := svc.SetField(env, "voice.chimes", raw(`false`)); err != nil {
		t.Fatal(err)
	}
	w := svc.Snapshot(env).Warning
	if w == nil || !strings.Contains(*w, "ctrl+shift+KeyK") {
		t.Fatal("the refused hotkey is still reported", w)
	}
	if r := svc.Reload(); r != ReloadUnchanged {
		t.Fatal(r)
	}
	if err := svc.SetField(env, "voice.chimes", raw(`true`)); err != nil {
		t.Fatal(err)
	}
	if w := svc.Snapshot(env).Warning; w != nil {
		t.Fatal("a later save clears it", *w)
	}
}

func TestALiveApplyFailureAfterASaveReachesThePage(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{refuse: []string{"pack"}}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "pack", raw(`"dino"`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Pack != "dino" {
		t.Fatal("saved anyway")
	}
	w := svc.Snapshot(env).Warning
	if w == nil || !strings.HasPrefix(*w, "Pixie couldn't be applied now") {
		t.Fatal(w)
	}
	if err := svc.SetField(env, "scale", raw(`4`)); err != nil {
		t.Fatal(err)
	}
	if w := svc.Snapshot(env).Warning; w != nil {
		t.Fatal("the next good change clears it")
	}
}

func TestSnapshotsAreNumberedInOrder(t *testing.T) {
	env := testEnv(t)
	svc := NewService(newMemStore(Default()), &recorder{})
	a, b := svc.Snapshot(env).Seq, svc.Snapshot(env).Seq
	if b <= a {
		t.Fatal(a, b)
	}
}

func TestEmptyOptionalTextIsStoredAsNull(t *testing.T) {
	env := testEnv(t)
	store := newMemStore(Default())
	svc := NewService(store, &recorder{})
	if err := svc.SetField(env, "agent.model", raw(`" opus "`)); err != nil {
		t.Fatal(err)
	}
	if m := store.Current().Agent.Model; m == nil || *m != "opus" {
		t.Fatal(m)
	}
	if err := svc.SetField(env, "agent.model", raw(`"  "`)); err != nil {
		t.Fatal(err)
	}
	if store.Current().Agent.Model != nil {
		t.Fatal("not null")
	}
}

func TestResetPositionRunsTheLiveActionWithoutASave(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if err := svc.SetField(env, "position", raw(`null`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.paths(), []string{"position"}) || store.saves != 0 {
		t.Fatal(rec.paths(), store.saves)
	}
	if e := errorsOf(t, svc.SetField(env, "position", raw(`[1,2]`))); !reflect.DeepEqual(e, one("position", "This setting is managed by the app")) {
		t.Fatal(e)
	}
}

func TestHandEditsArePickedUpAndABrokenFileIsNeverOverwritten(t *testing.T) {
	env := testEnv(t)
	store, rec := newMemStore(Default()), &recorder{}
	svc := NewService(store, rec)
	if r := svc.Reload(); r != ReloadUnchanged {
		t.Fatal(r)
	}

	store.err = errors.New("expected `,` or `}` at line 3 column 5")
	if r := svc.Reload(); r != ReloadFileError {
		t.Fatal(r)
	}
	if fe := svc.Snapshot(env).FileError; fe == nil || *fe != "expected `,` or `}` at line 3 column 5" {
		t.Fatal(fe)
	}
	if err := svc.SetField(env, "scale", raw(`4`)); !sameErr(err, saveErr(FileBroken)) {
		t.Fatal(err)
	}
	if store.saves != 0 {
		t.Fatal("saved")
	}

	edited := Default()
	edited.Scale = 5
	edited.Position = &[2]float64{9, 9}
	edited.Agent.Workers.Enabled = true
	store.setFile(edited)
	if r := svc.Reload(); r != ReloadChanged {
		t.Fatal(r)
	}
	if store.Current().Scale != 5 {
		t.Fatal("scale")
	}
	if store.Current().Position != nil {
		t.Fatal("the live position wins over a hand edit")
	}
	if !reflect.DeepEqual(rec.paths(), []string{"scale"}) {
		t.Fatal(rec.paths())
	}
	if !reflect.DeepEqual(svc.RestartPending(), []string{"agent.workers.enabled"}) {
		t.Fatal(svc.RestartPending())
	}
	if svc.Snapshot(env).FileError != nil {
		t.Fatal("file error")
	}
	if r := svc.Reload(); r != ReloadUnchanged {
		t.Fatal(r)
	}
}

func TestTheSnapshotCarriesValuesFieldsAndPending(t *testing.T) {
	env := testEnv(t)
	svc := NewService(newMemStore(Default()), &recorder{})
	snap := svc.Snapshot(env)
	if snap.Platform != Macos {
		t.Fatal(snap.Platform)
	}
	if !reflect.DeepEqual(mustJSON(t, snap.Values), mustJSON(t, Default())) {
		t.Fatal(snap.Values)
	}
	n := 0
	for _, f := range Fields {
		if f.Kind.Type != KindManaged {
			n++
		}
	}
	if len(snap.Fields) != n {
		t.Fatal(len(snap.Fields))
	}
	v := mustJSON(t, snap)
	if !reflect.DeepEqual(v["restartPending"], []any{}) {
		t.Fatal(v["restartPending"])
	}
	for _, k := range []string{"fileError", "warning"} {
		if x, ok := v[k]; !ok || x != nil {
			t.Fatal(k, x)
		}
	}
	if _, ok := v["seq"].(float64); !ok {
		t.Fatal(v["seq"])
	}
}

func TestSetErrorsSerializeForThePage(t *testing.T) {
	invalid := &SetError{Kind: SetInvalid, Errors: []FieldError{{Path: "scale", Message: "x"}}}
	want := map[string]any{"kind": "invalid", "errors": []any{map[string]any{"path": "scale", "message": "x"}}}
	if got := mustJSON(t, invalid); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	save := saveErr("disk full")
	if got := mustJSON(t, save); !reflect.DeepEqual(got, map[string]any{"kind": "save", "message": "disk full"}) {
		t.Fatal(got)
	}
}
