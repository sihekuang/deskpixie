package pix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/pix/describe"
	"github.com/sihekuang/deskpixie/core/pix/wire"
	"golang.org/x/term"
)

// Screen markers. Codex shows codexBusy while it works (the marker DeskPixie's workers use).
// Claude fires no Stop hook when a turn is interrupted (Esc, yours or Pixie's); it shows
// claudeInterrupted instead. An aborted Codex turn sends no notify; it shows codexInterrupted.
const (
	codexBusy         = "esc to interrupt"
	claudeInterrupted = "What should Claude do instead?"
	codexInterrupted  = "Conversation interrupted"
)

// ansi matches the escape sequences between the words on screen.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// screenWatch finds a marker in the agent's output, even split across reads.
type screenWatch struct{ tail string }

// saw adds chunk and reports whether marker is now on screen (once per appearance). Spaces
// are ignored: a terminal UI often moves the cursor instead of printing one.
func (w *screenWatch) saw(chunk, marker string) bool {
	w.tail += noSpace(ansi.ReplaceAllString(chunk, ""))
	if len(w.tail) > 1024 {
		w.tail = w.tail[len(w.tail)-1024:]
	}
	if strings.Contains(w.tail, noSpace(marker)) {
		w.tail = ""
		return true
	}
	return false
}

type sessionOpts struct {
	tool, description, daemonSock, home, where, app string
	stdin, stdout                                   *os.File
	agent                                           Launch
	log                                             *Log // nil: none
}

// Session is one wrapped agent: its PTY, delivery, and connection to DeskPixie.
type Session struct {
	o      sessionOpts
	runner *Runner
	client *Client
	queue  Queue
	gate   Gate

	mu        sync.Mutex
	status    string // idle | working | blocked
	afterStop bool   // the next prompt starts new work (an auto description)
	sessionID string
	userDesc  string // --description
	outdated  bool   // the app says this pix is older than its own (#189)
	agentDesc string // set_description
	autoDesc  string // first prompt of the current work
	flushing  bool
	flushMu   sync.Mutex // one typing of the queue at a time
	posted    []string   // Pixie's messages not yet seen as prompts (normalized)
	q         *question  // the prompt waiting on you that Pixie may answer (#105)
	landing   *landing   // an answer typed from Pixie, until its prompt leaves the screen (#268)
	screen    string     // what the agent's screen shows now, without spaces (#268)
	qseq      int

	turnStart     time.Time        // when this turn went working (zero: none seen)
	turnFromPixie bool             // a prompt of this turn was Pixie's
	now           func() time.Time // the clock (tests replace it, under mu)
}

// askDescription is added to a turn that starts new work, so the agent describes it: its
// prompt may be Pixie's message or a few words of yours that say little on their own.
const askDescription = "pix: if this starts a new task, call set_description with 3 to 8 words naming the project and the task (for example \"deskpixie: fix login redirect loop\"), so the user and Pixie can tell this session apart."

// norm is s with its spaces collapsed, for matching a prompt to a message Pixie sent.
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// fromPixie reports (and forgets) whether prompt is a message Pixie sent: Claude Code shows it
// as plain text, so only pix, which posted it, can tell it from one you typed.
func (s *Session) fromPixie(prompt string) bool {
	p := norm(prompt)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.posted {
		if m != "" && strings.Contains(p, m) {
			s.posted = append(s.posted[:i], s.posted[i+1:]...)
			return true
		}
	}
	return false
}

// newWork records the description a new piece of work's prompt gives ("" for none).
func (s *Session) newWork(prompt string) {
	d := describe.FromPrompt(prompt)
	if d == "" {
		return
	}
	s.mu.Lock()
	s.autoDesc = d
	s.mu.Unlock()
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: d})
	s.title()
}

