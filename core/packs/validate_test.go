package packs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ports of mascot-pack manifest.rs (6), resolve.rs (6) and validate.rs (20) tests, plus the
// src-tauri packs.rs discover/pick tests not already covered in packs_test.go.
// Omitted: sheet_data_url_is_base64_png and hit_at_maps_the_cursor_onto_the_mask (the Swift
// player reads the sheet by path and hit-tests natively; no Go equivalent), and the
// gen_*/bit_sheet generator tests (build-time only; bundled mascots/ are loaded instead).

const sheetW, sheetH = 256, 192

func bitManifest(t *testing.T) *manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundled, "bit", "mascot.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func bitText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundled, "bit", "mascot.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func vpaths(errs []string) []string {
	var out []string
	for _, e := range errs {
		out = append(out, strings.SplitN(e, ": ", 2)[0])
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// --- manifest.rs ---

func TestParsesBitManifest(t *testing.T) {
	m := bitManifest(t)
	if *m.Name != "Bit" || *m.Contract != 1 || *m.Cell != [2]uint32{32, 32} || *m.Sheet != "sheet.png" || *m.FPS != 8 {
		t.Fatalf("header: %+v", m)
	}
	if *m.Bob.Px != 1 || *m.Bob.Frames != 4 || len(*m.States) != 5 {
		t.Fatalf("bob/states: %+v", m)
	}
	mo := m.Mouth
	if *mo.Row != 5 || *mo.Anchor != [2]uint32{12, 20} || *mo.Size != [2]uint32{8, 5} || *mo.Levels != 4 {
		t.Fatalf("mouth: %+v", mo)
	}
}

func TestLoopAndBobDefaultToTrue(t *testing.T) {
	idle := (*bitManifest(t).States)["idle"]
	if idle.Loop != nil || idle.Bob != nil || idle.FaceDy != nil {
		t.Fatalf("idle should leave defaults unset: %+v", idle)
	}
	st := resolve(bitManifest(t)).States["idle"]
	if !st.Loop || !st.Bob {
		t.Fatalf("resolved idle = %+v", st)
	}
}

func TestReadsLoopFalseBobFalseAndFaceDy(t *testing.T) {
	happy := (*bitManifest(t).States)["happy"]
	if *happy.Loop || *happy.Bob || fmt.Sprint(happy.FaceDy) != "[0 -1 -2 -2 -2 -1 0 0]" {
		t.Fatalf("happy = %+v", happy)
	}
}

func TestMissingRequiredFieldIsAnErrorNamingIt(t *testing.T) {
	_, err := parseManifest([]byte(strings.Replace(bitText(t), `"cell": [32, 32],`, "", 1)))
	if err == nil || !strings.Contains(err.Error(), "cell") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	if _, err := parseManifest([]byte(strings.Replace(bitText(t), `"contract": 1,`, `"contract": 1, "futureThing": 42,`, 1))); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidJSONIsAnErrorNotAPanic(t *testing.T) {
	if _, err := parseManifest([]byte("{not json")); err == nil {
		t.Fatal("want error")
	}
}

// --- resolve.rs ---

func TestEveryStateNameIsPresent(t *testing.T) {
	r := resolve(bitManifest(t))
	for _, n := range StateNames {
		if _, ok := r.States[n]; !ok {
			t.Errorf("missing %s", n)
		}
	}
	if len(r.States) != 6 {
		t.Errorf("states = %d", len(r.States))
	}
}

func TestErrorFallsBackToIdle(t *testing.T) {
	r := resolve(bitManifest(t))
	if fmt.Sprint(r.States["error"]) != fmt.Sprint(r.States["idle"]) {
		t.Fatalf("error %v idle %v", r.States["error"], r.States["idle"])
	}
}

func TestMissingDelegatingFallsBackToThinking(t *testing.T) {
	m := bitManifest(t)
	delete(*m.States, "delegating")
	r := resolve(m)
	if fmt.Sprint(r.States["delegating"]) != fmt.Sprint(r.States["thinking"]) {
		t.Fatalf("delegating %v thinking %v", r.States["delegating"], r.States["thinking"])
	}
}

func TestMissingHappyFallsBackToIdle(t *testing.T) {
	m := bitManifest(t)
	delete(*m.States, "happy")
	r := resolve(m)
	if fmt.Sprint(r.States["happy"]) != fmt.Sprint(r.States["idle"]) {
		t.Fatal("happy should copy idle")
	}
}

func TestFaceDyDefaultsToZeros(t *testing.T) {
	if got := fmt.Sprint(resolve(bitManifest(t)).States["idle"].FaceDy); got != "[0 0 0 0 0 0 0 0]" {
		t.Fatal(got)
	}
}

func TestResolveMissingBobMeansNoBob(t *testing.T) {
	m := bitManifest(t)
	m.Bob = nil
	if resolve(m).Bob != (Bob{Px: 0, Frames: 1}) {
		t.Fatal("bob")
	}
}

// --- validate.rs ---

func TestBitIsValid(t *testing.T) {
	eq(t, validate(bitManifest(t), sheetW, sheetH), nil)
}

func TestMissingRequiredState(t *testing.T) {
	m := bitManifest(t)
	delete(*m.States, "listening")
	eq(t, vpaths(validate(m, sheetW, sheetH)), []string{"states.listening"})
}

func TestMissingOptionalStatesAreFine(t *testing.T) {
	m := bitManifest(t)
	delete(*m.States, "delegating")
	delete(*m.States, "happy")
	eq(t, validate(m, sheetW, sheetH), nil)
}

func TestRowOutOfBounds(t *testing.T) {
	m := bitManifest(t)
	s := (*m.States)["idle"]
	row := uint32(7)
	s.Row = &row
	(*m.States)["idle"] = s
	if got := validate(m, sheetW, sheetH)[0]; got != "states.idle.row: row 7 exceeds sheet rows (6)" {
		t.Fatal(got)
	}
}

func TestSheetTooNarrowForFrames(t *testing.T) {
	var found string
	for _, e := range validate(bitManifest(t), 128, sheetH) {
		if strings.HasPrefix(e, "states.idle.frames: ") {
			found = e
		}
	}
	if found != "states.idle.frames: 8 frames need 256px width, sheet is 128px" {
		t.Fatal(found)
	}
}

func TestBadContract(t *testing.T) {
	m := bitManifest(t)
	c := uint32(2)
	m.Contract = &c
	if got := validate(m, sheetW, sheetH)[0]; got != "contract: unsupported contract 2 (expected 1)" {
		t.Fatal(got)
	}
}

func TestLevelsBelowTwo(t *testing.T) {
	m := bitManifest(t)
	l := uint32(1)
	m.Mouth.Levels = &l
	if !strings.Contains(strings.Join(vpaths(validate(m, sheetW, sheetH)), ","), "mouth.levels") {
		t.Fatal("no mouth.levels error")
	}
}

func TestMouthOutsideCell(t *testing.T) {
	m := bitManifest(t)
	m.Mouth.Anchor = &[2]uint32{28, 20}
	if got := validate(m, sheetW, sheetH)[0]; got != "mouth.anchor: anchor + size (36, 25) outside cell (32, 32)" {
		t.Fatal(got)
	}
}

func TestFaceDyLengthMismatch(t *testing.T) {
	m := bitManifest(t)
	s := (*m.States)["happy"]
	s.FaceDy = []int32{0, 1}
	(*m.States)["happy"] = s
	if got := validate(m, sheetW, sheetH)[0]; got != "states.happy.faceDy: has 2 entries, expected 8 (one per frame)" {
		t.Fatal(got)
	}
}

func TestZeroCellStopsEarly(t *testing.T) {
	m := bitManifest(t)
	m.Cell = &[2]uint32{0, 32}
	eq(t, vpaths(validate(m, sheetW, sheetH)), []string{"cell"})
}

func TestFPSOutOfRange(t *testing.T) {
	m := bitManifest(t)
	for _, f := range []uint32{0, 61} {
		f := f
		m.FPS = &f
		eq(t, vpaths(validate(m, sheetW, sheetH)), []string{"fps"})
	}
}

func TestOversizedSheetIsRejected(t *testing.T) {
	got := validate(bitManifest(t), MaxSheetSide+1, 192)[0]
	if got != "sheet: 4097x192 exceeds the 4096x4096 limit" {
		t.Fatal(got)
	}
	if !strings.Contains(strings.Join(vpaths(validate(bitManifest(t), 256, MaxSheetSide+1)), ","), "sheet") {
		t.Fatal("tall sheet accepted")
	}
	eq(t, validate(bitManifest(t), MaxSheetSide, 192), nil)
}

func TestHugeBobIsRejected(t *testing.T) {
	m := bitManifest(t)
	px, fr := uint32(MaxBobPx+1), uint32(4)
	m.Bob = &bobSpec{Px: &px, Frames: &fr}
	if got := validate(m, sheetW, sheetH)[0]; got != "bob.px: 17 must be at most 16" {
		t.Fatal(got)
	}
	px = MaxBobPx
	eq(t, validate(m, sheetW, sheetH), nil)
}

// Pack-directory helpers for the load_and_validate tests.
func writePack(t *testing.T, dir, json string, sheetBytes []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mascot.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	if sheetBytes != nil {
		if err := os.WriteFile(filepath.Join(dir, "sheet.png"), sheetBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func bitSheetBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bundled, "bit", "sheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func firstPath(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("want error")
	}
	return strings.SplitN(err.Error(), ": ", 2)[0]
}

func TestOversizedSheetFileIsRejectedBeforeDecoding(t *testing.T) {
	d := t.TempDir()
	writePack(t, d, bitText(t), nil)
	f, err := os.Create(filepath.Join(d, "sheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxSheetBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = loadPack(d)
	if firstPath(t, err) != "sheet" || !strings.Contains(err.Error(), "larger than") {
		t.Fatal(err)
	}
}

func TestEveryBundledPackPassesValidation(t *testing.T) {
	entries, err := os.ReadDir(bundled)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
			if _, err := loadPack(filepath.Join(bundled, e.Name())); err != nil {
				t.Errorf("%s invalid: %v", e.Name(), err)
			}
		}
	}
	eq(t, names, []string{"bit", "blackgold", "dino", "flash"})
}

func TestLoadsValidPackFromDisk(t *testing.T) {
	d := t.TempDir()
	writePack(t, d, bitText(t), bitSheetBytes(t))
	p, err := loadPack(d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Bit" || p.SheetPath != filepath.Join(p.Dir, "sheet.png") {
		t.Fatalf("%+v", p)
	}
}

func TestMissingSheetFileIsAnError(t *testing.T) {
	d := t.TempDir()
	writePack(t, d, bitText(t), nil)
	_, err := loadPack(d)
	if firstPath(t, err) != "sheet" {
		t.Fatal(err)
	}
}

func TestSheetPathCannotEscapePack(t *testing.T) {
	for _, bad := range []string{"../evil.png", "/etc/passwd", "a/../b.png", "./sheet.png", ""} {
		d := t.TempDir()
		writePack(t, d, strings.Replace(bitText(t), `"sheet.png"`, fmt.Sprintf("%q", bad), 1), bitSheetBytes(t))
		_, err := loadPack(d)
		if firstPath(t, err) != "sheet" || !strings.Contains(err.Error(), "inside the pack") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestCorruptJSONIsAnError(t *testing.T) {
	d := t.TempDir()
	writePack(t, d, "{nope", bitSheetBytes(t))
	_, err := loadPack(d)
	if firstPath(t, err) != "mascot.json" {
		t.Fatal(err)
	}
}

func TestMissingManifestIsAnError(t *testing.T) {
	_, err := loadPack(t.TempDir())
	if firstPath(t, err) != "mascot.json" {
		t.Fatal(err)
	}
}

func TestOversizedSheetPixelsAreRejected(t *testing.T) {
	// A real PNG, wider than MaxSheetSide, with a manifest that fits otherwise.
	d := t.TempDir()
	writePack(t, d, bitText(t), nil)
	if err := writeBlankPNG(filepath.Join(d, "sheet.png"), MaxSheetSide+1, 192); err != nil {
		t.Fatal(err)
	}
	_, err := loadPack(d)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 4096x4096 limit") {
		t.Fatal(err)
	}
}

// --- packs.rs discover/pick ---

func TestFindsValidPacks(t *testing.T) {
	root := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "bit"), nil)
	ps := mustLoad(t, root)
	if len(ps) != 1 || ps["bit"].ID != "bit" || ps["bit"].Name != "Bit" {
		t.Fatalf("%v", ps)
	}
	if b, _ := os.ReadFile(ps["bit"].SheetPath); len(b) < 4 || string(b[1:4]) != "PNG" {
		t.Fatal("sheet is not a PNG")
	}
	if _, _, bits, err := ps["bit"].HitMask(); err != nil || len(bits) == 0 {
		t.Fatalf("hit mask: %v", err)
	}
}

func TestBrokenPackIsRejectedNotFatal(t *testing.T) {
	root := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "bit"), nil)
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "mascot.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(root)
	if len(ps) != 1 || err == nil || !strings.Contains(err.Error(), filepath.Join(root, "broken")) {
		t.Fatalf("ps=%d err=%v", len(ps), err)
	}
}

func TestMissingRootIsSkipped(t *testing.T) {
	ps, err := Load("/definitely/not/here")
	if len(ps) != 0 || err != nil {
		t.Fatalf("%v %v", ps, err)
	}
}

func TestPickFallsBackToBitThenFirst(t *testing.T) {
	root := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "bit"), nil)
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "zed"), nil)
	ps := mustLoad(t, root)
	if p, _ := Select(ps, "zed"); p.ID != "zed" {
		t.Fatal(p.ID)
	}
	if p, _ := Select(ps, "gone"); p.ID != "bit" {
		t.Fatal(p.ID)
	}
	delete(ps, "bit")
	if p, _ := Select(ps, "gone"); p.ID != "zed" {
		t.Fatal(p.ID)
	}
	if _, ok := Select(nil, "bit"); ok {
		t.Fatal("empty select")
	}
}
