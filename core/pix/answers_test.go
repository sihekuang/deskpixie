package pix

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// #359: pix is a pipe between Pixie and the agent's own UI. Its hooks only tell pix what is
// asked, and return at once: the question shows in the terminal as the agent shows it, and
// Pixie answers it there by typing what you would.

// hookAsync runs a hook the way Claude Code does, waiting on its reply.
func hookAsync(h *harness, event string, p map[string]any) chan string {
	reply := make(chan string, 1)
	go func() { reply <- h.s.Hook(event, p) }()
	return reply
}

// pending waits until the hook made its question the one waiting (before its dialog is drawn).
func pending(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for h.s.currentQuestion() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no question waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hookReply(t *testing.T, reply chan string, within time.Duration) map[string]any {
	t.Helper()
	select {
	case r := <-reply:
		if r == "" {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(r), &out); err != nil {
			t.Fatalf("hook reply %q: %v", r, err)
		}
		return out
	case <-time.After(within):
		t.Fatalf("the hook didn't reply within %v", within)
	}
	return nil
}

// noQuestionGone: pix doesn't take its question back from DeskPixie within d.
func noQuestionGone(t *testing.T, h *harness, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-h.d.events:
			if m.Event == "question-gone" {
				t.Fatalf("question %s gone", m.ID)
			}
		case <-deadline:
			return
		}
	}
}

var pushMenu = map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
	map[string]any{"question": "Push main?", "header": "Push", "multiSelect": false,
		"options": []any{map[string]any{"label": "Push", "description": "Send it"}, map[string]any{"label": "Don't push"}}}}}}

var codexAsk = map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
	map[string]any{"id": "database", "header": "Database", "question": "Which database?",
		"options": []any{map[string]any{"label": "Postgres", "description": "Relational"}, map[string]any{"label": "SQLite"}}}}}}

// The menus as Claude Code 2.1.310 and Codex 0.164 draw them.
const (
	claudePushMenu = " ☐ Push\r\nPush main?\r\n❯ 1. Push\r\n     Send it\r\n  2. Don't push\r\n  3. Type something.\r\n" +
		"────────────────────\r\n  4. Chat about this\r\nEnter to select · ↑/↓ to navigate · Esc to cancel"
	codexDatabaseMenu = "  Question 1/1 (1 unanswered)\r\n  Which database?\r\n  › 1. Postgres  Relational\r\n    2. SQLite\r\n" +
		"    3. None of the above  Optionally, add details in notes (tab)\r\n  tab to add notes | enter to submit answer | esc to interrupt"
)

// pipeHarness runs an agent that takes every key and draws nothing once it says it's ready
// (its terminal echoes no key after): from then on the test alone draws its screens, as only
// the PTY reader may otherwise.
func pipeHarness(t *testing.T, tool string) *harness {
	t.Helper()
	h := agentHarness(t, tool, `stty raw -echo; printf 'ready\r\n'; cat >/dev/null`)
	waitScreen(t, h, "ready")
	return h
}

// draw shows screen as the agent's whole screen.
func draw(h *harness, screen string) { h.s.runner.OnOutput([]byte("\x1b[2J\x1b[H" + screen)) }

func answerAsync(h *harness, id, choice string) chan string {
	errs := make(chan string, 1)
	go func() {
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "a-" + id + choice, Command: "answer", Text: id, Source: choice})
		errs <- waitEventCh(h.d.events, func(m wire.Msg) bool { return m.ID == "a-"+id+choice }).Err
	}()
	return errs
}

// waitEventCh is waitEvent for a goroutine: the first event ok takes, or an empty one after 10 s.
func waitEventCh(events chan wire.Msg, ok func(wire.Msg) bool) wire.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		select {
		case m := <-events:
			if ok(m) {
				return m
			}
		case <-ctx.Done():
			return wire.Msg{Err: "no reply"}
		}
	}
}

// waitTyped waits until pix has typed want into the agent, in all.
func waitTyped(t *testing.T, h *harness, want string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); h.s.typedForTest() != want; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("typed %q, want %q", h.s.typedForTest(), want)
		}
	}
}