// forceIn types msgs past the typing gate, as one prompt (#187), without taking your draft
// with it. Claude Code stashes the draft (Ctrl-S) and puts it back by itself once the prompt is
// sent, whatever pix knows of it. Elsewhere pix deletes the draft it followed, sends, and types
// it back; a draft it couldn't follow (after an arrow key) is sent along with the message.
func (s *Session) forceIn(msgs []string) error {
	if len(msgs) == 0 {
		return nil
	}
	prompt := Paste(strings.Join(msgs, "\n\n"))
	draft, known := s.gate.Draft()
	empty := known && draft == ""
	switch {
	case empty:
		return s.runner.WriteToAgent(prompt)
	case s.o.tool == "claude":
		if err := s.runner.WriteToAgent([]byte{0x13}); err != nil { // Ctrl-S: stash
			return err
		}
		time.Sleep(forceSettle)
		return s.runner.WriteToAgent(prompt)
	case known:
		del := []byte(strings.Repeat("\x7f", utf8.RuneCountInString(draft)))
		if err := s.runner.WriteToAgent(del); err != nil {
			return err
		}
		time.Sleep(forceSettle)
		if err := s.runner.WriteToAgent(prompt); err != nil {
			return err
		}
		time.Sleep(forceSettle)
		s.gate.Restore(draft)
		return s.runner.WriteToAgent([]byte("\x1b[200~" + draft + "\x1b[201~"))
	}
	s.gate.Sent() // what it couldn't follow went in with the message
	return s.runner.WriteToAgent(prompt)
}

// forceSettle lets the agent take one write before the next when pix sets your draft aside.
var forceSettle = 300 * time.Millisecond

