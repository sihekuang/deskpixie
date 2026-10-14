package tmux

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// FakeRunner is a scriptable Runner for tests: every call is recorded, and Handle (nil: a
// FakeServer's) answers it.
type FakeRunner struct {
	Handle func(args []string) (Output, error)

	mu    sync.Mutex
	calls [][]string
}

var _ Runner = (*FakeRunner)(nil)

func (f *FakeRunner) Run(ctx context.Context, args ...string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	handle := f.Handle
	f.mu.Unlock()
	return handle(slices.Clone(args))
}

// Calls is every call so far, oldest first.
func (f *FakeRunner) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = slices.Clone(c)
	}
	return out
}

// CallsOf is the calls whose tmux command (the word after `-L <socket>`) is cmd.
func (f *FakeRunner) CallsOf(cmd string) [][]string {
	var out [][]string
	for _, c := range f.Calls() {
		if len(c) > 2 && c[0] == "-L" && c[2] == cmd {
			out = append(out, c)
		}
	}
	return out
}

// FakePane is one pane of a FakeServer (one per window).
type FakePane struct {
	ID      string
	Window  string
	Cwd     string
	Dead    bool
	Screen  string
	Title   string
	Options map[string]string
	Env     []string
	Argv    []string
	Keys    []string // send-keys, one entry per key or literal text ("-l:" prefix)
}

// FakeServer is a small in-memory tmux server speaking the commands Host uses, for
// FakeRunner.Handle. Pane ids are never reused. Tests change it through its methods.
type FakeServer struct {
	Version string // `tmux -V` output; "" = "tmux 3.4"

	mu       sync.Mutex
	running  bool
	session  string
	panes    []*FakePane
	next     int
	selected string
	options  map[string]string
	// OnStart, if set, sets a new pane's first screen from its argv (e.g. a ready prompt).
	OnStart func(p *FakePane)
	// OnKeys, if set, is told about keys sent to a live pane (after the server's lock is
	// released, so it may change the server): a fake agent answering them.
	OnKeys func(s *FakeServer, pane string, keys []string)
}

func NewFakeServer() *FakeServer { return &FakeServer{options: map[string]string{}} }

// Runner is a FakeRunner backed by s.
func (s *FakeServer) Runner() *FakeRunner { return &FakeRunner{Handle: s.Handle} }

// Running: the server and its session exist.
func (s *FakeServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Stop ends the server (every pane gone), as when its last session closes.
func (s *FakeServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running, s.panes = false, nil
}

// Pane is a copy of the pane with id, or false.
func (s *FakeServer) Pane(id string) (FakePane, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.panes {
		if p.ID == id {
			c := *p
			c.Options = mapsClone(p.Options)
			c.Keys = slices.Clone(p.Keys)
			return c, true
		}
	}
	return FakePane{}, false
}

// PaneNamed is the newest pane of window name, or false.
func (s *FakeServer) PaneNamed(name string) (FakePane, bool) {
	s.mu.Lock()
	id := ""
	for _, p := range s.panes {
		if p.Window == name {
			id = p.ID
		}
	}
	s.mu.Unlock()
	if id == "" {
		return FakePane{}, false
	}
	return s.Pane(id)
}

// Windows is every window name, oldest first.
func (s *FakeServer) Windows() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.panes {
		out = append(out, p.Window)
	}
	return out
}

// Option is a global option the host set.
func (s *FakeServer) Option(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.options[name]
}

// Selected is the pane last brought to the front (select-window).
func (s *FakeServer) Selected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}

// SetScreen sets what pane id shows.
func (s *FakeServer) SetScreen(id, screen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.panes {
		if p.ID == id {
			p.Screen = screen
		}
	}
}

// Kill marks pane id's process exited (remain-on-exit keeps the pane).
func (s *FakeServer) Kill(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.panes {
		if p.ID == id {
			p.Dead = true
		}
	}
}

// Vanish removes pane id without a trace (closed from outside).
func (s *FakeServer) Vanish(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panes = slices.DeleteFunc(s.panes, func(p *FakePane) bool { return p.ID == id })
}

func mapsClone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func ok(stdout string) (Output, error) { return Output{Stdout: stdout}, nil }
func fail(stderr string) (Output, error) {
	return Output{Code: 1, Stderr: stderr + "\n"}, nil
}

// Handle answers one call: `-V`, or `-L <socket> <command> …` with `;` between commands.
func (s *FakeServer) Handle(args []string) (Output, error) {
	if len(args) == 1 && args[0] == "-V" {
		v := s.Version
		if v == "" {
			v = "tmux 3.4"
		}
		return ok(v + "\n")
	}
	if len(args) < 3 || args[0] != "-L" {
		return fail("usage")
	}
	var stdout strings.Builder
	cmd := []string{}
	run := func() (Output, error) {
		out, err := s.one(cmd)
		stdout.WriteString(out.Stdout)
		return out, err
	}
	for _, a := range args[2:] {
		if a == ";" {
			if out, err := run(); err != nil || out.Code != 0 {
				return out, err
			}
			cmd = []string{}
			continue
		}
		cmd = append(cmd, a)
	}
	out, err := run()
	if err != nil || out.Code != 0 {
		return out, err
	}
	return ok(stdout.String())
}

