package settings

// Ported from ai-mascot src-tauri/src/settings/store.rs (3 tests, all ported), plus Go-only
// tests of FileStore (Rust's AppConfigStore lived on the Tauri AppHandle and had none).

import (
	"os"
	"strings"
	"testing"
)

func TestAMissingFileReadsAsNone(t *testing.T) {
	c, err := ReadConfig(tmpFile(t))
	if c != nil || err != nil {
		t.Fatal(c, err)
	}
}

func TestABrokenFileGivesTheReasonAndIsLeftAlone(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{ "scale": 4,, }`), 0o644)
	_, err := ReadConfig(p)
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatal(err)
	}
	if text, _ := os.ReadFile(p); string(text) != `{ "scale": 4,, }` {
		t.Fatal(string(text))
	}
}

func TestAGoodFileReadsAsItsConfig(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{"scale":4}`), 0o644)
	c, err := ReadConfig(p)
	if err != nil || c == nil || c.Scale != 4 {
		t.Fatal(c, err)
	}
}

func TestASyntaxErrorOnALaterLineNamesThatLine(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte("{\n  \"scale\": 4\n  \"pack\": \"bit\"\n}"), 0o644)
	_, err := ReadConfig(p)
	if err == nil || !strings.Contains(err.Error(), "line 3 column 3") {
		t.Fatal(err)
	}
}

func TestFileStoreSaveKeepsLiveGeometryAndAdoptDoesNotWrite(t *testing.T) {
	p := tmpFile(t)
	s := OpenFileStore(p)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("defaults written on open", err)
	}
	if err := s.Update(func(c *Config) { c.Position = &[2]float64{3, 4} }); err != nil {
		t.Fatal(err)
	}
	edited := Default()
	edited.Scale = 4
	if err := s.Save(edited); err != nil {
		t.Fatal(err)
	}
	if got := Load(p); got.Scale != 4 || got.Position == nil || *got.Position != [2]float64{3, 4} {
		t.Fatalf("%+v", got)
	}
	adopted := Default()
	adopted.Scale = 2
	s.Adopt(adopted)
	if s.Current().Scale != 2 || *s.Current().Position != [2]float64{3, 4} {
		t.Fatal("adopt")
	}
	if Load(p).Scale != 4 {
		t.Fatal("adopt must not write")
	}
	if s.Path() != p {
		t.Fatal(s.Path())
	}
}