// keylog appends a line to the file DESKPIXIE_PIX_KEYLOG names (unset: nothing), to see what
// reaches the typing gate and what it makes of it.
func (s *Session) keylog(format string, args ...any) {
	path := os.Getenv("DESKPIXIE_PIX_KEYLOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

func newSession(o sessionOpts) *Session {
	s := &Session{o: o, status: "idle", afterStop: true, userDesc: o.description, now: time.Now}
	s.runner = &Runner{Launch: o.agent, Stdin: o.stdin, Stdout: o.stdout,
		OnInput: func(b []byte) {
			s.gate.Keys(b, time.Now())
			s.keylog("keys %q -> %s", b, s.gate.State())
			if !onlyReports(b) {
				s.clearQuestion() // you are answering it yourself
			}
		}}
	// Only the PTY reader goroutine touches these.
	var watch, stopped screenWatch
	var shown questionWatch
	var vt *screen
	var notes osc9Watch
	switch o.tool {
	case "codex":
		s.runner.OnOutput = func(b []byte) {
			out := string(b)
			if stopped.saw(out, codexInterrupted) && s.currentStatus() != "idle" {
				s.clearQuestion()
				s.interrupted()
			}
			if watch.saw(out, codexBusy) && s.currentStatus() != "working" {
				s.setStatus("working")
			}
			for _, msg := range notes.messages(out) {
				if asks := codexApproval(msg); asks != "" {
					s.ask(&question{kind: "permission", marker: codexFooter, read: readCodexDialog},
						wire.Msg{Source: "permission", Text: asks})
				}
			}
			vt = s.screenFor(vt)
			vt.Write(b)
			s.noteScreen(vt) // before watch: the question it may send is answered against this screen
			shown.watch(s, out, vt)
		}
	case "claude":
		s.runner.OnOutput = func(b []byte) {
			out := string(b)
			// Working, or blocked: Esc at a permission prompt fires no Stop either.
			if watch.saw(out, claudeInterrupted) {
				s.clearQuestion() // at a prompt too: it was dismissed
				if s.currentStatus() != "idle" {
					s.interrupted()
				}
			}
			vt = s.screenFor(vt)
			vt.Write(b)
			s.noteScreen(vt) // before watch: the question it may send is answered against this screen
			shown.watch(s, out, vt)
		}
	}
	return s
}

// Hook handles the agent's hooks (LocalHandler).
func (s *Session) Hook(event string, p map[string]any) string {
	if id, _ := p["session_id"].(string); id != "" {
		s.setSessionID(id)
	}
	switch event {
	case "PreToolUse":
		if name, _ := p["tool_name"].(string); name == "AskUserQuestion" {
			text, options := menuOf(p["tool_input"])
			s.ask(&question{kind: "menu", options: len(options), marker: claudeMenu, detail: firstRunes(text, 24)},
				wire.Msg{Source: "menu", Text: text, Keys: options})
		}
		return ""
	case "PermissionRequest":
		// The menu asks through its own PreToolUse; anything else is a permission dialog.
		if name, _ := p["tool_name"].(string); name != "AskUserQuestion" {
			input, _ := json.Marshal(p["tool_input"])
			ev := wire.Msg{Source: "permission", ToolName: name, Input: string(input)}
			q := &question{kind: "permission", marker: claudePermission, detail: claudeDetail(name, p["tool_input"]),
				read: func(lines []string) (dialog, bool) { return readDialog(lines, claudePermission) }}
			if !claudeAnswerable(name) || q.detail == "" {
				q, ev.Source = &question{kind: "notice"}, "notice" // answered in its terminal only
			}
			s.ask(q, ev)
		}
		return ""
	case "UserPromptSubmit":
		s.clearQuestion()
		defer func() { s.keylog("UserPromptSubmit -> %s", s.gate.State()) }()
		// A prompt while not working starts a new turn, even if the last one never ended with
		// a Stop (an API error, an interrupt pix didn't see): its start doesn't carry over.
		s.mu.Lock()
		if s.status != "working" {
			s.turnStart, s.turnFromPixie = time.Time{}, false
		}
		s.mu.Unlock()
		s.setStatus("working")
		prompt, _ := p["prompt"].(string)
		pixie := s.fromPixie(prompt)
		if !pixie {
			s.gate.Sent() // your prompt went in (#185)
		}
		s.mu.Lock()
		s.turnFromPixie = s.turnFromPixie || pixie
		first := s.afterStop && prompt != ""
		if first {
			s.afterStop = false
		}
		s.mu.Unlock()
		if !first {
			return ""
		}
		if !pixie {
			s.newWork(prompt)
		}
		return fmt.Sprintf(`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":%q}}`, askDescription)
	case "Stop":
		s.clearQuestion()
		pixie := false
		if s.o.tool == "codex" { // its notify carries the turn's prompts; there is no prompt hook
			if in, _ := p["input-messages"].([]any); len(in) > 0 {
				if first, _ := in[0].(string); first != "" {
					if s.fromPixie(first) {
						pixie = true
					} else {
						s.newWork(first)
					}
				}
			}
		}
		s.endTurn(p, pixie)
		s.gate.Started() // a turn ran, so your line was sent (a fast Codex turn shows no busy marker)
		s.mu.Lock()
		s.afterStop = true
		s.mu.Unlock()
		s.setStatus("idle")
		s.flushSoon()
	case "SessionStart":
		s.clearQuestion()
		// A resumed conversation has no prompt until you type one: describe it from its transcript.
		if src, _ := p["source"].(string); src == "resume" && s.currentDesc() == "" {
			if path, _ := p["transcript_path"].(string); path != "" {
				s.newWork(ResumedDescription(path))
			}
		}
	case "Notification":
		// Only a prompt waiting on you blocks; "waiting for your input" after a minute idle, or
		// a login, leaves it idle (#144). The hook's matcher already leaves those out.
		switch p["notification_type"] {
		case "idle_prompt", "auth_success":
			return ""
		}
		s.setStatus("blocked")
	case "PostToolUse":
		s.clearQuestion()
		// A tool finished, so nothing waits on you any more (#165).
		if s.currentStatus() == "blocked" {
			s.setStatus("working")
		}
		if msgs := s.queue.Drain(); len(msgs) > 0 {
			s.title()
			s.delivered()
			ctx := "Messages from Pixie:\n- " + strings.Join(msgs, "\n- ")
			return fmt.Sprintf(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":%q}}`, ctx)
		}
	}
	return ""
}

// SetDescription is the agent's set_description (LocalHandler).
func (s *Session) SetDescription(text string) error {
	if text = describe.Clean(text); text == "" {
		return errors.New("say in a few plain words what you are working on")
	}
	s.mu.Lock()
	s.agentDesc = text
	s.mu.Unlock()
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: text})
	s.title()
	return nil
}

func (s *Session) command(m wire.Msg) wire.Msg {
	r := s.runCommand(m)
	if r.Err != "" {
		s.o.log.Printf("command %s: %s", m.Command, r.Err)
	} else {
		if m.Command == "answer" {
			s.o.log.Printf("command answer %s with %s", m.Text, m.Source) // a question id and a choice
		} else {
			s.o.log.Printf("command %s", m.Command) // never its text
		}
	}
	return r
}

func (s *Session) runCommand(m wire.Msg) wire.Msg {
	switch m.Command {
	case "prompt":
		text := Sanitize(m.Text)
		s.mu.Lock()
		s.posted = append(s.posted, norm(text))
		if len(s.posted) > 8 {
			s.posted = s.posted[len(s.posted)-8:]
		}
		s.mu.Unlock()
		// Every prompt is typed, same as Codex, never relayed over Claude's inbox socket
		// (#129, extended): Claude Code labels anything arriving that way as coming from a
		// peer session, refuses to treat it as the user's own approval however it's worded,
		// and wraps even a routine coordinator instruction in a generic "another Claude
		// session" notice. The caller labels its own text ("Coordinator says: ") before it
		// reaches here; this package only ever sees the final text to type.
		if err := s.queue.Push(text); err != nil {
			return wire.Msg{Err: err.Error()}
		}
		s.title()
		// Typed at once, or waiting and why (#183).
		if s.flushNow() {
			s.keylog("prompt typed")
			return wire.Msg{Status: "typed"}
		}
		s.flushSoon()
		s.keylog("prompt waiting: status=%s %s", s.currentStatus(), s.gate.State())
		return wire.Msg{Status: "waiting", Reason: s.waitReason()}
	case "flush":
		// "Send it anyway" (#187): type what waits even if the gate thinks you're typing. A
		// working agent gets it with its next tool; a question on screen is never typed into.
		if st := s.currentStatus(); st != "idle" && s.queue.Len() > 0 {
			return wire.Msg{Status: "waiting", Reason: st}
		}
		s.flushMu.Lock()
		msgs := s.queue.Drain()
		if err := s.forceIn(msgs); err != nil {
			s.flushMu.Unlock()
			return wire.Msg{Err: err.Error()}
		}
		s.flushMu.Unlock()
		s.title()
		s.keylog("flush: typed %d", len(msgs))
		if len(msgs) > 0 {
			s.delivered()
		}
		return wire.Msg{Status: "typed"}
	case "keys":
		s.clearQuestion() // an Escape from Pixie dismisses the prompt
		for _, k := range m.Keys {
			var b []byte
			switch k {
			case "Escape":
				b = []byte{0x1b}
			case "Enter":
				b = []byte{'\r'}
			default:
				return wire.Msg{Err: "unknown key " + k}
			}
			if err := s.runner.WriteToAgent(b); err != nil {
				return wire.Msg{Err: err.Error()}
			}
		}
		return wire.Msg{}
	case "close":
		go s.runner.Hangup()
		return wire.Msg{}
	case "answer":
		return s.answer(m.Text, m.Source)
	}
	return wire.Msg{Err: "unknown command " + m.Command}
}

// flushNow types the queued messages if the agent is idle and you aren't typing; true if it did.
func (s *Session) flushNow() bool {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	if s.queue.Len() == 0 || s.currentStatus() != "idle" || !s.gate.Clear(time.Now()) {
		return false
	}
	for _, m := range s.queue.Drain() {
		if s.runner.WriteToAgent(Paste(m)) != nil {
			return false
		}
	}
	s.title()
	return true
}

// waitReason is why a message isn't typed now: the agent is "working" (it gets it with its
// next tool, or when it stops), "blocked" on a question, or you're "typing" in it.
func (s *Session) waitReason() string {
	switch st := s.currentStatus(); st {
	case "working", "blocked":
		return st
	}
	return "typing"
}

// delivered tells DeskPixie the messages that waited have reached the agent (#183).
func (s *Session) delivered() {
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "delivered"})
}

