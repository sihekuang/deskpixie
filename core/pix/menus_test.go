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
// PreToolUse hook), its folder trust (a new git repository), the Agent command center Esc there
// leads to (codex-agents.raw), and its prompt. *-answered.raw is
// what it drew after Esc, up to its prompt.

func TestCodexMenusAreReadOffTheScreen(t *testing.T) {
	for file, want := range map[string]string{
		"codex-update-menu.raw": "update",
		"codex-hooks-menu.raw":  "hooks",
		"codex-trust-menu.raw":  "trust",
		"codex-prompt.raw":      "",
		"codex-approval.raw":    "",       // its approval prompt is a question pix asks Pixie (#105)
		"codex-agents.raw":      "agents", // Esc on its folder trust (codex-trust-menu.raw's)
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
	quoted := []string{"⏺ The trust dialog reads:"}
	for _, l := range claudeTrust {
		quoted = append(quoted, "  "+l) // a plain quote in a reply, indented
	}
	quoted = append(quoted, "", "────────", "❯ ", "────────", "  ? for shortcuts")
	for name, c := range map[string]struct {
		tool  string
		lines []string
		want  string
	}{
		"codex picker": {"codex", []string{"  Pick a directory", "› 1. Here", "  2. There", "", "  enter continue · esc back"}, "menu"},
		"quoted in codex's transcript": {"codex", []string{"• The update menu ends with:", "  Update available · 0.1 → 0.2",
			"  enter continue · esc skip", "", "› Ask Codex to do anything", "  ? for shortcuts"}, ""},
		"codex agents": {"codex", []string{"  Agent command center", "  No tasks yet", "  ? help  esc back  ↑/↓ move  enter open  n new"}, "agents"},
		"claude trust": {"claude", claudeTrust, "trust"},
		"claude trust, Yes highlighted": {"claude", append(append([]string(nil), claudeTrust[:7]...),
			"   No, exit", " ❯ Yes, I trust this folder", " Enter to confirm · Esc to cancel"), "trust"},
		"claude trust, numbered": {"claude", append(append([]string(nil), claudeTrust[:7]...),
			" ❯ 1. Yes, I trust this folder", "   2. No, exit", " Enter to confirm · Esc to cancel"), "trust"},
		"claude trust after one Ctrl-C": {"claude", append(append([]string(nil), claudeTrust[:9]...),
			" Press Ctrl-C again to exit"), "trust"},
		"claude trust far above its footer": {"claude", append(append(append([]string(nil), claudeTrust[:9]...),
			strings.Split(strings.TrimSuffix(strings.Repeat("x\n", claudeMenuRows), "\n"), "\n")...), claudeTrust[9]), ""},
		"quoted in a claude reply": {"claude", quoted, ""},
		"quoted in claude's diff": {"claude", []string{"⏺ Update(menus_test.go)", `  12 + "Quick safety check: Is this a project"`,
			`  13 + "Yes, I trust this folder"`, "", "❯ ", "  ? for shortcuts"}, ""},
		"claude's trust in codex": {"codex", claudeTrust, ""},
		"empty":                   {"codex", []string{"", ""}, ""},
	} {
		if got := menuOn(c.tool, c.lines); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// claudeTrust is Claude Code 2.1.310's folder trust dialog as it showed in a 160x45 terminal
// (tmux capture-pane, its non-blank lines): unnumbered options, "No, exit" first and highlighted.
var claudeTrust = []string{
	"────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────",
	" Accessing workspace:",
	" /private/tmp/claude-501/-Users-daniel-Documents-Projects-deskpixie/dfed7d1c-6e30-4740-b42b-cc9efc3a91d5/scratchpad/e2e305/work",
	" Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not,",
	" take a moment to review what's in this folder first.",
	" Claude Code'll be able to read, edit, and execute files here.",
	" Security guide",
	" ❯ No, exit",
	"   Yes, I trust this folder",
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

// Pixie's own keys don't answer a menu only you should: the coordinator is told which it is.
func TestPixiesKeysDontAnswerAStartupMenu(t *testing.T) {
	h := menuHarness(t, "codex-update-menu.raw", "", 0)
	waitMenu(t, h, "update")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "keys", Keys: []string{"Enter"}})
	r := waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == "1" })
	if r.Err != "Codex is waiting for you to answer its update question." {
		t.Fatalf("%+v", r)
	}
	if got := h.s.typedForTest(); got != "" {
		t.Fatalf("typed %q", got)
	}

	h = menuHarness(t, "codex-prompt.raw", "", 0)
	agentShown(t, h, "Ask Codex")
	waitMenu(t, h, "")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "keys", Keys: []string{"Enter"}})
	if r := waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == "1" }); r.Err != "" {
		t.Fatalf("%+v", r)
	}
	if got := h.s.typedForTest(); got != "\r" {
		t.Fatalf("typed %q", got)
	}
}

