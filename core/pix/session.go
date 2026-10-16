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
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/sihekuang/deskpixie/core/pix/describe"
	"github.com/sihekuang/deskpixie/core/pix/wire"
	"golang.org/x/term"
)

// Screen markers. Codex shows codexBusy while it works (the marker DeskPixie's workers use).
// Claude fires no Stop hook when a turn is interrupted (Esc, yours or Pixie's); it shows
// claudeInterrupted instead.
const (
	codexBusy         = "esc to interrupt"
	claudeInterrupted = "What should Claude do instead?"
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
	agentDesc string // set_description
	autoDesc  string // first prompt of the current work
	flushing  bool
	posted    []string  // Pixie's messages not yet seen as prompts (normalized)
	q         *question // the prompt waiting on you that Pixie may answer (#105)
	qseq      int
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

func newSession(o sessionOpts) *Session {
	s := &Session{o: o, status: "idle", afterStop: true, userDesc: o.description}
	s.runner = &Runner{Launch: o.agent, Stdin: o.stdin, Stdout: o.stdout,
		OnInput: func(b []byte) {
			s.gate.Keys(b, time.Now())
			if !onlyFocusReports(b) {
				s.clearQuestion() // you are answering it yourself
			}
		}}
	// Only the PTY reader goroutine touches these.
	var watch screenWatch
	var shown questionWatch
	var notes osc9Watch
	switch o.tool {
	case "codex":
		s.runner.OnOutput = func(b []byte) {
			out := string(b)
			if watch.saw(out, codexBusy) && s.currentStatus() != "working" {
				s.setStatus("working")
			}
			for _, msg := range notes.messages(out) {
				if asks := codexApproval(msg); asks != "" {
					s.ask(&question{kind: "permission", seen: true}, wire.Msg{Source: "permission", Text: asks})
				}
			}
		}
	case "claude":
		s.runner.OnOutput = func(b []byte) {
			out := string(b)
			if watch.saw(out, claudeInterrupted) {
				s.clearQuestion() // at a prompt too (status blocked there): it was dismissed
				if s.currentStatus() == "working" {
					s.mu.Lock()
					s.afterStop = true
					s.mu.Unlock()
					s.setStatus("idle")
					s.flushSoon()
				}
			}
			shown.watch(s, out)
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
			q := &question{kind: "permission", marker: claudePermission, detail: claudeDetail(name, p["tool_input"])}
			if !claudeAnswerable(name) || q.detail == "" {
				q, ev.Source = &question{kind: "notice"}, "notice" // answered in its terminal only
			}
			s.ask(q, ev)
		}
		return ""
	case "UserPromptSubmit":
		s.clearQuestion()
		s.setStatus("working")
		prompt, _ := p["prompt"].(string)
		pixie := s.fromPixie(prompt)
		s.mu.Lock()
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
		if s.o.tool == "codex" { // its notify carries the turn's prompts; there is no prompt hook
			if in, _ := p["input-messages"].([]any); len(in) > 0 {
				if first, _ := in[0].(string); first != "" && !s.fromPixie(first) {
					s.newWork(first)
				}
			}
		}
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
		s.setStatus("blocked")
	case "PostToolUse":
		s.clearQuestion()
		if msgs := s.queue.Drain(); len(msgs) > 0 {
			s.title()
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
	switch m.Command {
	case "prompt":
		text := Sanitize(m.Text)
		s.mu.Lock()
		s.posted = append(s.posted, norm(text))
		if len(s.posted) > 8 {
			s.posted = s.posted[len(s.posted)-8:]
		}
		s.mu.Unlock()
		if s.o.tool == "claude" {
			if rec, err := FindClaudeRecord(s.o.home, s.runner.Pid()); err == nil {
				if err := PostClaude(rec, text); err == nil {
					return wire.Msg{}
				}
			}
		}
		if err := s.queue.Push(text); err != nil {
			return wire.Msg{Err: err.Error()}
		}
		s.title()
		s.flushSoon()
		return wire.Msg{}
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
			if s.currentStatus() == "idle" && s.gate.Clear(time.Now()) {
				for _, m := range s.queue.Drain() {
					if s.runner.WriteToAgent(Paste(m)) != nil {
						return
					}
				}
				s.title()
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (s *Session) setStatus(st string) {
	if st == "working" {
		s.gate.Started()
	}
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
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

// title sets the terminal tab title (OSC 2): the description, with waiting messages first.
func (s *Session) title() {
	t := s.description()
	if n := s.queue.Len(); n > 0 {
		t = fmt.Sprintf("📨 %d · %s", n, t)
	}
	if t != "" {
		fmt.Fprintf(s.o.stdout, "\x1b]2;%s\x07", Sanitize(t))
	}
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
			Where: s.o.where, App: s.o.app, Description: s.o.description},
		OnCommand: s.command,
		OnRefused: func(r string) {
			s.mu.Lock()
			s.userDesc = "pix: " + r
			s.mu.Unlock()
			s.title()
		},
		Resend: s.resend,
	}
	if err := s.runner.Start(); err != nil {
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
	s := newSession(sessionOpts{tool: tool, description: desc, home: home, stdin: os.Stdin, stdout: os.Stdout,
		agent: launch, where: Where(env), app: AppID(env), daemonSock: DaemonSocket(home, env)})
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
			s.runner.Hangup() // tab closed or the terminal app quit: end the agent too
		}
	}()
	return s.finish()
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
// wire.SocketPath under Application Support.
func DaemonSocket(home string, env func(string) string) string {
	if p := env("DESKPIXIE_PIX_SOCK"); p != "" {
		return p
	}
	appData := filepath.Join(home, "Library", "Application Support", "com.sihekuang.deskpixie")
	return wire.SocketPath(appData, os.Getuid(), os.TempDir())
}