// flushSoon types queued messages once the agent is idle and you aren't typing, checking
// every 500 ms while messages wait. One flusher at a time.
func (s *Session) flushSoon() {
	s.mu.Lock()
	if s.flushing {
		s.mu.Unlock()
		return
	}
	s.flushing = true
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); s.flushing = false; s.mu.Unlock() }()
		for s.queue.Len() > 0 {
			if s.flushNow() {
				s.delivered()
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

// interrupted: Claude shows an interrupt (Esc) and fires no Stop: the turn is over, unannounced.
func (s *Session) interrupted() {
	s.mu.Lock()
	s.afterStop = true
	s.turnStart, s.turnFromPixie = time.Time{}, false
	s.mu.Unlock()
	s.setStatus("idle")
	s.flushSoon()
}

// MaxTurnReplyBytes caps the reply a turn event carries (#114).
const MaxTurnReplyBytes = 4 << 10

// endTurn tells DeskPixie a turn finished (#114): its final reply (Claude's Stop has
// last_assistant_message, Codex's notify last-assistant-message), how long it ran and whether
// Pixie typed its prompt. The app decides whether to announce it.
func (s *Session) endTurn(p map[string]any, pixie bool) {
	reply, _ := p["last_assistant_message"].(string)
	if reply == "" {
		reply, _ = p["last-assistant-message"].(string)
	}
	s.mu.Lock()
	var d time.Duration
	if !s.turnStart.IsZero() {
		d = s.now().Sub(s.turnStart)
	}
	pixie = pixie || s.turnFromPixie
	s.turnStart, s.turnFromPixie = time.Time{}, false
	s.mu.Unlock()
	source := "you"
	if pixie {
		source = "pixie"
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: capUTF8(reply, MaxTurnReplyBytes),
		DurationMs: max(d.Milliseconds(), 0), Source: source})
}

// capUTF8 cuts s to at most n bytes without splitting a character.
func capUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *Session) setStatus(st string) {
	if st == "working" {
		s.gate.Started()
	}
	s.mu.Lock()
	if st == "working" && s.turnStart.IsZero() {
		s.turnStart = s.now()
	}
	changed := s.status != st
	s.status = st
	s.mu.Unlock()
	if changed {
		s.o.log.Printf("status %s", st)
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "status", Status: st})
}