func TestTheQuestionHooksReturnAtOnce(t *testing.T) {
	h := keyHarness(t, "claude")
	start := time.Now()
	if out := hookReply(t, hookAsync(h, "PreToolUse", pushMenu), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
	q := askedQuestion(t, h)
	if q.Source != "menu" || strings.Join(q.Keys, "|") != "Push|Don't push" || strings.Join(q.Details, "|") != "Send it|" || !q.Typed {
		t.Fatalf("%+v", q)
	}
	h.s.clearQuestion("test")
	if out := hookReply(t, hookAsync(h, "PermissionRequest", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "npm test"}}), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("the hooks took %v", d)
	}
}

func TestAClaudeMenuOptionIsItsDigit(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	if err := answer(t, h, q.ID, "3"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	errs := answerAsync(h, q.ID, "2")
	waitTyped(t, h, "2")
	draw(h, "⏺ User answered Claude's questions:\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

// Your own words go where you would type them: "Type something." (its digit opens it), then
// Enter. On one line: Enter would send the rest.
func TestAClaudeMenuTakesYourOwnWords(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	errs := answerAsync(h, q.ID, "text:  push it\nlater ")
	waitTyped(t, h, "3")
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. Type something.", 1))
	waitTyped(t, h, "3push it later")
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. push it later", 1))
	waitTyped(t, h, "3push it later\r")
	draw(h, "⏺ User answered Claude's questions:\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

// "Chat about this" is Claude's own option after "Type something.": its digit picks it.
func TestAClaudeMenusChatIsItsChatAboutThis(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	errs := answerAsync(h, q.ID, "chat")
	waitTyped(t, h, "4")
	draw(h, "⏺ User declined to answer questions\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

// With eight options, "Chat about this" is the tenth: no digit picks it.
func TestAClaudeMenuOfEightOptionsHasNoChatDigit(t *testing.T) {
	var opts []any
	for _, l := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		opts = append(opts, map[string]any{"label": l})
	}
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which letter?", "options": opts}}}})
	q := askedQuestion(t, h)
	draw(h, "Which letter?\r\n❯ 1. a\r\nEnter to select")
	if err := answer(t, h, q.ID, "chat"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	if got := h.s.typedForTest(); got != "" {
		t.Fatalf("typed %q", got)
	}
}

// A step the agent didn't take (its screen never shows it) stops the answer there. Its menu
// may not be as it was, so it still waits, asked again without its buttons: you finish it in
// its terminal.
func TestAnAnswerStepTheAgentDidntTakeStopsIt(t *testing.T) {
	defer func(w time.Duration) { stepWait = w }(stepWait)
	stepWait = 200 * time.Millisecond
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "own", Command: "answer", Text: q.ID, Source: "text:later"})
	var again, reply *wire.Msg
	waitEvent(t, h.d.events, "the reply and the question again", func(m wire.Msg) bool {
		switch {
		case m.ID == "own":
			reply = &m
		case m.Event == "question" && m.ID == q.ID:
			again = &m
		}
		return reply != nil && again != nil
	})
	if reply.Err != "The session didn't take all of the answer: finish it in its terminal." {
		t.Fatalf("%q", reply.Err)
	}
	if got := h.s.typedForTest(); got != "3" { // not your words into a menu that didn't open
		t.Fatalf("typed %q", got)
	}
	if len(again.Keys) != 0 || again.Typed {
		t.Fatalf("asked again with its buttons: %+v", again)
	}
	if err := answer(t, h, q.ID, "1"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
}

// A key of yours between the steps: you are answering it yourself, so the rest isn't typed
// (your words would go wherever your key left them) and it leaves Pixie.
func TestAKeyOfYoursStopsAnAnswerBetweenItsSteps(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	errs := answerAsync(h, q.ID, "text:later")
	waitTyped(t, h, "3")
	h.s.runner.OnInput([]byte{0x1b}) // you close the menu
	draw(h, "────────\r\n❯ \r\n────────\r\n")
	if err := <-errs; err != "You're answering it in its terminal." {
		t.Fatalf("%q", err)
	}
	if got := h.s.typedForTest(); got != "3" {
		t.Fatalf("typed %q", got)
	}
	if h.s.currentQuestion() != nil {
		t.Fatal("still waiting")
	}
}

// Marks the screen doesn't draw (a variation selector, a combining accent) don't keep your
// words from showing.
func TestOwnWordsWithMarksTheScreenDoesntDrawShow(t *testing.T) {
	words := "✔️ cafe\u0301"
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	errs := answerAsync(h, q.ID, "text:"+words)
	waitTyped(t, h, "3")
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. Type something.", 1))
	waitTyped(t, h, "3"+words)
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. "+words, 1))
	waitTyped(t, h, "3"+words+"\r")
	draw(h, "⏺ User answered Claude's questions:\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

// Your words already shown elsewhere (an option's own label) don't count as typed: the line
// they are typed on does.
func TestOwnWordsCountOnTheirOwnLine(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	errs := answerAsync(h, q.ID, "text:Push")
	waitTyped(t, h, "3")
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. Type something.", 1))
	waitTyped(t, h, "3Push")
	time.Sleep(300 * time.Millisecond)
	if got := h.s.typedForTest(); got != "3Push" { // no Enter before the field shows them
		t.Fatalf("typed %q", got)
	}
	draw(h, strings.Replace(strings.Replace(claudePushMenu, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. Push", 1))
	waitTyped(t, h, "3Push\r")
	draw(h, "⏺ User answered Claude's questions:\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

func TestAnEmptyOwnAnswerIsNone(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	draw(h, claudePushMenu)
	if err := answer(t, h, q.ID, "text: \n "); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
}

// A menu Pixie can't show (several questions at once) is only announced: you answer it in
// its terminal.
func TestAMenuPixieCantShowIsOnlyAnnounced(t *testing.T) {
	one := map[string]any{"question": "Which?", "options": []any{map[string]any{"label": "A"}}}
	var nine []any
	for i := range 9 {
		nine = append(nine, map[string]any{"label": strconv.Itoa(i + 1)})
	}
	many := map[string]any{"question": "Which number?", "options": nine}
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{one, many}}})
	q := askedQuestion(t, h)
	if len(q.Keys) != 0 || q.Typed {
		t.Fatalf("%+v", q)
	}
	draw(h, "Which? (and 1 more)\r\n❯ 1. A\r\nEnter to select")
	for _, choice := range []string{"1", "text:A", "chat"} {
		if err := answer(t, h, q.ID, choice); !strings.Contains(err, "isn't one of its answers") {
			t.Fatalf("%s: %q", choice, err)
		}
	}
	if got := h.s.typedForTest(); got != "" {
		t.Fatalf("typed %q", got)
	}
}

// Codex's menu takes a digit too, picking its option.
func TestACodexMenuOptionIsItsDigit(t *testing.T) {
	h := pipeHarness(t, "codex")
	h.s.Hook("PreToolUse", codexAsk)
	q := askedQuestion(t, h)
	if q.Source != "menu" || strings.Join(q.Keys, "|") != "Postgres|SQLite" || !q.Typed || strings.Join(q.Details, "|") != "Relational|" {
		t.Fatalf("%+v", q)
	}
	draw(h, codexDatabaseMenu)
	errs := answerAsync(h, q.ID, "2")
	waitTyped(t, h, "2")
	draw(h, "  • Which database?\r\n    answer: SQLite\r\n")
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
}

// Your own words go in the notes of Codex's "None of the above": Down to it, Tab, then Enter.
// Talking it over first is such a note too.
func TestACodexMenuTakesYourOwnWords(t *testing.T) {
	for choice, words := range map[string]string{"text: MariaDB ": "MariaDB", "chat": "I'd like to talk this over"} {
		t.Run(choice, func(t *testing.T) {
			h := pipeHarness(t, "codex")
			h.s.Hook("PreToolUse", codexAsk)
			q := askedQuestion(t, h)
			draw(h, codexDatabaseMenu)
			errs := answerAsync(h, q.ID, choice)
			down := "\x1b[B\x1b[B"
			waitTyped(t, h, down)
			other := strings.Replace(strings.Replace(codexDatabaseMenu, "› 1.", "  1.", 1), "  3. None", "› 3. None", 1)
			draw(h, other)
			waitTyped(t, h, down+"\t")
			draw(h, strings.Replace(other, "  tab to add notes", "  › Add notes\r\n  tab or esc to clear notes", 1))
			for end := time.Now().Add(5 * time.Second); !strings.HasPrefix(h.s.typedForTest(), down+"\t"+words); time.Sleep(10 * time.Millisecond) {
				if time.Now().After(end) {
					t.Fatalf("typed %q", h.s.typedForTest())
				}
			}
			typedWords := strings.TrimPrefix(h.s.typedForTest(), down+"\t")
			draw(h, strings.Replace(other, "  tab to add notes", "  › "+typedWords+"\r\n  tab or esc to clear notes", 1))
			waitTyped(t, h, down+"\t"+typedWords+"\r")
			draw(h, "  • Which database?\r\n    answer: None of the above\r\n")
			if err := <-errs; err != "" {
				t.Fatal(err)
			}
		})
	}
}

// A secret (typed in Codex's terminal only) or several questions: only announced.
func TestACodexQuestionPixieCantShowIsOnlyAnnounced(t *testing.T) {
	secret := map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
		map[string]any{"id": "token", "question": "Your token?", "isSecret": true,
			"options": []any{map[string]any{"label": "Skip"}}}}}}
	h := pipeHarness(t, "codex")
	h.s.Hook("PreToolUse", secret)
	if q := askedQuestion(t, h); len(q.Keys) != 0 || q.Typed {
		t.Fatalf("%+v", q)
	}
}

// Claude's own menu tool name means nothing to Codex, and Codex's nothing to Claude.
func TestEachAgentAsksOnlyItsOwnQuestionTool(t *testing.T) {
	claudeInCodex := map[string]any{"tool_name": "AskUserQuestion", "tool_input": codexAsk["tool_input"]}
	for tool, p := range map[string]map[string]any{"codex": claudeInCodex, "claude": codexAsk} {
		h := pipeHarness(t, tool)
		h.s.Hook("PreToolUse", p)
		if q := h.s.currentQuestion(); q != nil {
			t.Fatalf("%s asked %+v", tool, q)
		}
	}
}

// You answer the dialog in the terminal first: the bubble goes away.
func TestAPermissionAnsweredInTheTerminalLeavesPixie(t *testing.T) {
	h := keyHarness(t, "claude")
	q := askBash(t, h, "npm test")
	h.s.runner.OnOutput([]byte(bashDialog))
	h.stdinW.Write([]byte("1"))
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
}

// A permission's options are the dialog's own keys: "don't ask again" too.
func TestAPermissionsDontAskAgainIsItsDigit(t *testing.T) {
	h := screensHarness(t, bashDialog, "\x1b[2J\x1b[H* Bash(npm test)\r\n", 0)
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	var q wire.Msg
	waitEvent(t, h.d.events, "the dialog as shown", func(m wire.Msg) bool {
		q = m
		return m.Event == "question" && len(m.Keys) == 3
	})
	if q.Keys[1] != "Yes, and don't ask again for: npm test" {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "2"); err != "" {
		t.Fatal(err)
	}
	if got := h.s.typedForTest(); got != "2" {
		t.Fatalf("typed %q", got)
	}
}

// #400: a question asked while DeskPixie was away (quit, crashed, restarting) is asked again
// once pix registers, as it was told; one answered isn't.
func TestAQuestionAskedWhileDeskPixieWasAwayIsAskedOnReconnect(t *testing.T) {
	h := screensHarness(t, bashDialog, "\x1b[2J\x1b[H* Bash(npm test)\r\n", 0)
	if hasQuestion(h.s.resend(), "") {
		t.Fatal("asked again with no question waiting")
	}
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	var q wire.Msg
	waitEvent(t, h.d.events, "the dialog as shown", func(m wire.Msg) bool {
		q = m
		return m.Event == "question" && len(m.Keys) == 3
	})
	again := h.s.resend()
	if !hasQuestion(again, q.ID) {
		t.Fatalf("not asked again on reconnect: %+v", again)
	}
	for _, m := range again {
		if m.Event == "question" && (m.Source != "permission" || len(m.Keys) != 3 || m.Keys[1] != q.Keys[1]) {
			t.Fatalf("asked again as %+v, told %+v", m, q)
		}
	}
	h.s.clearQuestion("UserPromptSubmit")
	if hasQuestion(h.s.resend(), "") {
		t.Fatal("an answered question asked again")
	}
}

// hasQuestion: msgs asks question id (any, for "").
func hasQuestion(msgs []wire.Msg, id string) bool {
	for _, m := range msgs {
		if m.Event == "question" && (id == "" || m.ID == id) {
			return true
		}
	}
	return false
}

// #364: a menu of several questions, and a question of several picks, as Claude Code 2.1.311
// and Codex 0.164 draw them: a tab each, then Claude's review.

func claudeTwo() map[string]any {
	return map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which color?", "header": "Color", "options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Green"}}},
		map[string]any{"question": "Which size?", "header": "Size", "options": []any{map[string]any{"label": "Small"}, map[string]any{"label": "Large"}}}}}}
}

func claudeToppings() map[string]any {
	return map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which toppings?", "header": "Pick", "multiSelect": true, "options": []any{
			map[string]any{"label": "Cheese"}, map[string]any{"label": "Ham"}, map[string]any{"label": "Olives"}}}}}}
}

