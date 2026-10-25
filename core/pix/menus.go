package pix

import (
	"regexp"
	"strings"
	"time"
)

// Menus an agent shows in place of its prompt (#306): Codex's update, folder and hooks
// questions at startup and its list of agents, Claude Code's folder trust. Enter there picks
// the highlighted option (pix's Enter once ran Codex's update), and only you should answer
// them: pix types nothing while one shows, and says which one it waits for.

// codexMenuFoot is the key line under a Codex picker ("enter continue · esc skip"). It is the
// last line on the screen; at its prompt Codex shows its own footer there instead.
var codexMenuFoot = regexp.MustCompile(`^enter \S.* · esc \S+$`)

// claudeMenuRows: how far up from Claude Code's trust dialog's footer (its last line, "Enter to
// confirm · Esc to cancel", or "Press Ctrl-C again to exit" after one Ctrl-C) its question and
// option are.
const claudeMenuRows = 15

// promptMarks start the row of each agent's prompt (its input box).
var promptMarks = map[string]string{"codex": "›", "claude": "❯"}

// promptOf is what the agent's prompt holds on lines, without spaces: the last row that starts
// with its mark (in Claude's, right under its rule) and the rows it wrapped onto (indented), up
// to a blank row or Claude's rule. Not the transcript above, nor the status and key lines under
// it.
func promptOf(tool string, lines []string) string {
	mark := promptMarks[tool]
	if mark == "" {
		return ""
	}
	at := -1
	for i := len(lines) - 1; i >= 0 && at < 0; i-- {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " "), mark) && (tool != "claude" || ruledAbove(lines[:i])) {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	rows := []string{strings.TrimPrefix(strings.TrimLeft(lines[at], " "), mark)}
	for _, l := range lines[at+1:] {
		if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "─") || !strings.HasPrefix(l, " ") {
			break
		}
		rows = append(rows, l)
	}
	return noSpace(strings.Join(rows, ""))
}

// codexPromptMarks show in Codex's empty prompt and under it: pix knows it has drawn its prompt.
var codexPromptMarks = []string{noSpace("Ask Codex to do anything"), noSpace("? for shortcuts")}

// startupMenus are the menus Codex or Claude Code show before you reach the prompt: your keys
// there answered them, not a draft.
var startupMenus = map[string]bool{"update": true, "trust": true, "hooks": true, "agents": true}

// menuOn is the menu lines show in place of tool's prompt: "update", "trust" (the folder),
// "hooks", "agents" (Codex's Agent command center), "menu" (another one), or "" for none.
func menuOn(tool string, lines []string) string {
	var rows []string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			rows = append(rows, t)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	last := rows[len(rows)-1]
	switch {
	case tool == "codex" && strings.Contains(last, "esc back") && strings.Contains(last, "enter open"):
		return "agents"
	case tool == "codex" && codexMenuFoot.MatchString(last):
		for _, t := range rows {
			switch {
			case strings.HasPrefix(t, "Update available · "):
				return "update"
			case t == "Hooks need review":
				return "hooks"
			case t == "Folder access":
				return "trust"
			}
		}
		return "menu"
	case tool == "claude" && (strings.Contains(last, "to confirm") && strings.Contains(last, "to cancel") ||
		strings.HasSuffix(last, "again to exit")):
		// Its question and its option, right above its footer.
		ask, yes := false, false
		for _, t := range rows[max(0, len(rows)-claudeMenuRows):] {
			ask = ask || strings.HasPrefix(t, "Quick safety check:")
			opt := strings.TrimSpace(strings.TrimPrefix(t, "❯"))
			if i := strings.Index(opt, ". "); i > 0 && i <= 2 {
				opt = opt[i+2:] // "1. " when it numbers them
			}
			yes = yes || opt == "Yes, I trust this folder"
		}
		if ask && yes {
			return "trust"
		}
	}
	return ""
}

// menuSettle is how long Codex's screen must show no menu, after it first draws its prompt or
// a menu leaves, before pix types: Codex draws its prompt first and a startup menu over it a
// moment later (0.2-1 s seen), and one menu may follow another. Claude Code draws its trust
// dialog before anything else, so it gets no wait. A session keeps the one it started with.
var menuSettle = 3 * time.Second

