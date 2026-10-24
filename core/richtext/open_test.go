package richtext

import (
	"reflect"
	"testing"
)

func TestOpenPlan(t *testing.T) {
	const f = "/Users/me/my app/main.go"
	cases := []struct {
		name, app, editor string
		line, col         int
		dir               bool
		want              []string
	}{
		{"default app only", "", EditorAuto, 12, 0, false,
			[]string{"file:///Users/me/my%20app/main.go"}},
		{"the session's IDE first", "com.jetbrains.WebStorm", EditorAuto, 12, 3, false,
			[]string{"webstorm://open?column=3&file=%2FUsers%2Fme%2Fmy+app%2Fmain.go&line=12", "file:///Users/me/my%20app/main.go"}},
		{"VS Code at a line", "com.microsoft.VSCode", EditorAuto, 12, 0, false,
			[]string{"vscode://file/Users/me/my%20app/main.go:12", "file:///Users/me/my%20app/main.go"}},
		{"a terminal isn't an editor", "com.apple.Terminal", EditorAuto, 12, 0, false,
			[]string{"file:///Users/me/my%20app/main.go"}},
		{"the chosen editor wins", "com.jetbrains.WebStorm", EditorZed, 12, 4, false,
			[]string{"zed://file/Users/me/my%20app/main.go:12:4", "file:///Users/me/my%20app/main.go"}},
		{"chosen default app skips the IDE", "com.microsoft.VSCode", EditorDefault, 12, 0, false,
			[]string{"file:///Users/me/my%20app/main.go"}},
		{"no line", "", EditorCursor, 0, 0, false,
			[]string{"cursor://file/Users/me/my%20app/main.go", "file:///Users/me/my%20app/main.go"}},
		{"a folder opens in Finder", "com.microsoft.VSCode", EditorVSCode, 0, 0, true,
			[]string{"file:///Users/me/my%20app/main.go"}},
	}
	for _, c := range cases {
		if got := OpenPlan(f, c.line, c.col, c.dir, c.app, c.editor); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}

func TestEveryEditorHasAPlan(t *testing.T) {
	for _, e := range Editors {
		got := OpenPlan("/a.go", 1, 0, false, "", e)
		if e == EditorAuto || e == EditorDefault {
			if len(got) != 1 {
				t.Errorf("%s: %q", e, got)
			}
			continue
		}
		if len(got) != 2 {
			t.Errorf("%s: %q", e, got)
		}
	}
}
