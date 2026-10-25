package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMac is a home with some pixes: path → what `pix version` prints after "pix " ("" = it
// doesn't run), and links: path → what it points at.
type fakeMac struct {
	pix   map[string]string
	links map[string]string
}

func (m fakeMac) env() AboutEnv {
	return AboutEnv{
		Home: "/Users/me",
		Core: "0.19.0 (abc1234)",
		Exists: func(p string) bool {
			_, ok := m.pix[m.resolve(p)]
			return ok
		},
		Resolve: m.resolve,
		PixVersion: func(p string) (string, error) {
			if v := m.pix[m.resolve(p)]; v != "" {
				return v, nil
			}
			return "", errors.New("exit status 1")
		},
	}
}

func (m fakeMac) resolve(p string) string {
	if to, ok := m.links[p]; ok {
		return to
	}
	return p
}

const (
	appPix  = "/Applications/DeskPixie.app/Contents/Helpers/pix"
	linkPix = "/Users/me/.local/bin/pix"
	shell   = "/Users/me/.local/bin:/opt/homebrew/bin:/usr/bin"
)

func about(m fakeMac, path string) About {
	return BuildAbout(AboutArgs{App: "0.19.0 (412)", OS: "macOS 15.1.0", Pix: appPix, Path: path}, m.env())
}

func value(a About, label string) string {
	for _, r := range a.Rows {
		if r.Label == label {
			return r.Value
		}
	}
	return "<no row>"
}

func TestAboutShowsEachPartsVersion(t *testing.T) {
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)"}, links: map[string]string{linkPix: appPix}}
	a := about(m, shell)
	want := []AboutRow{
		{"DeskPixie", "0.19.0 (412)"},
		{"Core", "0.19.0 (abc1234)"},
		{"pix in the app", "0.19.0 (abc1234)"},
		{"pix in your terminal", "0.19.0 (abc1234), at ~/.local/bin/pix"},
	}
	if len(a.Rows) != len(want) {
		t.Fatalf("%+v", a.Rows)
	}
	for i := range want {
		if a.Rows[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, a.Rows[i], want[i])
		}
	}
	if a.Note != "" || a.Warning {
		t.Errorf("note %q, warning %v", a.Note, a.Warning)
	}
	wantText := "DeskPixie: 0.19.0 (412)\nCore: 0.19.0 (abc1234)\npix in the app: 0.19.0 (abc1234)\n" +
		"pix in your terminal: 0.19.0 (abc1234), at ~/.local/bin/pix\nmacOS 15.1.0\n"
	if a.Text != wantText {
		t.Errorf("text %q", a.Text)
	}
}

func TestAboutSaysWhenTheLinkReachesAnOlderApp(t *testing.T) {
	old := "/Users/me/Downloads/DeskPixie.app/Contents/Helpers/pix"
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)", old: "0.17.0 (9f8e7d6)"}, links: map[string]string{linkPix: old}}
	a := about(m, shell)
	if v := value(a, AboutPixTerminal); v != "0.17.0 (9f8e7d6), at ~/.local/bin/pix" {
		t.Fatal(v)
	}
	if !a.Warning || !strings.Contains(a.Note, "Your terminal's pix is 0.17.0 (9f8e7d6), the app's is 0.19.0 (abc1234).") || !strings.Contains(a.Note, "Install the pix command again") {
		t.Fatal(a.Note)
	}
	if !strings.HasSuffix(a.Text, a.Note+"\n") {
		t.Fatalf("the copied text leaves out the note: %q", a.Text)
	}
}

func TestAboutSaysWhenAnotherPixComesFirstOnPath(t *testing.T) {
	brew := "/opt/homebrew/bin/pix"
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)", brew: "0.12.0"}}
	a := about(m, "/opt/homebrew/bin:/Users/me/.local/bin")
	if v := value(a, AboutPixTerminal); v != "0.12.0, at /opt/homebrew/bin/pix" {
		t.Fatal(v)
	}
	if !a.Warning || !strings.Contains(a.Note, "another pix, 0.12.0 at /opt/homebrew/bin/pix; the app's is 0.19.0 (abc1234).") || !strings.Contains(a.Note, "put ~/.local/bin first") {
		t.Fatal(a.Note)
	}
}

func TestAboutWithTheSameVersionElsewhereIsFine(t *testing.T) {
	copyPix := "/Users/me/bin/pix"
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)", copyPix: "0.19.0 (abc1234)"}}
	if a := about(m, "/Users/me/bin"); a.Note != "" {
		t.Fatal(a.Note)
	}
}

func TestAboutWithNoPixInTheTerminal(t *testing.T) {
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)"}}
	a := about(m, shell)
	if v := value(a, AboutPixTerminal); v != "not found" {
		t.Fatal(v)
	}
	// Not a warning: plenty of people never use pix.
	if a.Warning || !strings.Contains(a.Note, "doesn't find pix") || !strings.Contains(a.Note, "Sessions page") {
		t.Fatal(a.Note)
	}
}

func TestAboutWithAPixThatDoesntRun(t *testing.T) {
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)", linkPix: ""}}
	a := about(m, shell)
	if v := value(a, AboutPixTerminal); v != "doesn't run, at ~/.local/bin/pix" {
		t.Fatal(v)
	}
	if !a.Warning || a.Note != "Your terminal's pix, at ~/.local/bin/pix, doesn't run. Install the pix command again on the Sessions page." {
		t.Fatal(a.Note)
	}
}

func TestAboutWithNoBundledPix(t *testing.T) {
	m := fakeMac{pix: map[string]string{}}
	a := BuildAbout(AboutArgs{}, m.env())
	if value(a, AboutApp) != "unknown" || value(a, AboutPixApp) != "not found" || value(a, AboutPixTerminal) != "not found" {
		t.Fatalf("%+v", a.Rows)
	}
	// A terminal pix with no app pix to compare with: no warning.
	m = fakeMac{pix: map[string]string{linkPix: "0.19.0"}}
	if a := BuildAbout(AboutArgs{Pix: "/nowhere/pix", Path: shell}, m.env()); a.Warning || a.Note != "" {
		t.Fatalf("%q", a.Note)
	}
}

// With no PATH from the shell, the usual install dirs are searched.
func TestAboutWithoutAPathLooksInTheInstallDirs(t *testing.T) {
	m := fakeMac{pix: map[string]string{appPix: "0.19.0 (abc1234)"}, links: map[string]string{linkPix: appPix}}
	if v := value(about(m, ""), AboutPixTerminal); v != "0.19.0 (abc1234), at ~/.local/bin/pix" {
		t.Fatal(v)
	}
}

func TestAppAboutIsADpCall(t *testing.T) {
	h := newHarness(t)
	out := h.call("app.about", AboutArgs{App: "0.19.0 (412)", OS: "macOS 15.1.0", Pix: "/nonexistent/pix", Path: "/nonexistent"})
	var a About
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatal(err)
	}
	if value(a, AboutApp) != "0.19.0 (412)" || value(a, AboutCore) == "" || value(a, AboutPixApp) != "not found" {
		t.Fatalf("%+v", a.Rows)
	}
}

func TestPixVersionRunsPix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 'pix 0.19.0 (abc1234)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v, err := pixVersion(p); err != nil || v != "0.19.0 (abc1234)" {
		t.Fatal(v, err)
	}
	broken := filepath.Join(t.TempDir(), "pix")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := pixVersion(broken); err == nil {
		t.Fatal("a pix that fails has a version")
	}
}