// stage is one step of a staged agent: it reads n bytes (pix's or yours), then draws draw.
type stage struct {
	n    int
	draw []byte
}

// stagedHarness runs a Codex at 120x50 that goes through stages, then stays.
func stagedHarness(t *testing.T, tool string, settle time.Duration, stages ...stage) *harness {
	t.Helper()
	defer func(d time.Duration) { menuSettle = d }(menuSettle) // the session keeps its own
	menuSettle = settle
	dir := t.TempDir()
	script := "stty rows 50 cols 120 raw -echo"
	for i, st := range stages {
		path := filepath.Join(dir, fmt.Sprint(i))
		if err := os.WriteFile(path, st.draw, 0o600); err != nil {
			t.Fatal(err)
		}
		if st.n > 0 {
			script += fmt.Sprintf("; dd bs=1 count=%d of=/dev/null 2>/dev/null", st.n) // exactly n: head reads ahead
		}
		script += fmt.Sprintf("; cat %q", path)
	}
	return agentHarness(t, tool, script+"; sleep 30")
}

// withPrompt is Codex's screen after answered, with text in its prompt (the row its
// placeholder shows on).
func withPrompt(t *testing.T, answered, text string) []byte {
	return append(readTestdata(t, answered), []byte("\x1b[47;1H\x1b[2K› "+text)...)
}

const pasteLen = len("\x1b[200~hello\x1b[201~")

// A menu drawn during the paste stops its Enter. Once you answer it (Esc) and the prompt holds
// the message, pix presses Enter and says it's delivered; if the prompt doesn't hold it any more,
// pix types it again.
func TestAMessageAMenuStoppedIsSentOnceYouAnswerIt(t *testing.T) {
	for name, c := range map[string]struct {
		after []byte
		want  string
	}{
		"still in the prompt": {withPrompt(t, "codex-update-answered.raw", "hello"), "\x1b[200~hello\x1b[201~\x1b\r"},
		"gone from it":        {readTestdata(t, "codex-update-answered.raw"), "\x1b[200~hello\x1b[201~\x1b\x1b[200~hello\x1b[201~\r"},
	} {
		t.Run(name, func(t *testing.T) {
			h := stagedHarness(t, "codex", 0, stage{0, readTestdata(t, "codex-prompt.raw")},
				stage{1, readTestdata(t, "codex-update-menu.raw")}, // the paste brings the menu up
				stage{pasteLen - 1 + 1, c.after})                   // the rest of it, and your Esc
			agentShown(t, h, "Ask Codex")
			waitMenu(t, h, "")
			if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != "update" {
				t.Fatalf("%+v", r)
			}
			if r := promptCmd(t, h, "2", "flush", ""); r.Status != "waiting" || r.Reason != "update" {
				t.Fatalf("flush: %+v", r)
			}
			time.Sleep(500 * time.Millisecond)
			if got := h.s.typedForTest(); got != "\x1b[200~hello\x1b[201~" {
				t.Fatalf("typed %q before the menu was answered", got)
			}
			h.stdinW.Write([]byte{0x1b})
			waitMenu(t, h, "")
			waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })
			if got := h.s.typedForTest(); got != c.want {
				t.Fatalf("typed %q, want %q", got, c.want)
			}
		})
	}
}

// "Send it anyway" sets your draft aside; a menu drawn during the paste keeps it aside until the
// message is sent, then types it back.
func TestSendAnywayKeepsYourDraftWhenAMenuStopsIt(t *testing.T) {
	h := stagedHarness(t, "codex", 0, stage{0, readTestdata(t, "codex-prompt.raw")},
		stage{2 + 2 + 1, readTestdata(t, "codex-update-menu.raw")}, // your "ab", pix's 2 deletes, the paste
		stage{pasteLen - 1 + 1, withPrompt(t, "codex-update-answered.raw", "hello")})
	agentShown(t, h, "Ask Codex")
	waitMenu(t, h, "")
	h.stdinW.Write([]byte("ab"))
	time.Sleep(100 * time.Millisecond)
	if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != "typing" {
		t.Fatalf("%+v", r)
	}
	if r := promptCmd(t, h, "2", "flush", ""); r.Status != "waiting" || r.Reason != "update" {
		t.Fatalf("flush: %+v", r)
	}
	h.stdinW.Write([]byte{0x1b})
	waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })
	want := "ab\x7f\x7f\x1b[200~hello\x1b[201~\x1b\r\x1b[200~ab\x1b[201~"
	for end := time.Now().Add(2 * time.Second); h.s.typedForTest() != want && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.s.typedForTest(); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
	if d, known := h.s.gate.Draft(); d != "ab" || !known {
		t.Fatalf("draft %q %v", d, known)
	}
}