func (s *Session) currentStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Session) setSessionID(id string) {
	s.mu.Lock()
	changed := s.sessionID != id
	s.sessionID = id
	s.mu.Unlock()
	if changed {
		s.o.log.Printf("session %s", id)
		s.client.SetSessionID(id)
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: id})
	}
}

// currentDesc is the agent's or the automatic description, "" when there is neither.
func (s *Session) currentDesc() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentDesc + s.autoDesc
}

// description is what the tab shows: yours, else the agent's, else the first prompt.
func (s *Session) description() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range []string{s.userDesc, s.agentDesc, s.autoDesc} {
		if d = describe.Clean(d); d != "" {
			return d
		}
	}
	return ""
}

// title sets the terminal tab title (OSC 2): titleText.
func (s *Session) title() {
	if t := s.titleText(); t != "" {
		fmt.Fprintf(s.o.stdout, "\x1b]2;%s\x07", Sanitize(t))
	}
}

// titleText is the description, with waiting messages first, and first of all a note when
// this pix is older than the app's (#189).
func (s *Session) titleText() string {
	t := s.description()
	if n := s.queue.Len(); n > 0 {
		t = fmt.Sprintf("📨 %d · %s", n, t)
	}
	s.mu.Lock()
	outdated := s.outdated
	s.mu.Unlock()
	if outdated {
		t = "⟳ restart pix · " + t
	}
	return t
}

// resend is the state DeskPixie needs after (re)registering.
func (s *Session) resend() []wire.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []wire.Msg{{Kind: wire.KindEvent, Event: "status", Status: s.status}}
	if s.sessionID != "" {
		out = append(out, wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: s.sessionID})
	}
	if s.autoDesc != "" {
		out = append(out, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: s.autoDesc})
	}
	if s.agentDesc != "" {
		out = append(out, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: s.agentDesc})
	}
	return out
}

