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
	if got := string(Paste("a\nb")); got != "\x1b[200~a\nb\x1b[201~\r" {
		t.Fatalf("%q", got)
	}
}

func TestQueueLimit(t *testing.T) {
	var q Queue
	for i := 0; i < MaxQueue; i++ {
		if err := q.Push("m"); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Push("one too many"); err != ErrQueueFull {
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