// claudeTabPage draws a tab of Claude's menu: its question, options (with their boxes when it
// takes several picks) and the cursor on option at.
func claudeTabPage(tabs, question string, options []string, ticked map[int]bool, multi bool, at int) string {
	// Your prompt stays above it, naming every question (#364).
	lines := []string{"❯ Ask me 'Which color?', 'Which size?' and 'Which toppings?'", "←  " + tabs + "  ✔ Submit  →", question}
	for i, o := range options {
		cursor := "  "
		if i+1 == at {
			cursor = "❯ "
		}
		if multi {
			box := "[ ]"
			if ticked[i+1] {
				box = "[✔]"
			}
			o = box + " " + o
		}
		lines = append(lines, fmt.Sprintf("%s%d. %s", cursor, i+1, o))
	}
	n := len(options)
	lines = append(lines, fmt.Sprintf("  %d. Type something.", n+1), "────────────────────", fmt.Sprintf("  %d. Chat about this", n+2),
		"Enter to select · Tab/Arrow keys to navigate · Esc to cancel")
	return strings.Join(lines, "\r\n")
}

func claudeReviewPage(answers ...string) string {
	return "←  ☒ Color  ✔ Submit  →\r\nReview your answers\r\n" + strings.Join(answers, "\r\n") +
		"\r\nReady to submit your answers?\r\n❯ 1. Submit answers\r\n  2. Cancel"
}