// start starts the agent, then connects to DeskPixie in the background.
func (s *Session) start(ctx context.Context) error {
	cwd, _ := os.Getwd()
	s.client = &Client{
		Sock: s.o.daemonSock,
		Hello: wire.Msg{Kind: wire.KindRegister, Tool: s.o.tool, Cwd: cwd, GitRoot: gitRoot(cwd),
			Where: s.o.where, App: s.o.app, Description: s.o.description, Build: wire.SelfBuild()},
		OnCommand: s.command,
		OnRegistered: func(reply wire.Msg) {
			s.o.log.Printf("registered with DeskPixie as %s (outdated %v)", reply.Address, reply.Outdated)
			s.mu.Lock()
			changed := s.outdated != reply.Outdated
			s.outdated = reply.Outdated
			s.mu.Unlock()
			if changed {
				s.title()
			}
		},
		OnRefused: func(r string) {
			s.o.log.Printf("DeskPixie refused: %s", r)
			s.mu.Lock()
			s.userDesc = "pix: " + r
			s.mu.Unlock()
			s.title()
		},
		Resend:    s.resend,
		OnDropped: func() { s.o.log.Printf("lost DeskPixie; reconnecting") },
	}
	s.o.log.Printf("start in %s (%s)", cwd, strings.TrimSpace(s.o.where+" "+s.o.app))
	if err := s.runner.Start(); err != nil {
		s.o.log.Printf("couldn't start the agent: %v", err)
		return err
	}
	s.client.Hello.Pid = s.runner.Pid()
	s.client.Hello.ProcStart, _ = ProcStart(s.runner.Pid())
	if s.o.description != "" {
		s.title()
	}
	go s.client.Run(ctx)
	return nil
}

// finish waits for the agent, tells DeskPixie it exited, and returns its exit code.
func (s *Session) finish() int {
	code := s.runner.Wait()
	s.o.log.Printf("the agent exited (%d)", code)
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "exited", ExitCode: code})
	s.client.Send(wire.Msg{Kind: wire.KindBye})
	return code
}

// Run is `pix [--description "…"] <tool> [args…]`; it returns the agent's exit code.
func Run(args []string, env func(string) string, home string) int {
	desc := ""
	for len(args) >= 2 && args[0] == "--description" {
		desc, args = args[1], args[2:]
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: pix [--description \"…\"] claude|codex [args…]")
		return 2
	}
	tool, rest := args[0], args[1:]
	if Nested(env) || Passthrough(tool, rest, term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd()))) {
		path, err := exec.LookPath(tool)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pix:", err)
			return 127
		}
		err = syscall.Exec(path, append([]string{tool}, rest...), os.Environ())
		fmt.Fprintln(os.Stderr, "pix:", err)
		return 126
	}
	dir, err := os.MkdirTemp("", "pix-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pix:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pix:", err)
		return 1
	}
	localSock := filepath.Join(dir, "pix.sock")
	launch, err := PlanLaunch(tool, rest, self, localSock, dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	appData := AppData(home, env)
	s := newSession(sessionOpts{tool: tool, description: desc, home: home, stdin: os.Stdin, stdout: os.Stdout,
		agent: launch, where: Where(env), app: AppID(env), daemonSock: DaemonSocket(home, env),
		log: openLog(LogFile(appData), logMax(appData), tool)})
	stop, err := ServeLocal(localSock, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pix:", err)
		return 1
	}
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sig)
	if err := s.start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pix:", err)
		return 127
	}
	go func() {
		if _, ok := <-sig; ok {
			s.o.log.Printf("hung up")
			s.runner.Hangup() // tab closed or the terminal app quit: end the agent too
		}
	}()
	return s.finish()
}

// ProcStart is pid's process start time, for telling a live process from a reused pid.
func ProcStart(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRoot is the nearest folder from dir up that holds .git, or "".
func gitRoot(dir string) string {
	for d := dir; d != ""; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return ""
}

// DaemonSocket is where DeskPixie listens: DESKPIXIE_PIX_SOCK when set (tests, smoke), else
// wire.SocketPath under its data folder (AppData).
func DaemonSocket(home string, env func(string) string) string {
	if p := env("DESKPIXIE_PIX_SOCK"); p != "" {
		return p
	}
	return wire.SocketPath(AppData(home, env), os.Getuid(), os.TempDir())
}