// Codex's wait counts from when it draws its prompt, not from its start: one slow to start still
// gets the wait, as its startup menu comes after its prompt. Claude Code gets none.
func TestCodexWaitsFromItsPromptNotItsStart(t *testing.T) {
	defer func(d time.Duration) { menuSettle = d }(menuSettle)
	menuSettle = time.Second
	h := agentHarness(t, "codex", fmt.Sprintf("stty rows 50 cols 120 raw -echo; sleep 1.5; cat %q; sleep 30",
		filepath.Join(mustAbs(t, "testdata"), "codex-prompt.raw")))
	agentShown(t, h, "Ask Codex")
	if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "waiting" || r.Reason != "starting" {
		t.Fatalf("%+v", r)
	}
	waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })

	menuSettle = 3 * time.Second
	h = agentHarness(t, "claude", `stty raw -echo; echo ready; sleep 30`)
	agentShown(t, h, "ready")
	if r := promptCmd(t, h, "1", "prompt", "hello"); r.Status != "typed" {
		t.Fatalf("claude: %+v", r)
	}
}

// Keys you pressed at a startup menu answered it, so the gate is clear once it goes; at another
// picker (one you may open over your draft) they may be your draft, so it isn't. A resize, which
// draws the screen again from nothing, doesn't count as the menu going.
func TestAStartupMenuGoingClearsTheGate(t *testing.T) {
	update := realScreen(t, "codex-update-menu.raw").Lines()
	picker := []string{"› 1. Here", "  2. There", "  enter continue · esc back"}
	prompt := realScreen(t, "codex-prompt.raw").Lines()
	for name, c := range map[string]struct {
		menu             []string
		fresh            bool
		menuAfter, draft string
	}{
		"startup menu": {update, false, "", ""},
		"other picker": {picker, false, "", "ab"},
		"resize":       {update, true, "update", "ab"},
	} {
		s := newSession(sessionOpts{tool: "codex"})
		s.noteMenu(c.menu, "")
		s.gate.Keys([]byte("a"), time.Now())
		s.gate.Keys([]byte("b"), time.Now())
		if c.fresh {
			s.freshAt = time.Now()
		}
		s.noteMenu(prompt, "")
		if got, _ := s.gate.Draft(); s.menuShown() != c.menuAfter || got != c.draft {
			t.Errorf("%s: menu %q draft %q", name, s.menuShown(), got)
		}
	}
}

// A message that waits long says so in pix.log now and then, so a menu misread as shown shows up.
func TestALongHoldIsLogged(t *testing.T) {
	defer func(d time.Duration) { holdLogEvery = d }(holdLogEvery)
	holdLogEvery = 300 * time.Millisecond
	path := filepath.Join(t.TempDir(), "pix.log")
	h := newLoggedHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", fmt.Sprintf("stty rows 50 cols 120 raw -echo; cat %q; sleep 30",
		filepath.Join(mustAbs(t, "testdata"), "codex-update-menu.raw"))}}, openLog(path, 1<<20, "codex"))
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	waitMenu(t, h, "update")
	promptCmd(t, h, "1", "prompt", "hello")
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "messages waiting") && strings.Contains(string(b), ": update") {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("log: %s", b)
		}
	}
}

