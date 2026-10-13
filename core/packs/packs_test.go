package packs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bundled = "../../mascots"

func copyPack(t *testing.T, from, to string, edit func(string) string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := os.ReadFile(filepath.Join(from, "mascot.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(m)
	if edit != nil {
		text = edit(text)
	}
	if err := os.WriteFile(filepath.Join(to, "mascot.json"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	png, err := os.ReadFile(filepath.Join(from, "sheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "sheet.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBundledPacks(t *testing.T) {
	ps, err := Load(bundled)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ps) != 4 {
		t.Fatalf("got %d packs, want 4: %v", len(ps), ps)
	}
	for _, id := range []string{"bit", "blackgold", "dino", "flash"} {
		if _, ok := ps[id]; !ok {
			t.Errorf("missing pack %q", id)
		}
	}
	bit := ps["bit"]
	if bit.Cell != [2]uint32{32, 32} {
		t.Errorf("bit cell = %v", bit.Cell)
	}
	if bit.Mouth.Levels != 4 {
		t.Errorf("bit mouth levels = %d", bit.Mouth.Levels)
	}
	if bit.Name != "Bit" || bit.ID != "bit" {
		t.Errorf("bit name/id = %q/%q", bit.Name, bit.ID)
	}
	if !filepath.IsAbs(bit.SheetPath) || filepath.Base(bit.SheetPath) != "sheet.png" {
		t.Errorf("sheet path %q", bit.SheetPath)
	}
	if _, err := os.Stat(bit.SheetPath); err != nil {
		t.Errorf("sheet: %v", err)
	}
}

func TestResolveDefaultsAndFallbacks(t *testing.T) {
	bit := mustLoad(t)["bit"]
	idle := bit.States["idle"]
	if !idle.Loop || !idle.Bob || len(idle.FaceDy) != 8 {
		t.Errorf("idle = %+v, want loop/bob true and 8 zero faceDy", idle)
	}
	for _, d := range idle.FaceDy {
		if d != 0 {
			t.Errorf("idle faceDy = %v", idle.FaceDy)
		}
	}
	happy := bit.States["happy"]
	if happy.Loop || happy.Bob || happy.FaceDy[3] != -2 {
		t.Errorf("happy = %+v", happy)
	}
	// bit has no "error" state: it falls back to idle.
	if got := bit.States["error"]; got.Row != idle.Row || got.Frames != idle.Frames {
		t.Errorf("error = %+v, want idle %+v", got, idle)
	}
	if bit.Bob != (Bob{Px: 1, Frames: 4}) {
		t.Errorf("bob = %+v", bit.Bob)
	}
}

func TestMissingBobMeansNoBob(t *testing.T) {
	root := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "bit"), func(s string) string {
		return strings.Replace(s, `"bob": { "px": 1, "frames": 4 },`, "", 1)
	})
	ps := mustLoad(t, root)
	if ps["bit"].Bob != (Bob{Px: 0, Frames: 1}) {
		t.Errorf("bob = %+v", ps["bit"].Bob)
	}
}

// The JSON the Swift player decodes must match the Rust ResolvedPack serde output.
func TestBitPackJSONShape(t *testing.T) {
	b, err := json.Marshal(mustLoad(t)["bit"])
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "cell", "fps", "bob", "states", "mouth"} {
		if _, ok := v[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if len(v) != 6 {
		t.Errorf("unexpected keys: %s", b)
	}
	states := v["states"].(map[string]any)
	for _, name := range StateNames {
		s, ok := states[name].(map[string]any)
		if !ok {
			t.Fatalf("state %q missing in %s", name, b)
		}
		for _, k := range []string{"row", "frames", "loop", "bob", "faceDy"} {
			if _, ok := s[k]; !ok {
				t.Errorf("state %s missing %q", name, k)
			}
		}
		if len(s["faceDy"].([]any)) != int(s["frames"].(float64)) {
			t.Errorf("state %s faceDy len != frames", name)
		}
	}
	mouth := v["mouth"].(map[string]any)
	for _, k := range []string{"row", "anchor", "size", "levels"} {
		if _, ok := mouth[k]; !ok {
			t.Errorf("mouth missing %q", k)
		}
	}
	bob := v["bob"].(map[string]any)
	if bob["px"] != 1.0 || bob["frames"] != 4.0 {
		t.Errorf("bob = %v", bob)
	}
}

func TestUserRootOverridesBundled(t *testing.T) {
	user := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(user, "bit"), func(s string) string {
		return strings.Replace(s, `"name": "Bit"`, `"name": "Mine"`, 1)
	})
	ps := mustLoad(t, bundled, user)
	if len(ps) != 4 {
		t.Fatalf("got %d packs", len(ps))
	}
	if ps["bit"].Name != "Mine" || ps["bit"].Dir != filepath.Join(user, "bit") {
		t.Errorf("bit = %q from %s", ps["bit"].Name, ps["bit"].Dir)
	}
}

func TestSelect(t *testing.T) {
	ps := mustLoad(t)
	if p, ok := Select(ps, "dino"); !ok || p.ID != "dino" {
		t.Errorf("Select(dino) = %q %v", p.ID, ok)
	}
	if p, ok := Select(ps, "nope"); !ok || p.ID != "bit" {
		t.Errorf("Select(nope) = %q %v", p.ID, ok)
	}
	delete(ps, "bit")
	if p, ok := Select(ps, "nope"); !ok || p.ID != "blackgold" {
		t.Errorf("Select without bit = %q %v, want first by id", p.ID, ok)
	}
	if _, ok := Select(map[string]Pack{}, "bit"); ok {
		t.Error("Select on empty map succeeded")
	}
}

func TestBrokenPacksAreSkipped(t *testing.T) {
	root := t.TempDir()
	copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, "bit"), nil)
	bad := map[string]func(string) string{
		"brokenjson": func(string) string { return "{" },
		"nocell":     func(s string) string { return strings.Replace(s, `"cell": [32, 32],`, "", 1) },
		"contract2":  func(s string) string { return strings.Replace(s, `"contract": 1`, `"contract": 2`, 1) },
		"escape":     func(s string) string { return strings.Replace(s, `"sheet.png"`, `"../evil.png"`, 1) },
		"rowoob":     func(s string) string { return strings.Replace(s, `"row": 0,`, `"row": 9,`, 1) },
		"facedy":     func(s string) string { return strings.Replace(s, `[0, -1, -2, -2, -2, -1, 0, 0]`, `[0, 1]`, 1) },
		"noidle": func(s string) string {
			return strings.Replace(s, `"idle":       { "row": 0, "frames": 8 },`, "", 1)
		},
	}
	for name, edit := range bad {
		copyPack(t, filepath.Join(bundled, "bit"), filepath.Join(root, name), edit)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(root, "/definitely/not/here")
	if len(ps) != 1 || ps["bit"].ID != "bit" {
		t.Fatalf("packs = %v", ps)
	}
	if err == nil {
		t.Fatal("want an error listing rejected packs")
	}
	for name := range bad {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %q: %v", name, err)
		}
	}
}

func mustLoad(t *testing.T, roots ...string) map[string]Pack {
	t.Helper()
	if len(roots) == 0 {
		roots = []string{bundled}
	}
	ps, err := Load(roots...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return ps
}
