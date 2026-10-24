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

// The dialog as drawn in the screenshot of #105: the command in a box with only a left border.
func TestADialogWithABorderedBoxIsRead(t *testing.T) {
	lines := []string{
		"────────────────────────────────────────",
		" Bash command",
		" Tip: auto mode handles these prompts for you",
		" Fetch hourly forecast for tonight",
		"",
		" │ curl -s --max-time 15 \"https://api.open-meteo.com/v1/forecast?latitude=47.7918&lon",
		" │ gitude=-122.2243\" | python3 -c \"",
		" │ import json,sys",
		" │ \"",
		"",
		" This command requires approval",
		"",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and don’t ask again for: python3 -c ' *",
		"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
		"   4. No",
		"",
		" Esc to cancel · Tab to amend",
	}
	d, ok := readDialog(lines, claudePermission)
	want := dialog{Title: "Bash command", Summary: "Fetch hourly forecast for tonight",
		Body:     "curl -s --max-time 15 \"https://api.open-meteo.com/v1/forecast?latitude=47.7918&lon\ngitude=-122.2243\" | python3 -c \"\nimport json,sys\n\"",
		Question: "Do you want to proceed?",
		Options:  []string{"Yes", "Yes, and don’t ask again for: python3 -c ' *", "Yes, and switch to auto mode", "No"}}
	if !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("%v %+v", ok, d)
	}
}

// A real Codex 0.162.1 approval (120x50): every option with the key Codex shows for it.
func TestTheCodexApprovalIsReadOffTheScreen(t *testing.T) {
	d, ok := readCodexDialog(realScreen(t, "codex-approval.raw").Lines())
	want := dialog{Question: "Would you like to run the following command?",
		Summary: "Do you want to allow the exact requested command to create hi.txt in the workspace?",
		Body:    "touch hi.txt",
		Options: []string{"Yes, proceed", "Yes, and don't ask again for commands that start with `touch hi.txt`", "No, and tell Codex what to do differently"},
		Keys:    []string{"y", "p", "\x1b"}}
	if !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("%v %+v", ok, d)
	}
}

// #271: an option Codex wraps (it wraps at its own width, here after a hyphen) is read whole,
// with the key on its last line.
func TestAWrappedCodexOptionIsRead(t *testing.T) {
	lines := []string{
		"  Would you like to run the following command?",
		"  Environment: local",
		"  Reason: Allow creating zz-codex-1.txt in the current workspace?",
		"  $ touch zz-codex-1.txt",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for commands that start with `touch zz-codex-",
		"     1.txt` (p)",
		"  3. No, and tell Codex what to do differently (esc)",
		"  Press enter to confirm or esc to cancel",
	}
	d, ok := readCodexDialog(lines)
	want := dialog{Question: "Would you like to run the following command?",
		Summary: "Allow creating zz-codex-1.txt in the current workspace?",
		Body:    "touch zz-codex-1.txt",
		Options: []string{"Yes, proceed", "Yes, and don't ask again for commands that start with `touch zz-codex-1.txt`", "No, and tell Codex what to do differently"},
		Keys:    []string{"y", "p", "\x1b"}}
	if !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("%v %+v", ok, d)
	}
	// The selection arrow on the wrapped option (you moved to it): read the same.
	arrowed := append([]string{}, lines...)
	arrowed[4], arrowed[5] = "  1. Yes, proceed (y)", "› 2. Yes, and don't ask again for commands that start with `touch zz-codex-"
	if d, ok := readCodexDialog(arrowed); !ok || !reflect.DeepEqual(d, want) {
		t.Fatalf("arrowed: %v %+v", ok, d)
	}
	// Wrapped at a space, its words keep their space.
	lines[5], lines[6] = "  2. Yes, and don't ask again for commands that start with", "     `touch zz-codex-1.txt` (p)"
	if d, ok := readCodexDialog(lines); !ok || d.Options[1] != want.Options[1] {
		t.Fatalf("%v %q", ok, d.Options)
	}
}

func TestNoCodexApprovalIsRead(t *testing.T) {
	for _, lines := range [][]string{
		{"Would you like to run the following command?", "› 1. Yes, proceed (y)"},                                                                                     // no footer
		{"› 1. Yes, proceed (y)", "  2. No (esc)", "Press enter to confirm or esc to cancel"},                                                                         // no question
		{"Would you like to run the following command?", "› 1. Yes, proceed", "  2. No (esc)", "Press enter to confirm or esc to cancel"},                             // a key missing
		{"Would you like to run the following command?", "› 1. Yes, proceed (y)", "  2. Yes, and don't ask", "     again", "Press enter to confirm or esc to cancel"}, // wrapped, never a key
	} {
		if d, ok := readCodexDialog(lines); ok {
			t.Errorf("%q: %+v", lines, d)
		}
	}
}

// A real dialog whose second option wraps mid-path at the screen's edge (e2e run of #105).
func TestAWrappedOptionIsJoinedAsShown(t *testing.T) {
	d, ok := readDialog(realScreen(t, "claude-wrapped-dialog.raw").Lines(), claudePermission)
	want := []string{"Yes",
		"Yes, and always allow access to /private/tmp/claude-501/-Users-daniel-Documents-Projects-deskpixie/da090dd0-caf7-46cb-b134-83c8ca08a17e/scratchpad/tui-probe/work from this project",
		"Yes, and switch to auto mode", "No"}
	if !ok || !reflect.DeepEqual(d.Options, want) {
		t.Fatalf("%v %q", ok, d.Options)
	}
}

// Half drawn: no footer yet, and what is below the options is the status bar, not a label.
func TestADialogStillBeingDrawnIsNotRead(t *testing.T) {
	lines := []string{
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and switch to auto mode",
		"",
		"  Clauding… (4s · ↓ 50 tokens)",
		"────────────────────────────────────────",
	}
	if d, ok := readDialog(lines, claudePermission); ok {
		t.Fatalf("%+v", d)
	}
}
