package pix

import (
	"strings"
	"testing"
	"time"
)

func TestGateClearWhenNothingTyped(t *testing.T) {
	var g Gate
	if !g.Clear(time.Unix(100, 0)) {
		t.Fatal("fresh gate should be clear")
	}
}

// TestGateIgnoresFocusReports (#138): switching windows sends ESC[I / ESC[O. That isn't
// typing, so it must neither shut the gate nor restart its quiet time.
func TestGateIgnoresFocusReports(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("\x1b[O"), t0)
	g.Keys([]byte("\x1b[I\x1b[O"), t0.Add(time.Second))
	if !g.Clear(t0.Add(time.Second)) {
		t.Fatal("focus reports alone must leave the gate clear")
	}
	g.Keys([]byte("\x1b[Ihi"), t0.Add(2*time.Second))
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("text typed after a focus report is still pending")
	}
	g.Keys([]byte("\x7f\x7f"), t0.Add(11*time.Second))
	if !g.Clear(t0.Add(13 * time.Second)) {
		t.Fatal("a focus report must not count as a key: deleting the text leaves the gate clear")
	}
}

// Mouse reports as fullscreen Claude Code asks the terminal for them (SGR, every move: ?1003
// and ?1006): a move, a scroll and a click with its release.
const mouseReports = "\x1b[<35;40;12M\x1b[<64;40;12M\x1b[<0;40;12M\x1b[<0;40;12m"

// TestGateIgnoresMouseReports (#138): moving, scrolling or clicking in the terminal isn't
// typing, so it must not shut the gate.
func TestGateIgnoresMouseReports(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte(mouseReports), t0)
	if !g.Clear(t0) {
		t.Fatal("mouse reports alone must leave the gate clear")
	}
	g.Keys([]byte(mouseReports+"hi"), t0.Add(time.Second))
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("text typed among mouse reports is still pending")
	}
}

// TestOnlyReportsIsNotYou (#138): a pending question Pixie may answer is dropped when you
// start answering it yourself; moving the mouse over the terminal isn't that.
func TestOnlyReportsIsNotYou(t *testing.T) {
	for _, in := range []string{"\x1b[I", "\x1b[O", mouseReports, "\x1b[O" + mouseReports} {
		if !onlyReports([]byte(in)) {
			t.Errorf("%q is the terminal, not you", in)
		}
	}
	for _, in := range []string{"y", "\r", "\x1b", "\x1b[A", mouseReports + "1"} {
		if onlyReports([]byte(in)) {
			t.Errorf("%q is you", in)
		}
	}
}

// TestGateFocusReportKeepsQuietTime (#138): a focus change after you stop typing must not
// restart the quiet time, or a window switch would delay every message by TypingQuiet.
func TestGateFocusReportKeepsQuietTime(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("a\x7f"), t0)
	g.Keys([]byte("\x1b[O"), t0.Add(time.Second))
	if !g.Clear(t0.Add(TypingQuiet + 100*time.Millisecond)) {
		t.Fatal("quiet time counts from your last key, not from the focus change")
	}
}

func TestGateHoldsWhileTyping(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("hel"), t0)
	if g.Clear(t0.Add(time.Second)) {
		t.Fatal("typed text pending: not clear")
	}
	if g.Clear(t0.Add(5 * time.Second)) {
		t.Fatal("unsent text after quiet time: still not clear")
	}
	g.Keys([]byte("\r"), t0.Add(6*time.Second))
	g.Started()
	if g.Clear(t0.Add(7 * time.Second)) {
		t.Fatal("within quiet period after Enter: not clear")
	}
	if !g.Clear(t0.Add(8 * time.Second)) {
		t.Fatal("after Enter and quiet: clear")
	}
}

func TestGateBackspaceToEmpty(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("ab\x7f\x7f"), t0)
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("typed and deleted: clear after quiet")
	}
}

func TestGateCountsRunesNotBytes(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("é\x7f"), t0) // one rune (two bytes), one backspace
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("one rune deleted by one backspace")
	}
}

func TestGateUnsureAfterEditingKey(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("ab\x1b[D"), t0) // arrow key: the line can't be followed any more
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("unsure: not clear")
	}
	g.Keys([]byte("\r"), t0.Add(11*time.Second))
	g.Started() // the line had text: clear once the turn starts
	if !g.Clear(t0.Add(13 * time.Second)) {
		t.Fatal("Enter resets unsure")
	}
}