// freshWait: how long after a resize (a fresh screen, drawn again from nothing) a menu that
// seems gone is still taken as shown.
const freshWait = time.Second

// menuHold is why pix can't type now for the agent's menus: the menu on screen, "starting"
// while one may still be drawn, or "" when it can.
func (s *Session) menuHold() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.menu != "":
		return s.menu
	case s.o.tool == "codex" && s.now().Sub(s.menuAt) < s.settle:
		return "starting"
	}
	return ""
}

// menuShown is the menu on screen now ("" for none).
func (s *Session) menuShown() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.menu
}

// menuUp: a menu showed before pix's Enter, so the message wasn't sent. pasted: its text was
// typed (behind the menu), not only held back.
type menuUp struct {
	menu   string
	pasted bool
}

func (m menuUp) Error() string { return "a menu showed before Enter: " + m.menu }

// menuWaits says, for the coordinator to pass on, which menu tool waits on you for.
func menuWaits(tool, menu string) string {
	name := map[string]string{"codex": "Codex", "claude": "Claude Code"}[tool]
	switch menu {
	case "update":
		return name + " is waiting for you to answer its update question."
	case "trust":
		return name + " is waiting for you to say whether to trust its folder."
	case "hooks":
		return name + " is waiting for you to say whether to trust its hooks."
	case "agents":
		return name + " is showing its list of agents; only you should leave it."
	}
	return name + " is showing a menu only you should answer."
}

// noteMenu keeps the menu lines show (PTY reader goroutine only). Keys you pressed while a
// startup menu showed answered it, so once it leaves nothing of yours is left in the prompt.
// Until Codex first draws its prompt, its startup menus may still come.
func (s *Session) noteMenu(lines []string, text string) {
	menu := menuOn(s.o.tool, lines)
	s.mu.Lock()
	was := s.menu
	now := s.now()
	if menu == "" && was != "" && now.Sub(s.freshAt) < freshWait {
		menu = was // a resize: the menu is drawn again in a moment
	}
	s.menu = menu
	if !s.promptSeen && now.Sub(s.startAt) >= promptWait {
		s.promptSeen = true // it never showed its usual prompt: stop waiting for it
	}
	if menu != "" || !s.promptSeen {
		s.menuAt = now
	}
	if s.o.tool == "codex" && menu == "" && !s.promptSeen {
		for _, p := range codexPromptMarks {
			s.promptSeen = s.promptSeen || strings.Contains(text, p)
		}
	}
	s.mu.Unlock()
	if menu == was {
		return
	}
	if menu == "" {
		s.o.log.Printf("menu %s answered", was)
		if startupMenus[was] {
			s.gate.Sent()
		}
		return
	}
	s.o.log.Printf("menu %s on screen: holding messages", menu)
}

// promptWait: how long after the start Codex's grace waits for its prompt at most (its first
// turn ending ends it too).
const promptWait = 20 * time.Second

// promptDrawn: Codex's prompt is up (its first turn ended), whatever its screen shows.
func (s *Session) promptDrawn() {
	s.mu.Lock()
	s.promptSeen = true
	s.mu.Unlock()
}

// unsent is a message a menu kept from being sent (#306): its text, typed behind the menu (""
// when the menu showed before it was), the messages in it, your draft pix set aside for it
// (typed back once it's sent), the turns started when it was typed, and whether a hook said
// you sent it.
type unsent struct {
	text, draft string
	msgs        []string
	turns       int
	sent        bool
}

// submitted: the agent took prompt (its hook says so); if it holds the unsent message, you
// sent it from the terminal.
func (s *Session) submitted(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hookPrompts = s.hookPrompts || prompt != ""
	if u := s.unsent; u != nil && u.text != "" && strings.Contains(norm(prompt), norm(u.text)) {
		u.sent = true
	}
}

// holdBack keeps msgs that a menu stopped (err, a menuUp): queued again if nothing was typed
// and no draft of yours was set aside, else unsent until the menu is answered. false for
// another error.
func (s *Session) holdBack(err error, msgs []string, text, draft string) bool {
	m, ok := err.(menuUp)
	if !ok {
		return false
	}
	if !m.pasted && draft == "" {
		s.queue.Requeue(msgs)
		return true
	}
	if !m.pasted {
		text = ""
	}
	s.mu.Lock()
	s.unsent = &unsent{text: text, draft: draft, msgs: msgs, turns: s.turns}
	s.mu.Unlock()
	return true
}

