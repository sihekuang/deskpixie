package pix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// #306: pix's Enter picked the highlighted option of a Codex startup menu (it ran Codex's
// update). The captures are a real Codex 0.164.0 (120x50) with a scratch CODEX_HOME, answered
// with Esc only: its update menu (a version.json naming 9.9.9), its hooks review (pix's
// PreToolUse hook), its folder trust (a new git repository), and its prompt. *-answered.raw is
// what it drew after Esc, up to its prompt.

func TestCodexMenusAreReadOffTheScreen(t *testing.T) {
	for file, want := range map[string]string{
		"codex-update-menu.raw": "update",
		"codex-hooks-menu.raw":  "hooks",
		"codex-trust-menu.raw":  "trust",
		"codex-prompt.raw":      "",
		"codex-approval.raw":    "", // its approval prompt is a question pix asks Pixie (#105)
	} {
		if got := menuOn("codex", realScreen(t, file).Lines()); got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
	for _, menu := range []string{"update", "hooks"} {
		v := realScreen(t, "codex-"+menu+"-menu.raw")
		v.Write(readTestdata(t, "codex-"+menu+"-answered.raw"))
		if got := menuOn("codex", v.Lines()); got != "" {
			t.Errorf("%s answered: %q", menu, got)
		}
		if !strings.Contains(strings.Join(v.Lines(), "\n"), "Ask Codex to do anything") {
			t.Errorf("%s answered: no prompt", menu)
		}
	}
}

// Codex draws its prompt first and its update menu over it a moment later: until then the
// screen shows no menu, which is why pix waits menuSettle after the agent starts.
func TestCodexDrawsItsPromptBeforeItsStartupMenu(t *testing.T) {
	b := readTestdata(t, "codex-update-menu.raw")
	v := newScreen(50, 120)
	prompt := false
	for i := 0; i < len(b); i += 64 {
		v.Write(b[i:min(i+64, len(b))])
		screen := strings.Join(v.Lines(), "\n")
		prompt = prompt || strings.Contains(screen, "Ask Codex to do anything")
		if menuOn("codex", v.Lines()) != "" {
			break
		}
	}
	if !prompt {
		t.Fatal("the menu showed before the prompt")
	}
}

func TestOnlyAMenuAtTheBottomOfTheScreenIsOne(t *testing.T) {
	for name, c := range map[string]struct {
		tool  string
		lines []string
		want  string
	}{
		"codex picker": {"codex", []string{"  Pick a directory", "› 1. Here", "  2. There", "", "  enter continue · esc back"}, "menu"},
		"quoted in codex's transcript": {"codex", []string{"• The update menu ends with:", "  Update available · 0.1 → 0.2",
			"  enter continue · esc skip", "", "› Ask Codex to do anything", "  ? for shortcuts"}, ""},
		"claude trust": {"claude", claudeTrust, "trust"},
		"claude trust, No highlighted": {"claude", append(append([]string(nil), claudeTrust[:9]...),
			"   1. Yes, I trust this folder", " ❯ 2. No, exit"), "trust"},
		"quoted in claude's transcript": {"claude", []string{"⏺ Update(menus_test.go)", `  12 + "Quick safety check: Is this a project"`,
			`  13 + "❯ 1. Yes, I trust this folder"`, "", "❯ ", "  ? for shortcuts"}, ""},
		"claude's trust in codex": {"codex", claudeTrust, ""},
		"empty":                   {"codex", []string{"", ""}, ""},
	} {
		if got := menuOn(c.tool, c.lines); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// claudeTrust is Claude Code 2.1.310's folder trust dialog, built from its text (not captured:
// running Claude Code to see it changes your own Claude config).
var claudeTrust = []string{
	"────────────────────────────────────────",
	" Accessing workspace:",
	"",
	" /tmp/project",
	"",
	" Quick safety check: Is this a project you created or one you trust? (Like your own code, a",
	" well-known open source project, or work from your team). If not, take a moment to review",
	" what's in this folder first.",
	" Claude Code'll be able to read, edit, and execute files here.",
	" ❯ 1. Yes, I trust this folder",
	"   2. No, exit",
	"",
	" Enter to confirm · Esc to cancel",
}

// TestMain: pix's other tests type at once, as if the agent had been up for a while.
func TestMain(m *testing.M) {
	menuSettle = 0
	os.Exit(m.Run())
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// menuHarness runs a Codex at 120x50 that draws the menu capture, takes one key (yours), draws
// answered (if any), and stays.
func menuHarness(t *testing.T, menu, answered string, settle time.Duration) *harness {
	t.Helper()
	defer func(d time.Duration) { menuSettle = d }(menuSettle) // the session keeps its own
	menuSettle = settle
	dir := t.TempDir()
	after := filepath.Join(dir, "answered")
	var b []byte
	if answered != "" {
		b = readTestdata(t, answered)
	}
	if err := os.WriteFile(after, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return agentHarness(t, "codex", fmt.Sprintf("stty rows 50 cols 120 raw -echo; cat %q; head -c 1 >/dev/null; cat %q; sleep 30",
		filepath.Join(mustAbs(t, "testdata"), menu), after))
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func waitMenu(t *testing.T, h *harness, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		h.s.mu.Lock()
		got := h.s.menu
		h.s.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("menu %q, want %q", got, want)
		}
	}
}

func promptCmd(t *testing.T, h *harness, id, command, text string) wire.Msg {
	t.Helper()
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: id, Command: command, Text: text})
	return waitEvent(t, h.d.events, command+" result", func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == id })
}

// A message (and "send it anyway") waits while a menu only you should answer is on screen, and
// says which: nothing is typed into it.
func TestAMessageWaitsWhileACodexMenuShows(t *testing.T) {
	for file, reason := range map[string]string{
		"codex-update-menu.raw": "update", "codex-hooks-menu.raw": "hooks", "codex-trust-menu.raw": "trust",
	} {
		t.Run(reason, func(t *testing.T) {
			h := menuHarness(t, file, "", 0)
			waitMenu(t, h, reason)
			if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != reason {
				t.Fatalf("%+v", r)
			}
			if r := promptCmd(t, h, "2", "flush", ""); r.Status != "waiting" || r.Reason != reason {
				t.Fatalf("flush: %+v", r)
			}
			time.Sleep(TypingQuiet + time.Second) // the flusher polls meanwhile
			if got := h.s.typedForTest(); got != "" {
				t.Fatalf("typed into the menu: %q", got)
			}
		})
	}
}

// At its prompt, Codex gets the message at once.
func TestAMessageIsTypedAtCodexsPrompt(t *testing.T) {
	h := menuHarness(t, "codex-prompt.raw", "", 0)
	agentShown(t, h, "Ask Codex")
	waitMenu(t, h, "")
	if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "typed" {
		t.Fatalf("%+v", r)
	}
	if got := h.s.typedForTest(); got != "\x1b[200~hello\x1b[201~\r" {
		t.Fatalf("%q", got)
	}
}

// You answer the menu (Esc, or arrows and Enter); Codex shows its prompt, and the message that
// waited is typed then, not before.
func TestAHeldMessageIsTypedOnceYouAnswerTheMenu(t *testing.T) {
	for name, keys := range map[string][]string{"esc": {"\x1b"}, "arrow and enter": {"\x1b[B", "\r"}} {
		t.Run(name, func(t *testing.T) {
			h := menuHarness(t, "codex-update-menu.raw", "codex-update-answered.raw", 300*time.Millisecond)
			waitMenu(t, h, "update")
			if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != "update" {
				t.Fatalf("%+v", r)
			}
			for _, k := range keys {
				h.stdinW.Write([]byte(k))
				time.Sleep(50 * time.Millisecond)
			}
			waitMenu(t, h, "")
			answered := time.Now()
			waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })
			want := strings.Join(keys, "") + "\x1b[200~hello\x1b[201~\r"
			if got := h.s.typedForTest(); got != want {
				t.Fatalf("typed %q, want %q", got, want)
			}
			for _, w := range h.s.writesForTest() {
				if strings.Contains(w.b, "hello") && w.at.Before(answered) {
					t.Fatal("typed before the menu was answered")
				}
			}
		})
	}
}

// A message that comes as Codex starts, before its startup menu is drawn, waits too: it is
// "starting", then the menu shows and it waits for that.
func TestAMessageAsCodexStartsWaitsForItsMenu(t *testing.T) {
	defer func(d time.Duration) { menuSettle = d }(menuSettle)
	menuSettle = 2 * time.Second
	h := agentHarness(t, "codex", fmt.Sprintf("stty rows 50 cols 120 raw -echo; sleep 0.5; cat %q; sleep 30",
		filepath.Join(mustAbs(t, "testdata"), "codex-update-menu.raw")))
	if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != "starting" {
		t.Fatalf("%+v", r)
	}
	waitMenu(t, h, "update")
	if r := promptCmd(t, h, "2", "flush", ""); r.Status != "waiting" || r.Reason != "update" {
		t.Fatalf("flush: %+v", r)
	}
	time.Sleep(menuSettle + time.Second)
	if got := h.s.typedForTest(); got != "" {
		t.Fatalf("typed into the menu: %q", got)
	}
}
