package pix

import (
	"context"
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

	"github.com/sihekuang/deskpixie/core/pix/wire"
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

// screenWatch finds a marker in the agent's output, even split across reads.
type screenWatch struct{ tail string }

// saw adds chunk and reports whether marker is now on screen (once per appearance).
func (w *screenWatch) saw(chunk, marker string) bool {
	w.tail += ansi.ReplaceAllString(chunk, "")
	if len(w.tail) > 1024 {
		w.tail = w.tail[len(w.tail)-1024:]
	}
	if strings.Contains(w.tail, marker) {
		w.tail = ""
		return true
	}
	return false
}

type sessionOpts struct {
	tool, description, daemonSock, home, where string
	stdin, stdout                              *os.File
	agent                                      Launch
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
}

func newSession(o sessionOpts) *Session {
	s := &Session{o: o, status: "idle", afterStop: true, userDesc: o.description}
	s.runner = &Runner{Launch: o.agent, Stdin: o.stdin, Stdout: o.stdout,
		OnInput: func(b []byte) { s.gate.Keys(b, time.Now()) }}
	var watch screenWatch // only the PTY reader goroutine touches it
	switch o.tool {
	case "codex":
		s.runner.OnOutput = func(b []byte) {
			if watch.saw(string(b), codexBusy) && s.currentStatus() != "working" {
				s.setStatus("working")
			}
		}
	case "claude":
		s.runner.OnOutput = func(b []byte) {
			if watch.saw(string(b), claudeInterrupted) && s.currentStatus() == "working" {
				s.mu.Lock()
				s.afterStop = true
				s.mu.Unlock()
				s.setStatus("idle")
				s.flushSoon()
			}
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
	case "UserPromptSubmit":
		s.setStatus("working")
		prompt, _ := p["prompt"].(string)
		s.mu.Lock()
		first := s.afterStop && prompt != ""
		if first {
			s.afterStop = false
			s.autoDesc = prompt
		}
		s.mu.Unlock()
		if first {
			s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: prompt})
			s.title()
		}
	case "Stop":
		s.mu.Lock()
		s.afterStop = true
		s.mu.Unlock()
		s.setStatus("idle")
		s.flushSoon()
	case "Notification":
		s.setStatus("blocked")
	case "PostToolUse":
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

// description is what the tab shows: yours, else the agent's, else the first prompt.
func (s *Session) description() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range []string{s.userDesc, s.agentDesc, s.autoDesc} {
		if d = strings.Join(strings.Fields(d), " "); d != "" {
			if r := []rune(d); len(r) > 60 {
				d = string(r[:60])
			}
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
			Where: s.o.where, Description: s.o.description},
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
	if Nested(env) {
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
		agent: launch, where: Where(env), daemonSock: DaemonSocket(home, env)})
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
