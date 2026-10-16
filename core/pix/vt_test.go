package pix

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestTheScreenFollowsCursorMovesAndErases(t *testing.T) {
	v := newScreen(4, 10)
	v.Write([]byte("hello\x1b[2;3Hab\x1b[1;1Hj\x1b[3BX\r\x1b[2Cy\x1b[1;4H\x1b[K"))
	want := []string{"jel", "  ab", "", " Xy"}
	if got := v.Lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
	v.Write([]byte("\x1b[2J\x1b[H\x1b[2Gok"))
	if got := v.Lines(); got[0] != " ok" || got[1] != "" {
		t.Fatalf("%q", got)
	}
}

func TestTheScreenScrollsAtTheBottom(t *testing.T) {
	v := newScreen(2, 5)
	v.Write([]byte("a\r\nb\r\nc"))
	if got := v.Lines(); got[0] != "b" || got[1] != "c" {
		t.Fatalf("%q", got)
	}
}

func TestASequenceSplitAcrossWritesIsKept(t *testing.T) {
	v := newScreen(2, 10)
	v.Write([]byte("ab\x1b[1;"))
	v.Write([]byte("5Hc"))
	if got := v.Lines()[0]; got != "ab  c" {
		t.Fatalf("%q", got)
	}
}

// A real Claude Code 2.1.301 session (120x50) up to its permission dialogs.
func realScreen(t *testing.T, name string) *screen {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	v := newScreen(50, 120)
	v.Write(b)
	return v
}

func TestTheBashDialogIsReadOffTheScreen(t *testing.T) {
	d, ok := readDialog(realScreen(t, "claude-bash-dialog.raw").Lines(), claudePermission)
	want := dialog{Title: "Bash command", Summary: "Print the product of 6 and 7 with Python",
		Body: "python3 -c 'print(6*7)'", Question: "Do you want to proceed?",
		Options: []string{"Yes", "Yes, and don’t ask again for: python3 *", "Yes, and switch to auto mode", "No"}}
	if !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("%v %+v", ok, d)
	}
}

func TestTheWriteDialogIsReadOffTheScreen(t *testing.T) {
	d, ok := readDialog(realScreen(t, "claude-write-dialog.raw").Lines(), claudePermission)
	want := dialog{Title: "Create file", Summary: "notes.txt", Body: "1 hi", Question: "Do you want to create notes.txt?",
		Options: []string{"Yes", "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "No"}}
	if !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("%v %+v", ok, d)
	}
}

func TestNoDialogIsRead(t *testing.T) {
	for _, lines := range [][]string{
		{"❯ 1. Yes", "  2. No"},                        // no question
		{"Do you want to proceed?", "❯ 1. Yes"},        // one option is no dialog
		{"Do you want to proceed?", "1. Yes", "3. No"}, // not numbered in order
	} {
		if d, ok := readDialog(lines, claudePermission); ok {
			t.Errorf("%q: %+v", lines, d)
		}
	}
}
