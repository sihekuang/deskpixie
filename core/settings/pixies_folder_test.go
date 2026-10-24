package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// The pixies folder (#266) is a live, optional folder in the Pixie section; nil by default, so
// the app reads <AppSupport>/pixies.
func TestPixiesFolderIsALiveOptionalFolder(t *testing.T) {
	if Default().PixiesFolder != nil {
		t.Fatal("no folder by default")
	}
	f := FieldByPath("pixiesFolder")
	if f.Section != SectionGeneral || f.Apply != ApplyLive || f.Kind.Type != KindPath || !f.Kind.Dir || !f.Kind.Optional {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("pixiesFolder"); !ok || r != RoutePixiesFolder {
		t.Fatal(r, ok)
	}
	c, err := ParseConfig([]byte(`{"pixiesFolder":"  "}`))
	if err != nil || c.Normalized().PixiesFolder != nil {
		t.Fatal("a blank folder means not set:", c.PixiesFolder, err)
	}
}

func TestPixiesFolderMustBeAFolder(t *testing.T) {
	env := testEnv(t)
	dir := filepath.Join(env.Home, "Art", "my-pixies")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(env.Home, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, ok := range map[string]bool{
		dir:                                true,
		"~/Art/my-pixies":                  true,
		"Art/my-pixies":                    true,
		filepath.Join(env.Home, "missing"): false,
		file:                               false,
		"~someone/pixies":                  false,
	} {
		c := Default()
		c.PixiesFolder = &path
		got := problems(c, env)
		bad := len(got) == 1 && got[0] == [2]string{"pixiesFolder", NoFolder}
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%s: %v", path, got)
		}
	}
}

func TestPixiesFolderExpandsHome(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "pixies")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := PixiesFolder("~/pixies", home); !ok || got != dir {
		t.Fatal(got, ok)
	}
	if _, ok := PixiesFolder("", home); ok {
		t.Fatal("empty is not a folder")
	}
}