// answerSeen is answerAsync, handing back too the events pix sent before its reply: a menu's
// next question comes then.
func answerSeen(h *harness, id, choice string) chan []wire.Msg {
	got := make(chan []wire.Msg, 1)
	go func() {
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "a-" + id + choice, Command: "answer", Text: id, Source: choice})
		var evs []wire.Msg
		for {
			m := waitEventCh(h.d.events, func(wire.Msg) bool { return true })
			evs = append(evs, m)
			if m.ID == "a-"+id+choice || m.Err == "no reply" {
				got <- evs
				return
			}
		}
	}()
	return got
}

// answered: the reply to an answer took it, its question went, and the menu's next question
// came (nil: none).
func answered(t *testing.T, evs []wire.Msg, id string) *wire.Msg {
	t.Helper()
	if r := evs[len(evs)-1]; r.Err != "" {
		t.Fatalf("answer %s: %q (%+v)", id, r.Err, evs)
	}
	var next *wire.Msg
	gone := false
	for _, m := range evs {
		if m.Event == "question-gone" && m.ID == id {
			gone = true
		}
		if m.Event == "question" {
			next = &m
		}
	}
	if !gone {
		t.Fatalf("%s not gone: %+v", id, evs)
	}
	return next
}

// Each question is answered on its tab, as Claude goes on to the next; then its review, whose
// Submit answers sends them, shows what each was answered.
func TestAMenuOfSeveralQuestionsIsAnsweredInTurn(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", claudeTwo())
	q := askedQuestion(t, h)
	if q.Text != "Which color? (1 of 2)" || strings.Join(q.Keys, "|") != "Red|Green" || !q.Typed || q.Multi {
		t.Fatalf("%+v", q)
	}
	draw(h, claudeTabPage("☐ Color  ☐ Size", "Which color?", []string{"Red", "Green"}, nil, false, 1))
	evs := answerSeen(h, q.ID, "2")
	waitTyped(t, h, "2")
	size := claudeTabPage("☒ Color  ☐ Size", "Which size?", []string{"Small", "Large"}, nil, false, 1)
	draw(h, size)
	q2 := answered(t, <-evs, q.ID)
	if q2 == nil || q2.Text != "Which size? (2 of 2)" || strings.Join(q2.Keys, "|") != "Small|Large" || !q2.Typed {
		t.Fatalf("%+v", q2)
	}
	evs = answerSeen(h, q2.ID, "text:Medium")
	waitTyped(t, h, "23")
	draw(h, strings.Replace(size, "  3. Type something.", "❯ 3. Type something.", 1))
	waitTyped(t, h, "23Medium")
	draw(h, strings.Replace(size, "  3. Type something.", "❯ 3. Medium", 1))
	waitTyped(t, h, "23Medium\r")
	draw(h, claudeReviewPage(" ● Which color?", "   → Green", " ● Which size?", "   → Medium"))
	q3 := answered(t, <-evs, q2.ID)
	if q3 == nil || q3.Text != "Ready to submit your answers? Which color? → Green · Which size? → Medium" ||
		strings.Join(q3.Keys, "|") != "Submit answers|Cancel" || q3.Typed {
		t.Fatalf("%+v", q3)
	}
	if err := answer(t, h, q3.ID, "text:hi"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	evs = answerSeen(h, q3.ID, "1")
	waitTyped(t, h, "23Medium\r1")
	draw(h, "⏺ User answered Claude's questions:")
	if next := answered(t, <-evs, q3.ID); next != nil {
		t.Fatalf("after the review: %+v", next)
	}
}

// A question of several picks ticks each with its digit, then Tab goes on; a single digit isn't
// its answer. Cancel on the review ends the menu.
func TestAQuestionOfSeveralPicksTicksEach(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", claudeToppings())
	q := askedQuestion(t, h)
	if q.Text != "Which toppings?" || !q.Multi || q.Typed || strings.Join(q.Keys, "|") != "Cheese|Ham|Olives" {
		t.Fatalf("%+v", q)
	}
	opts := []string{"Cheese", "Ham", "Olives"}
	draw(h, claudeTabPage("☐ Pick", "Which toppings?", opts, nil, true, 1))
	for _, bad := range []string{"2", "text:Ham", "chat", "picks:", "picks:4", "picks:1,1"} {
		if err := answer(t, h, q.ID, bad); !strings.Contains(err, "isn't one of its answers") {
			t.Fatalf("%s: %q", bad, err)
		}
	}
	evs := answerSeen(h, q.ID, "picks:3,1")
	waitTyped(t, h, "1")
	// A tick marks its tab answered at once.
	draw(h, claudeTabPage("☒ Pick", "Which toppings?", opts, map[int]bool{1: true}, true, 1))
	waitTyped(t, h, "13")
	draw(h, claudeTabPage("☒ Pick", "Which toppings?", opts, map[int]bool{1: true, 3: true}, true, 1))
	waitTyped(t, h, "13\t")
	draw(h, claudeReviewPage(" ● Which toppings?", "   → Cheese, Olives"))
	q2 := answered(t, <-evs, q.ID)
	if q2 == nil || q2.Text != "Ready to submit your answers? Which toppings? → Cheese, Olives" {
		t.Fatalf("%+v", q2)
	}
	evs = answerSeen(h, q2.ID, "2")
	waitTyped(t, h, "13\t2")
	draw(h, "⏺ User declined to answer questions")
	if next := answered(t, <-evs, q2.ID); next != nil {
		t.Fatalf("after Cancel: %+v", next)
	}
}

// A pick that doesn't tick leaves the question halfway: finished in its terminal.
func TestAPickThatDoesntTickIsFinishedInTheTerminal(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", claudeToppings())
	q := askedQuestion(t, h)
	draw(h, claudeTabPage("☐ Pick", "Which toppings?", []string{"Cheese", "Ham", "Olives"}, nil, true, 1))
	if err := answer(t, h, q.ID, "picks:1,2"); !strings.Contains(err, "finish it in its terminal") {
		t.Fatalf("%q", err)
	}
	if got := h.s.typedForTest(); got != "1" {
		t.Fatalf("typed %q", got)
	}
}

// Talking a question over ends the menu: no question after it is asked.
func TestChatOnAQuestionEndsTheMenu(t *testing.T) {
	h := pipeHarness(t, "claude")
	h.s.Hook("PreToolUse", claudeTwo())
	q := askedQuestion(t, h)
	draw(h, claudeTabPage("☐ Color  ☐ Size", "Which color?", []string{"Red", "Green"}, nil, false, 1))
	evs := answerSeen(h, q.ID, "chat")
	waitTyped(t, h, "4")
	draw(h, "⏺ User wants to chat about this")
	if next := answered(t, <-evs, q.ID); next != nil {
		t.Fatalf("%+v", next)
	}
}

// Codex asks its questions in turn too: answering the last sends them all, with no review.
// One of several picks it doesn't take: the menu is only announced.
func TestACodexMenuOfSeveralQuestionsIsAnsweredInTurn(t *testing.T) {
	h := pipeHarness(t, "codex")
	two := map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
		map[string]any{"id": "color", "question": "Which color?", "options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Green"}}},
		map[string]any{"id": "size", "question": "Which size?", "options": []any{map[string]any{"label": "Small"}, map[string]any{"label": "Large"}}}}}}
	h.s.Hook("PreToolUse", two)
	q := askedQuestion(t, h)
	if q.Text != "Which color? (1 of 2)" || !q.Typed {
		t.Fatalf("%+v", q)
	}
	page := func(n int, question, a, b, foot string) string {
		return fmt.Sprintf("› Ask me 'Which color?' and 'Which size?'\r\n  Question %d/2\r\n  %s\r\n  › 1. %s\r\n    2. %s\r\n    3. None of the above  Optionally, add details in notes (tab)\r\n"+
			"  tab to add notes | enter to submit %s | ←/→ to navigate questions | esc to interrupt", n, question, a, b, foot)
	}
	draw(h, page(1, "Which color?", "Red", "Green", "answer"))
	evs := answerSeen(h, q.ID, "2")
	waitTyped(t, h, "2")
	draw(h, page(2, "Which size?", "Small", "Large", "all"))
	q2 := answered(t, <-evs, q.ID)
	if q2 == nil || q2.Text != "Which size? (2 of 2)" {
		t.Fatalf("%+v", q2)
	}
	evs = answerSeen(h, q2.ID, "1")
	waitTyped(t, h, "21")
	draw(h, "  • Which color?\r\n    answer: Green\r\n  • Which size?\r\n    answer: Small")
	if next := answered(t, <-evs, q2.ID); next != nil {
		t.Fatalf("no review in Codex: %+v", next)
	}

	h.s.Hook("PreToolUse", map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Which ones?", "multiSelect": true, "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}}})
	if q := askedQuestion(t, h); len(q.Keys) != 0 || q.Multi {
		t.Fatalf("%+v", q)
	}
}