func TestGateCtrlCClears(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("abc\x03"), t0)
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("Ctrl-C discards the line")
	}
}

func TestSanitizeStripsPasteEnd(t *testing.T) {
	got := Sanitize("hi\x1b[201~rm -rf /\x07\nnext")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) || got != "hi[201~rm -rf /\nnext" {
		t.Fatalf("%q", got)
	}
}

func TestPaste(t *testing.T) {
	if got := string(Paste("a\nb")); got != "\x1b[200~a\nb\x1b[201~" { // Enter goes on its own (#305)
		t.Fatalf("%q", got)
	}
}

func TestQueueLimit(t *testing.T) {
	var q Queue
	for i := 0; i < MaxQueue; i++ {
		if _, err := q.Push("m"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Push("one too many"); err != ErrQueueFull {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if len(q.Drain()) != MaxQueue || q.Len() != 0 {
		t.Fatal("drain")
	}
}

func TestGateLoneEscapeOnEmptyLineStaysClear(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte{0x1b}, t0) // you pressed Esc to interrupt; nothing typed
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("a lone Esc on an empty line must not block delivery")
	}
	g.Keys([]byte("ab"), t0.Add(3*time.Second))
	g.Keys([]byte{0x1b}, t0.Add(3*time.Second))
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("Esc with text typed: unsure")
	}
}

// Keys that add a line to your draft, or accept a completion, leave your text in the box.
func TestGateMultiLineKeysKeepTheDraft(t *testing.T) {
	t0 := time.Unix(100, 0)
	for name, keys := range map[string][]string{
		"Option+Enter (ESC CR)": {"fix the", "\x1b\r"},
		"Ctrl-J":                {"fix the", "\n"},
		"backslash Enter":       {"fix the \\", "\r"},
		"pasted lines":          {"fix the", "one\rtwo"},
	} {
		var g Gate
		for _, k := range keys {
			g.Keys([]byte(k), t0)
		}
		if g.Clear(t0.Add(10 * time.Second)) {
			t.Errorf("%s: your draft is still there, not clear", name)
		}
	}
}

func TestGateEnterWaitsForTheTurnToStart(t *testing.T) {
	// Enter may have only picked a completion (@file); the text is gone once the agent works.
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("look at @src/ma"), t0)
	g.Keys([]byte("\r"), t0)
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("Enter alone: not clear until the turn starts")
	}
	g.Started()
	if !g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("the turn started: clear")
	}
}

func TestGateSlashCommandAndEmptyEnterNeedNoTurn(t *testing.T) {
	t0 := time.Unix(100, 0)
	var g Gate
	g.Keys([]byte("/status"), t0)
	g.Keys([]byte("\r"), t0)
	if !g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("a slash command runs on Enter: clear")
	}
	var e Gate
	e.Keys([]byte("\r"), t0)
	if !e.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("Enter on an empty line: clear")
	}
}

func TestSessionStopOpensTheGate(t *testing.T) {
	// A Codex turn can end before its busy marker shows; its Stop still means the line was sent.
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.gate.Keys([]byte("fix it"), time.Now())
	h.s.gate.Keys([]byte("\r"), time.Now())
	h.s.Hook("Stop", map[string]any{})
	if !h.s.gate.Clear(time.Now().Add(TypingQuiet)) {
		t.Fatal("after Stop the gate must be clear")
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// startupReplies are a terminal's answers to what Claude Code 2.1.304 asks at startup (#181):
// device attributes (ESC[c), keyboard protocol flags (ESC[?u), its version (ESC[>0q, a DCS
// string), Warp's own query (ESC]7501;?, an OSC string), plus a mode report and a cursor
// position, which other agents ask for.
var startupReplies = []string{
	"\x1b[?62;22;52c", "\x1b[>1;10;0c", "\x1b[?5u", "\x1b[?0u",
	"\x1bP>|WarpTerminal(v0.2026.10.14)\x1b\\", "\x1b]7501;1\x07", "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\",
	"\x1b[?2026;2$y", "\x1b[24;1R",
	"\x1b_Gi=31;OK\x1b\\", // Warp's answer to Claude Code asking whether it can draw images (#185)
}

// TestGateIgnoresTerminalReplies (#181): the terminal answering Claude Code's startup
// questions isn't you typing. Counted as keys it couldn't follow, they shut the gate until an
// Enter that a session you haven't typed into never gets, and Pixie's messages waited forever.
func TestGateIgnoresTerminalReplies(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte(strings.Join(startupReplies, "")), t0)
	for _, r := range startupReplies {
		g.Keys([]byte(r), t0)
		if !onlyReports([]byte(r)) {
			t.Errorf("%q is the terminal, not you", r)
		}
	}
	if !g.Clear(t0) {
		t.Fatal("terminal replies shut the gate")
	}
	// Typing around them still counts.
	g.Keys([]byte("hi\x1b[?0u"), t0)
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("your text was lost with the reply")
	}
}