// Codex's grace waits for its prompt, but not for good: a screen that keeps changing without
// its usual prompt stops holding 20 s after the start, and its first turn ending ends it too.
func TestCodexsGraceEndsWithoutItsPrompt(t *testing.T) {
	t0 := time.Now()
	for _, c := range []struct {
		name string
		at   time.Duration // checked at, frames every second until then
		want string
		turn bool
	}{{"19 s", 19 * time.Second, "starting", false}, {"25 s", 25 * time.Second, "", false}, {"a turn ended", 5 * time.Second, "", true}} {
		s := newSession(sessionOpts{tool: "codex"})
		s.settle, s.startAt = 3*time.Second, t0
		frame := []string{"  Loading…"}
		for d := time.Duration(0); d <= c.at; d += time.Second {
			s.now = func() time.Time { return t0.Add(d) }
			if c.turn && d == c.at-3*time.Second {
				s.promptDrawn() // what Codex's Stop does
			}
			s.noteMenu(frame, "Loading…")
		}
		if got := s.menuHold(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A turn counts as you sending the stopped message only if its prompt holds it.
func TestATurnWithAnotherPromptDidntSendTheStoppedMessage(t *testing.T) {
	for name, c := range map[string]struct {
		prompt string
		sent   bool
	}{"its own": {"hello", true}, "another": {"something else", false}} {
		s := newSession(sessionOpts{tool: "codex"})
		s.unsent = &unsent{text: "hello", msgs: []string{"hello"}}
		s.turns++
		s.submitted(c.prompt)
		if got := s.finishUnsent(); got != c.sent || s.hasUnsent() {
			t.Errorf("%s: sent %v, unsent %v", name, got, s.hasUnsent())
		}
		if q := s.queue.Len(); c.sent && q != 0 || !c.sent && q != 1 { // not sent: it waits to be typed again
			t.Errorf("%s: %d queued", name, q)
		}
	}
}

// The prompt is the bottom of the screen: the message quoted above it in the transcript isn't in it.
func TestOnlyTheBottomRowsAreThePrompt(t *testing.T) {
	s := newSession(sessionOpts{tool: "codex"})
	v := newScreen(30, 40)
	v.Write([]byte("hello\r\n" + strings.Repeat("line\r\n", promptRows) + "› "))
	s.noteScreen(v)
	if s.promptHolds("hello") {
		t.Fatal("the transcript counted as the prompt")
	}
	v.Write([]byte("hello"))
	s.noteScreen(v)
	if !s.promptHolds("hello") {
		t.Fatal("the prompt didn't count")
	}
}

// "Send it anyway" stopped by a menu before its paste stays forced: once the menu is answered the
// messages go in, then your draft. If the deletes went to the menu, your draft is still in the
// prompt: it is deleted again, not typed twice.
func TestSendAnywayStoppedBeforeItsPasteIsStillForced(t *testing.T) {
	for name, c := range map[string]struct {
		after []byte
		want  string
	}{
		"draft deleted": {readTestdata(t, "codex-update-answered.raw"),
			"ab\x7f\x7f\x1b\x1b[200~hello\x1b[201~\r\x1b[200~ab\x1b[201~"},
		"deletes went to the menu": {withPrompt(t, "codex-update-answered.raw", "ab"),
			"ab\x7f\x7f\x1b\x7f\x7f\x1b[200~hello\x1b[201~\r\x1b[200~ab\x1b[201~"},
	} {
		t.Run(name, func(t *testing.T) {
			h := stagedHarness(t, "codex", 0, stage{0, readTestdata(t, "codex-prompt.raw")},
				stage{2 + 2, readTestdata(t, "codex-update-menu.raw")}, // your "ab", pix's 2 deletes: the menu
				stage{1, c.after}) // your Esc
			agentShown(t, h, "Ask Codex")
			waitMenu(t, h, "")
			h.stdinW.Write([]byte("ab"))
			time.Sleep(100 * time.Millisecond)
			promptCmd(t, h, "1", "prompt", "hello")
			if r := promptCmd(t, h, "2", "flush", ""); r.Status != "waiting" || r.Reason != "update" {
				t.Fatalf("flush: %+v", r)
			}
			if got := h.s.typedForTest(); got != "ab\x7f\x7f" {
				t.Fatalf("typed %q into the menu", got)
			}
			h.stdinW.Write([]byte{0x1b})
			waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })
			for end := time.Now().Add(2 * time.Second); h.s.typedForTest() != c.want && time.Now().Before(end); {
				time.Sleep(20 * time.Millisecond)
			}
			if got := h.s.typedForTest(); got != c.want {
				t.Fatalf("typed %q, want %q", got, c.want)
			}
		})
	}
}

// Delivered means all of them: a message that came while one waited behind the menu is typed
// before it is said.
func TestDeliveredWaitsForEveryMessage(t *testing.T) {
	h := stagedHarness(t, "codex", 0, stage{0, readTestdata(t, "codex-prompt.raw")},
		stage{1, readTestdata(t, "codex-update-menu.raw")},
		stage{pasteLen - 1 + 1, withPrompt(t, "codex-update-answered.raw", "hello")})
	agentShown(t, h, "Ask Codex")
	waitMenu(t, h, "")
	promptCmd(t, h, "1", "prompt", "hello")
	if r := promptCmd(t, h, "2", "prompt", "second"); r.Status != "waiting" || r.Reason != "update" {
		t.Fatalf("%+v", r)
	}
	h.stdinW.Write([]byte{0x1b})
	waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Event == "delivered" })
	if got := h.s.typedForTest(); !strings.HasSuffix(got, "\r\x1b[200~second\x1b[201~\r") {
		t.Fatalf("delivered with %q typed", got)
	}
}