// Codex's "chat" is a note that answers its question: it goes on to the next one, so that one
// is asked.
func TestACodexChatGoesOnToTheNextQuestion(t *testing.T) {
	h := pipeHarness(t, "codex")
	h.s.Hook("PreToolUse", map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
		map[string]any{"id": "color", "question": "Which color?", "options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Green"}}},
		map[string]any{"id": "size", "question": "Which size?", "options": []any{map[string]any{"label": "Small"}, map[string]any{"label": "Large"}}}}}})
	q := askedQuestion(t, h)
	page := func(n int, question, notes string) string {
		one, other := "› 1. A", "  3. None of the above"
		if notes != "" {
			one, other = "  1. A", "› 3. None of the above"
		}
		foot := map[int]string{1: "answer", 2: "all"}[n] // the last question's sends them all
		return fmt.Sprintf("  Question %d/2\r\n  %s\r\n  %s\r\n    2. B\r\n  %s%s\r\n"+
			"  tab to add notes | enter to submit %s | esc to interrupt", n, question, one, other, notes, foot)
	}
	draw(h, page(1, "Which color?", ""))
	evs := answerSeen(h, q.ID, "chat")
	waitTyped(t, h, "\x1b[B\x1b[B")
	draw(h, page(1, "Which color?", " "))
	waitTyped(t, h, "\x1b[B\x1b[B\t")
	draw(h, page(1, "Which color?", "\r\n  › Add notes"))
	for end := time.Now().Add(5 * time.Second); !strings.Contains(h.s.typedForTest(), "talk"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("typed %q", h.s.typedForTest())
		}
	}
	draw(h, page(1, "Which color?", "\r\n  › I'd like to talk this over"))
	for end := time.Now().Add(5 * time.Second); !strings.HasSuffix(h.s.typedForTest(), "\r"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("typed %q", h.s.typedForTest())
		}
	}
	draw(h, page(2, "Which size?", ""))
	if next := answered(t, <-evs, q.ID); next == nil || next.Text != "Which size? (2 of 2)" {
		t.Fatalf("%+v", next)
	}
}