func (s *Session) hasUnsent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsent != nil
}

// finishUnsent sends the unsent message once its menu is gone, the agent idle and you aren't
// typing: Enter if its prompt still holds it, then your draft back. If the prompt doesn't (you
// cleared it), it is queued again; forced past your draft before the menu, it is forced again.
// A prompt the agent took that holds it (submitted) means you sent it. sent: it reached the
// agent. Under flushMu.
func (s *Session) finishUnsent() (sent bool) {
	s.mu.Lock()
	u := s.unsent
	// Without a hook that says what it took, a turn that ran with the message in the transcript.
	yours := u != nil && (u.sent || s.turns != u.turns && !s.hookPrompts && s.inTranscript(u.text))
	s.mu.Unlock()
	switch {
	case u == nil:
		return false
	case yours: // you pressed Enter on it yourself
		s.dropUnsent()
		s.o.log.Printf("the message a menu stopped was sent from the terminal")
		return true
	case s.currentStatus() != "idle" || s.menuHold() != "" || !s.gate.Clear(time.Now()):
		return false
	}
	if u.text != "" && s.promptHolds(u.text) {
		entered, err := s.runner.EnterIf(func() bool { return s.menuShown() == "" })
		if err != nil || !entered {
			return false
		}
		s.dropUnsent()
		s.o.log.Printf("pressed Enter on the message a menu stopped")
		s.restoreDraft(u.draft)
		return true
	}
	s.dropUnsent()
	if u.text == "" && u.draft != "" {
		// "Send it anyway" before the menu: still forced, the messages ahead of your draft. Its
		// deletes may have gone to the menu, leaving your draft in the prompt.
		if s.promptHolds(u.draft) {
			s.gate.Restore(u.draft)
		} else {
			s.gate.Sent()
		}
		if err := s.forceIn(u.msgs); err != nil {
			s.o.log.Printf("sending the messages a menu stopped: %v", err)
			s.queue.Requeue(u.msgs)
			return false
		}
		if s.hasUnsent() { // a menu stopped them again
			return false
		}
		if !s.promptHolds(u.draft) { // your draft was set aside, not deleted for good
			s.restoreDraft(u.draft)
		}
		return true
	}
	if u.text != "" {
		s.o.log.Printf("the message a menu stopped is no longer in the prompt: typing it again")
	}
	s.queue.Requeue(u.msgs)
	s.restoreDraft(u.draft)
	return false
}

// inTranscript: text shows on screen above the prompt, sent. Under mu.
func (s *Session) inTranscript(text string) bool {
	t := noSpace(text)
	return t != "" && strings.Contains(s.screen, t) && !strings.Contains(s.prompt, t)
}

func (s *Session) dropUnsent() {
	s.mu.Lock()
	s.unsent = nil
	s.mu.Unlock()
}

// ruledAbove: the last non-blank of lines is Claude's rule, the top of its prompt box (its
// dialogs' options and your past prompts have none right above).
func ruledAbove(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return strings.HasPrefix(t, "─")
		}
	}
	return false
}

// promptHolds: the agent's prompt shows text (its first paragraph: a blank line ends what
// promptOf reads), or a long paste it shows as a marker.
func (s *Session) promptHolds(text string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n\n")
	if noSpace(first) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range []string{noSpace(first), noSpace("[Pasted Content"), noSpace("[Pasted text")} {
		if strings.Contains(s.prompt, m) {
			return true
		}
	}
	return false
}

// restoreDraft types your draft back after pix set it aside ("": none), unless the prompt still
// shows it (what deleted it went to a menu).
func (s *Session) restoreDraft(draft string) {
	if draft == "" {
		return
	}
	time.Sleep(forceSettle)
	s.gate.Restore(draft)
	if s.promptHolds(draft) {
		return
	}
	s.runner.WriteToAgent(Paste(draft))
}

// holdLogEvery: how often pix.log says how long a message has waited, so a menu misread as
// shown stands out. A session keeps the one it started with.
var holdLogEvery = 30 * time.Second