// TestGateReadsKittyKeys (#181): Claude Code turns on the kitty keyboard protocol, in which
// Esc arrives as ESC[27u and Ctrl-C as ESC[99;5u. They mean what they meant before.
func TestGateReadsKittyKeys(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("\x1b[27u"), t0) // Esc on an empty line: interrupting the agent
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("a kitty Esc on an empty line shut the gate")
	}
	g.Keys([]byte("abc"), t0.Add(3*time.Second))
	g.Keys([]byte("\x1b[99;5u"), t0.Add(4*time.Second)) // Ctrl-C clears the line
	if !g.Clear(t0.Add(6 * time.Second)) {
		t.Fatal("a kitty Ctrl-C didn't clear the line")
	}
}

// TestGateReadsKittyEnter (#185): Enter as the kitty keyboard protocol may send it.
func TestGateReadsKittyEnter(t *testing.T) {
	for _, enter := range []string{"\x1b[13u", "\x1b[13;1u"} {
		var g Gate
		t0 := time.Unix(100, 0)
		g.Keys([]byte("/help"), t0)
		g.Keys([]byte(enter), t0)
		if !g.Clear(t0.Add(2 * time.Second)) {
			t.Fatalf("%q: a /command sent with kitty Enter left the gate shut", enter)
		}
	}
}

// TestGateAfterWarpsStartupReplies (#185): Warp's replies exactly as pix logged them on Oct 20.
// The image reply ("Gi=31;OK", an APC string) counted as 10 typed characters, and its closing
// backslash made your next Enter a line continuation; the gate then waited for a turn that
// never came, and Pixie's message was held.
func TestGateAfterWarpsStartupReplies(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("\x1bP>|Warp(v0.2026.10.14.08.27.stable_01)\x1b\\\x1b[?5u\x1b[?62c"), t0)
	g.Keys([]byte("\x1b[?2026;2$y\x1b_Gi=31;OK\x1b\\\x1b[?62c"), t0)
	if !g.Clear(t0) {
		t.Fatalf("shut by Warp's replies: %s", g.State())
	}
	g.Keys([]byte("\r"), t0.Add(time.Second)) // Enter on an empty box: no turn follows
	if !g.Clear(t0.Add(3 * time.Second)) {
		t.Fatalf("an Enter on an empty box shut the gate: %s", g.State())
	}
}

// #187: the gate keeps the text of your draft while it can follow it, so a forced message can set
// it aside and put it back.
func TestGateKeepsYourDraft(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("fix the héader"), t0)
	g.Keys([]byte{0x7f, 0x7f}, t0)
	g.Keys([]byte("\x1b[200~ and\nfooter\x1b[201~"), t0) // a paste
	if d, ok := g.Draft(); !ok || d != "fix the héad and\nfooter" {
		t.Fatalf("%q %t", d, ok)
	}
	g.Keys([]byte("\x1b[D"), t0) // an arrow: pix can't follow the line any more
	if _, ok := g.Draft(); ok {
		t.Fatal("known after an arrow key")
	}
	g.Keys([]byte{0x03}, t0)
	if d, ok := g.Draft(); !ok || d != "" {
		t.Fatalf("after Ctrl-C: %q %t", d, ok)
	}
	g.Restore("put back")
	if d, ok := g.Draft(); !ok || d != "put back" || g.Clear(t0.Add(time.Hour)) {
		t.Fatalf("restored: %q %t clear=%t", d, ok, g.Clear(t0.Add(time.Hour)))
	}
}