// flags parses a command's flags: those in valued take a value; "--" or the first non-flag
// ends them.
func flags(args []string, valued string) (map[string][]string, []string) {
	f := map[string][]string{}
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || len(a) < 2 {
			break
		}
		for j := 1; j < len(a); j++ {
			c := string(a[j])
			if strings.Contains(valued, c) {
				if j+1 < len(a) {
					f[c] = append(f[c], a[j+1:])
				} else if i+1 < len(args) {
					i++
					f[c] = append(f[c], args[i])
				}
				break
			}
			f[c] = append(f[c], "")
		}
	}
	return f, args[i:]
}

func (s *FakeServer) pane(target string) *FakePane {
	for _, p := range s.panes {
		if p.ID == target {
			return p
		}
	}
	return nil
}

func (s *FakeServer) one(cmd []string) (Output, error) {
	var after func()
	defer func() {
		if after != nil {
			after()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(cmd) == 0 {
		return fail("no command")
	}
	name, args := cmd[0], cmd[1:]
	noServer := "no server running on /private/tmp/tmux-501/" + s.session
	switch name {
	case "has-session":
		f, _ := flags(args, "t")
		if !s.running || len(f["t"]) == 0 || strings.TrimPrefix(f["t"][0], "=") != s.session {
			if !s.running {
				return fail(noServer)
			}
			return fail("can't find session: " + strings.TrimPrefix(f["t"][0], "="))
		}
		return ok("")
	case "new-session":
		f, _ := flags(args, "scnxy")
		if s.running {
			return fail("duplicate session: " + f["s"][0])
		}
		s.running, s.session = true, f["s"][0]
		s.panes = append(s.panes, s.newPane(f["n"][0], first(f["c"]), nil, nil))
		return ok("")
	case "set-option":
		f, rest := flags(args, "t")
		if len(rest) < 2 {
			return fail("bad set-option")
		}
		if t := first(f["t"]); t != "" {
			p := s.pane(t)
			if p == nil {
				return fail("can't find pane: " + t)
			}
			p.Options[rest[0]] = rest[1]
			return ok("")
		}
		s.options[rest[0]] = rest[1]
		return ok("")
	case "new-window":
		if !s.running {
			return fail(noServer)
		}
		f, argv := flags(args, "tncFe")
		p := s.newPane(first(f["n"]), first(f["c"]), f["e"], argv)
		s.panes = append(s.panes, p)
		if s.OnStart != nil {
			s.OnStart(p)
		}
		if _, print := f["P"]; print {
			return ok(p.ID + "\n")
		}
		return ok("")
	case "list-panes":
		if !s.running {
			return fail(noServer)
		}
		f, _ := flags(args, "tF")
		format := first(f["F"])
		var b strings.Builder
		for _, p := range s.panes {
			dead := "0"
			if p.Dead {
				dead = "1"
			}
			line := strings.NewReplacer("#{window_name}", p.Window, "#{pane_id}", p.ID, "#{pane_current_path}", p.Cwd,
				"#{pane_dead}", dead, "#{@deskpixie_kind}", p.Options["@deskpixie_kind"]).Replace(format)
			b.WriteString(line + "\n")
		}
		return ok(b.String())
	case "display-message":
		if !s.running {
			return fail(noServer)
		}
		f, rest := flags(args, "tF")
		p := s.pane(first(f["t"]))
		if p == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		dead := "0"
		if p.Dead {
			dead = "1"
		}
		return ok(strings.ReplaceAll(strings.Join(rest, " "), "#{pane_dead}", dead) + "\n")
	case "capture-pane":
		if !s.running {
			return fail(noServer)
		}
		f, _ := flags(args, "tSE")
		p := s.pane(first(f["t"]))
		if p == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		return ok(p.Screen + "\n")
	case "send-keys":
		if !s.running {
			return fail(noServer)
		}
		f, keys := flags(args, "tN")
		p := s.pane(first(f["t"]))
		if p == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		if p.Dead {
			return ok("") // tmux accepts keys for a dead pane and drops them
		}
		sent := keys
		if _, literal := f["l"]; literal {
			sent = []string{"-l:" + strings.Join(keys, " ")}
		}
		p.Keys = append(p.Keys, sent...)
		if hook, id := s.OnKeys, p.ID; hook != nil {
			after = func() { hook(s, id, sent) }
		}
		return ok("")
	case "kill-pane":
		if !s.running {
			return fail(noServer)
		}
		f, _ := flags(args, "t")
		if s.pane(first(f["t"])) == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		s.panes = slices.DeleteFunc(s.panes, func(p *FakePane) bool { return p.ID == first(f["t"]) })
		if len(s.panes) == 0 {
			s.running = false // the last window closed: the session and server end
		}
		return ok("")
	case "select-window":
		if !s.running {
			return fail(noServer)
		}
		f, _ := flags(args, "t")
		if s.pane(first(f["t"])) == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		s.selected = first(f["t"])
		return ok("")
	case "select-pane":
		if !s.running {
			return fail(noServer)
		}
		f, _ := flags(args, "tT")
		p := s.pane(first(f["t"]))
		if p == nil {
			return fail("can't find pane: " + first(f["t"]))
		}
		p.Title = first(f["T"])
		return ok("")
	}
	return fail("unknown command: " + name)
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (s *FakeServer) newPane(window, cwd string, env, argv []string) *FakePane {
	s.next++
	return &FakePane{ID: "%" + strconv.Itoa(s.next-1), Window: window, Cwd: cwd, Env: env, Argv: argv, Options: map[string]string{}}
}

// String is a one-line summary (for test failures).
func (p FakePane) String() string {
	return fmt.Sprintf("%s %s dead=%v argv=%v", p.ID, p.Window, p.Dead, p.Argv)
}
