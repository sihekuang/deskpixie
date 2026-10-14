package settings

// Ported from ai-mascot src-tauri/src/settings/store.rs (3 tests, all ported), plus Go-only
// tests of FileStore (Rust's AppConfigStore lived on the Tauri AppHandle and had none).

import (
	"errors"
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

// Final review I2: an app write re-reads settings.json and writes only its own change onto it.
func TestUpdateKeepsAHandEditAndUnknownKeys(t *testing.T) {
	p := tmpFile(t)
	s := OpenFileStore(p)
	os.WriteFile(p, []byte(`{"hotkey":"ctrl+shift+K","scale":5,"pack":"bit","myNote":"keep me"}`), 0o644)
	if err := s.Update(func(c *Config) { c.Position = &[2]float64{7, 8} }); err != nil {
		t.Fatal(err)
	}
	disk, err := ReadConfig(p)
	if err != nil || disk == nil {
		t.Fatal(err)
	}
	if disk.Hotkey != "ctrl+shift+K" || disk.Scale != 5 {
		t.Fatalf("hand edit lost: %+v", disk)
	}
	if disk.Position == nil || *disk.Position != [2]float64{7, 8} {
		t.Fatalf("position %v", disk.Position)
	}
	if text, _ := os.ReadFile(p); !strings.Contains(string(text), `"myNote"`) {
		t.Fatalf("unknown key lost: %s", text)
	}
	// The running config gets the change only; the hand edit arrives through Reload.
	if cur := s.Current(); cur.Position == nil || cur.Scale != Default().Scale {
		t.Fatalf("%+v", cur)
	}
	// A toggle flips the running value, not the disk's.
	if err := s.Update(func(c *Config) { c.Scale = 3 }); err != nil {
		t.Fatal(err)
	}
	if disk, _ := ReadConfig(p); disk.Scale != 3 || disk.Hotkey != "ctrl+shift+K" || disk.Position == nil {
		t.Fatalf("%+v", disk)
	}
}

func TestUpdateNeverOverwritesABrokenFile(t *testing.T) {
	p := tmpFile(t)
	s := OpenFileStore(p)
	os.WriteFile(p, []byte(`{ "scale": 4,, }`), 0o644)
	err := s.Update(func(c *Config) { c.Position = &[2]float64{1, 2} })
	if !errors.Is(err, ErrFileBroken) {
		t.Fatal(err)
	}
	if text, _ := os.ReadFile(p); string(text) != `{ "scale": 4,, }` {
		t.Fatalf("broken file overwritten: %s", text)
	}
	if pos := s.Current().Position; pos == nil || *pos != [2]float64{1, 2} {
		t.Fatalf("change not kept in memory: %v", pos)
	}
}

func TestUpdateWritesTheFileWhenItIsMissing(t *testing.T) {
	p := tmpFile(t)
	s := OpenFileStore(p)
	os.Remove(p)
	if err := s.Update(func(c *Config) { c.Scale = 6 }); err != nil {
		t.Fatal(err)
	}
	if disk, err := ReadConfig(p); err != nil || disk == nil || disk.Scale != 6 {
		t.Fatal(disk, err)
	}
}

func TestABrokenFileAtLaunchShowsTheBannerAndBlocksSaves(t *testing.T) {
	p := tmpFile(t)
	os.WriteFile(p, []byte(`{ "scale": 4,, }`), 0o644)
	s := OpenFileStore(p)
	svc := NewService(s, &recorder{})
	snap := svc.Snapshot(Env{})
	if snap.FileError == nil || !strings.Contains(*snap.FileError, "line 1") {
		t.Fatalf("no banner: %v", snap.FileError)
	}
	err := svc.SetField(Env{}, "scale", []byte("5"))
	var se *SetError
	if !errors.As(err, &se) || se.Kind != SetSave || se.Message != FileBroken {
		t.Fatalf("save not blocked: %v", err)
	}
	if text, _ := os.ReadFile(p); string(text) != `{ "scale": 4,, }` {
		t.Fatalf("rewritten: %s", text)
	}
}
