package agentapp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Go tests for the composition root (Rust's build/attach_voice/on_menu/shutdown are only
// exercised by the app there): build, the end-to-end flow over the fakes, the tray, the menu,
// the echo gate on the voice link, and the shutdown sequence.

// stoppableHost is the fake session host plus the herdr Host's Shutdown.
type stoppableHost struct {
	*agenttest.FakeSessionHost
	shut atomic.Int32
}

func (h *stoppableHost) Shutdown() { h.shut.Add(1) }

// recorder records the app hooks.
type recorder struct {
	mu                  sync.Mutex
	celebrated, flashed int
	refreshed           int
	notes               [][2]string
	chats               []any
}

func (r *recorder) hooks() app.AgentHooks {
	return app.AgentHooks{
		Celebrate:   func() { r.mu.Lock(); r.celebrated++; r.mu.Unlock() },
		FlashError:  func() { r.mu.Lock(); r.flashed++; r.mu.Unlock() },
		RefreshTray: func() { r.mu.Lock(); r.refreshed++; r.mu.Unlock() },
		Notify: func(title, body string) {
			r.mu.Lock()
			r.notes = append(r.notes, [2]string{title, body})
			r.mu.Unlock()
		},
		Chat: func(line any) { r.mu.Lock(); r.chats = append(r.chats, line); r.mu.Unlock() },
	}
}

func (r *recorder) chatLines() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.chats)
}

func (r *recorder) counts() (celebrated, flashed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.celebrated, r.flashed
}

// fakeVoice is the voice loop as the agent sees it.
type fakeVoice struct {
	mu       sync.Mutex
	injected []string
	holds    []bool
	opens    int
	signals  chan voice.VoiceSignal
}

// Open counts the call and answers yes; the test sends Opened itself.
func (v *fakeVoice) Open() bool {
	v.mu.Lock()
	v.opens++
	v.mu.Unlock()
	return true
}

func (v *fakeVoice) Opens() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.opens
}

func newFakeVoice() *fakeVoice { return &fakeVoice{signals: make(chan voice.VoiceSignal, 16)} }

func (v *fakeVoice) Inject(n voice.Notice) {
	v.mu.Lock()
	v.injected = append(v.injected, n.Text)
	v.mu.Unlock()
}

func (v *fakeVoice) HoldOpen(on bool) {
	v.mu.Lock()
	v.holds = append(v.holds, on)
	v.mu.Unlock()
}

func (v *fakeVoice) Subscribe() <-chan voice.VoiceSignal { return v.signals }

func (v *fakeVoice) Injected() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.injected)
}

type fixture struct {
	agent    *Agent
	host     *stoppableHost
	launcher *agenttest.FakePlanner
	rec      *recorder
	data     string
	home     string
}

func newFixture(t *testing.T, change func(*Config)) *fixture {
	t.Helper()
	f := &fixture{
		host:     &stoppableHost{FakeSessionHost: agenttest.NewFakeSessionHost()},
		launcher: &agenttest.FakePlanner{},
		rec:      &recorder{},
		data:     t.TempDir(),
		home:     t.TempDir(),
	}
	cfg := Config{
		Settings:   settings.Default().Agent,
		AppSupport: f.data,
		Home:       f.home,
		Hooks:      f.rec.hooks(),
		Runner:     &agenttest.FakeRunner{},
		Environ:    []string{"PATH=/usr/bin:/bin"},
		Hosts: map[string]HostFactory{"herdr": func(HostDeps) (agent.SessionHost, error) {
			return f.host, nil
		}},
		Terminals: map[string]TerminalFactory{"warp": func(agent.CommandRunner, string) agent.TerminalPlanner {
			return f.launcher
		}},
		IDs:       &agenttest.SeqIds{},
		Listen:    func(uint16) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		PixSocket: shortSocket(t),
	}
	if change != nil {
		change(&cfg)
	}
	a := Build(cfg)
	if a == nil {
		t.Fatal("Build returned nil")
	}
	f.agent = a
	t.Cleanup(a.Shutdown)
	return f
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fixture) tool(t *testing.T, name string) voice.Tool {
	t.Helper()
	for _, tool := range f.agent.Tools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("no %s tool", name)
	return nil
}

func (f *fixture) delegate(t *testing.T, task string) agent.JobID {
	t.Helper()
	out := f.tool(t, "delegate").Invoke(context.Background(), json.RawMessage(`{"task":"`+task+`"}`))
	var r struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out.Output, &r); err != nil || r.JobID == "" {
		t.Fatalf("delegate: %s", out.Output)
	}
	return agent.JobID(r.JobID)
}

// coordinatorToken is the bearer the coordinator was started with.
func (f *fixture) coordinatorToken(t *testing.T) string {
	t.Helper()
	for _, s := range f.host.Starts() {
		for _, e := range s.Env {
			if e.Key == "PIXIE_AGENT_TOKEN" {
				return e.Value
			}
		}
	}
	t.Fatal("the coordinator has no token")
	return ""
}

func (f *fixture) post(t *testing.T, path, token string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+f.agent.Addr()+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text)
}

func openVoice(t *testing.T, f *fixture) *fakeVoice {
	v := newFakeVoice()
	f.agent.AttachVoice(v)
	v.signals <- voice.VoiceSignal{Kind: voice.SigOpened}
	waitFor(t, "the link to open", f.agent.link.IsOpen)
	return v
}

// The whole flow over the fakes and the real hook server: delegate → the coordinator is started
// and prompted → its Stop hook carries the reply → Pixie is injected with it, Pixie celebrates.
func TestEndToEndDelegateToStopHookToVoiceInject(t *testing.T) {
	f := newFixture(t, nil)
	v := openVoice(t, f)
	job := f.delegate(t, "count the open issues")
	waitFor(t, "the coordinator prompt", func() bool {
		for _, p := range f.host.Prompts() {
			if p.Agent == agent.Coordinator && strings.Contains(p.Text, "count the open issues") && strings.Contains(p.Text, "[ref "+string(job)+"]") {
				return true
			}
		}
		return false
	})
	start := f.host.Starts()[0]
	if start.Cwd != NewPaths(f.data).Workspace || !slices.Contains(start.Args, "--settings") {
		t.Fatalf("%+v", start)
	}
	status, _ := f.post(t, "/hooks/stop", f.coordinatorToken(t), map[string]any{
		"hook_event_name":        "Stop",
		"last_assistant_message": `Counted. <spoken ref="` + string(job) + `" status="done">There are twelve open issues.</spoken>`,
	})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	waitFor(t, "the reply injected", func() bool {
		return slices.Contains(v.Injected(), "Coordinator reply: There are twelve open issues.")
	})
	waitFor(t, "Pixie celebrates", func() bool { c, _ := f.rec.counts(); return c == 1 })
	waitFor(t, "the job to leave the tray", func() bool { return len(f.agent.Tray().Jobs) == 0 })
}

// #221: a message typed in the text box reaches the coordinator, and its spoken answer comes
// back to the box, not spoken or notified.
func TestEndToEndATypedMessageAnswersInTheTextBox(t *testing.T) {
	f := newFixture(t, nil)
	v := openVoice(t, f)
	id, err := f.agent.ChatSend("count the open issues")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the coordinator prompt", func() bool {
		for _, p := range f.host.Prompts() {
			if strings.Contains(p.Text, "count the open issues") && strings.Contains(p.Text, "[ref "+id+"]") {
				return true
			}
		}
		return false
	})
	f.post(t, "/hooks/stop", f.coordinatorToken(t), map[string]any{
		"hook_event_name":        "Stop",
		"last_assistant_message": `Counted. <spoken ref="` + id + `" status="done">There are twelve open issues.</spoken>`,
	})
	waitFor(t, "the answer in the box", func() bool {
		for _, l := range f.rec.chatLines() {
			if c, ok := l.(agent.ChatLine); ok && reflect.DeepEqual(c, agent.ChatLine{Job: agent.JobID(id), Status: "done", Text: "There are twelve open issues."}) {
				return true
			}
		}
		return false
	})
	if slices.Contains(v.Injected(), "Coordinator reply: There are twelve open issues.") || len(f.rec.notesOrNil()) != 0 {
		t.Fatal("a typed answer was spoken or notified")
	}
}

func TestAFailedJobFlashesTheErrorState(t *testing.T) {
	f := newFixture(t, nil)
	openVoice(t, f)
	job := f.delegate(t, "deploy it")
	waitFor(t, "the prompt", func() bool { return len(f.host.Prompts()) > 0 })
	f.post(t, "/hooks/stop", f.coordinatorToken(t), map[string]any{
		"last_assistant_message": `<spoken ref="` + string(job) + `" status="failed">I could not.</spoken>`,
	})
	waitFor(t, "Pixie flashes the error", func() bool { _, e := f.rec.counts(); return e == 1 })
}

func TestTheTrayShowsOpenJobsAndTheMenuActsOnThem(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Hosts["herdr"] = func(HostDeps) (agent.SessionHost, error) {
			f := agenttest.NewFakeSessionHost().ManualTurns()
			return f, nil
		}
	})
	job := f.delegate(t, "a task")
	tray := f.agent.Tray()
	if !tray.Enabled || len(tray.Jobs) != 1 || tray.Jobs[0].ID != string(job) || tray.Jobs[0].Task != "a task" {
		t.Fatalf("%+v", tray)
	}
	if !f.agent.Menu("agent:cancel:" + string(job)) {
		t.Fatal("cancel not handled")
	}
	waitFor(t, "the job cancelled", func() bool { return len(f.agent.Tray().Jobs) == 0 })
	if !f.agent.Menu("agent:restart") {
		t.Fatal("restart not handled")
	}
	if f.agent.Menu("agent:nope") || f.agent.Menu("talk") {
		t.Fatal("not the agent's ids")
	}
}

func TestOpenTerminalAttachesAndAFailureNamesTheTerminal(t *testing.T) {
	f := newFixture(t, nil)
	if !f.agent.Menu("agent:open") {
		t.Fatal("agent:open")
	}
	waitFor(t, "the terminal", func() bool { return len(f.launcher.Opened()) == 1 })
	if got := f.launcher.Opened()[0]; !slices.Equal(got, f.host.AttachCommand()) {
		t.Fatal(got)
	}
	// Workers are off by default, so the missing projects folder is left alone (never created,
	// #216) and the terminal starts in home.
	if _, err := os.Stat(filepath.Join(f.home, "Documents", "deskpixie")); err == nil {
		t.Fatal("created the projects folder with workers off")
	}
	if got := f.launcher.Attaches()[0].Dir; got != f.home {
		t.Fatal(got)
	}
	f.launcher.SetFail("no app")
	f.agent.OpenTerminal()
	waitFor(t, "the failure line", func() bool { return strings.HasPrefix(f.agent.Tray().Status, "Coordinator: ") })
}

// #116: the shell's FocusPlan outcome reaches the tray through the same TerminalFailureStatus
// parsing as an OpenTerminal failure, so wording never diverges.
func TestReportFocusOutcomeFailureNamesTheTerminal(t *testing.T) {
	f := newFixture(t, nil)
	f.agent.ReportFocusOutcome(false, "could not open Warp: no app")
	waitFor(t, "the failure line", func() bool { return f.agent.Tray().Status == "Coordinator: could not open Warp" })
}

func TestUserWordsHeardWhileBitSpeaksNeverReachTheAgent(t *testing.T) {
	f := newFixture(t, nil)
	var mu sync.Mutex
	var got []voice.VoiceSignal
	f.agent.toAgent = func(s voice.VoiceSignal) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	}
	v := newFakeVoice()
	f.agent.AttachVoice(v)
	v.signals <- voice.VoiceSignal{Kind: voice.SigOpened}
	f.agent.OnMouth(3) // Pixie speaks
	v.signals <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"}
	f.agent.OnMouth(0) // and stops: the tail still drops
	v.signals <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"}
	v.signals <- voice.VoiceSignal{Kind: voice.SigClosed}
	waitFor(t, "the close", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 })
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Kind != voice.SigOpened || got[1].Kind != voice.SigClosed {
		t.Fatalf("%+v", got)
	}
	if f.agent.link.IsOpen() {
		t.Fatal("still open after Closed")
	}
}

func TestBuildIsOffWhenTheAgentIsDisabled(t *testing.T) {
	s := settings.Default().Agent
	s.Enabled = false
	if Build(Config{Settings: s, AppSupport: t.TempDir(), Home: t.TempDir()}) != nil {
		t.Fatal("built")
	}
	if Factory(settings.Config{Agent: s}, t.TempDir(), app.AgentHooks{}) != nil {
		t.Fatal("Factory must return a nil interface when off")
	}
}

func TestBuildInstallsFilesClearsStaleMcpAndTrustsTheWorkspace(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	paths := NewPaths(data)
	if err := os.MkdirAll(paths.McpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(paths.McpDir, "old-worker.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, func(c *Config) { c.AppSupport, c.Home = data, home })
	_ = f
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale MCP config left")
	}
	if readFile(t, paths.Settings) != shippedCoordinatorSettings(t, paths, settings.Default().Agent.Port) {
		t.Fatal("settings not installed")
	}
	claude := readFile(t, filepath.Join(home, ".claude.json"))
	if !strings.Contains(claude, paths.Workspace) {
		t.Fatal("workspace not trusted:", claude)
	}
}

func TestSetupProblemsBecomeTheStatusLine(t *testing.T) {
	busy := newFixture(t, func(c *Config) {
		c.Listen = func(uint16) (net.Listener, error) { return nil, &net.OpError{Op: "listen"} }
	})
	waitFor(t, "port busy", func() bool { return busy.agent.Tray().Status == "Coordinator: hook port busy" })

	missing := newFixture(t, func(c *Config) { c.Hosts = nil; p := "/definitely/not/herdr"; c.Settings.HerdrPath = &p })
	waitFor(t, "herdr not found", func() bool { return missing.agent.Tray().Status == "Coordinator: herdr not found" })
	out := missing.tool(t, "delegate").Invoke(context.Background(), json.RawMessage(`{"task":"x"}`))
	if !strings.Contains(string(out.Output), "error") {
		t.Fatal(string(out.Output))
	}

	unknown := newFixture(t, func(c *Config) { c.Settings.SessionHost = "screen" })
	waitFor(t, "invalid settings", func() bool { return unknown.agent.Tray().Status == "Coordinator: invalid settings (see log)" })

	noTmux := newFixture(t, func(c *Config) {
		c.Hosts = nil
		c.Settings.SessionHost = "tmux"
		p := "/definitely/not/tmux"
		c.Settings.TmuxPath = &p
	})
	waitFor(t, "tmux not found", func() bool { return noTmux.agent.Tray().Status == "Coordinator: tmux not found" })
}

// Under tmux the Claude settings files carry the state hooks; under Herdr they don't.
func TestBuildInstallsTheStateHooksOnlyForTmux(t *testing.T) {
	data := t.TempDir()
	newFixture(t, func(c *Config) {
		c.AppSupport = data
		c.Settings.SessionHost = "tmux"
		c.Hosts["tmux"] = c.Hosts["herdr"]
	})
	paths := NewPaths(data)
	port := settings.Default().Agent.Port
	for _, p := range []string{paths.Settings, paths.WorkerSettings} {
		if err := ValidateStateHooks(readFile(t, p), port); err != nil {
			t.Fatal(p, err)
		}
	}
}

func TestTheIntegrationHintReachesTheTray(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Hosts["herdr"] = func(d HostDeps) (agent.SessionHost, error) {
			return NewIntegrationCheck(agenttest.NewFakeSessionHost(), func() bool { return true }, d.Hint), nil
		}
	})
	f.agent.OpenTerminal()
	waitFor(t, "the hint", func() bool { return f.agent.Tray().Hint == IntegrationHint })
}

func TestWarmStartsTheCoordinatorNow(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Settings.Warm = true })
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
}

// Quit: pending permissions are denied, the panes stop, mcp/*.json is deleted, the host shut
// down and the hook server closed.
func TestShutdownDeniesStopsPanesClearsMcpAndShutsTheHostDown(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Settings.KeepPanesOnQuit = false
		c.Settings.Workers = settings.WorkerSettings{Enabled: true, Max: 2, Kinds: []string{"claude"}}
		c.Settings.ProjectsRoot = "~/code"
		_ = os.MkdirAll(filepath.Join(c.Home, "code"), 0o755)
	})
	if _, err := os.Stat(NewPaths(f.data).CoordinatorMcp()); err != nil {
		t.Fatal("workers on: the coordinator's MCP config is written:", err)
	}
	f.agent.Menu("agent:open")
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	token := f.coordinatorToken(t)
	type reply struct {
		status int
		body   string
	}
	pending := make(chan reply, 1)
	go func() {
		s, b := f.post(t, "/hooks/permission", token, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "git push"}})
		pending <- reply{s, b}
	}()
	waitFor(t, "the question", func() bool { return len(f.rec.notesOrNil()) > 0 })
	addr := f.agent.Addr()
	began := time.Now()
	f.agent.Shutdown()
	if time.Since(began) > 5*time.Second {
		t.Fatal("shutdown took too long")
	}
	select {
	case r := <-pending:
		if !strings.Contains(r.body, "deny") {
			t.Fatal(r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the permission was never answered")
	}
	if !slices.Contains(f.host.Closed(), agent.Coordinator) {
		t.Fatal("the coordinator pane was not stopped")
	}
	entries, _ := os.ReadDir(NewPaths(f.data).McpDir)
	if len(entries) != 0 {
		t.Fatal("mcp/*.json left:", entries)
	}
	if f.host.shut.Load() != 1 {
		t.Fatal("host not shut down")
	}
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the hook server still listens")
	}
	f.agent.Shutdown() // idempotent
}

func TestKeepPanesOnQuitLeavesTheCoordinatorRunning(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Settings.KeepPanesOnQuit = true })
	f.agent.Menu("agent:open")
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	f.agent.Shutdown()
	if slices.Contains(f.host.Closed(), agent.Coordinator) {
		t.Fatal("closed despite keepPanesOnQuit")
	}
	if f.host.shut.Load() != 1 {
		t.Fatal("host not shut down")
	}
}

func (r *recorder) notesOrNil() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notes)
}

// The terminal starts in the projects folder when it exists.
func TestTheTerminalStartsInTheProjectsFolder(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Settings.ProjectsRoot = "~/code"
		_ = os.MkdirAll(filepath.Join(c.Home, "code"), 0o755)
	})
	f.agent.Menu("agent:open")
	waitFor(t, "the terminal", func() bool { return len(f.launcher.Opened()) == 1 })
	want, _ := filepath.EvalSymlinks(filepath.Join(f.home, "code"))
	if got := f.launcher.Attaches()[0].Dir; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

// keepPanesOnQuit (the default): the quit leaves the coordinator running and saves its token;
// the next start adopts it with that token instead of replacing it.
func TestAQuitKeepsTheCoordinatorAndTheNextStartAdoptsIt(t *testing.T) {
	f := newFixture(t, nil)
	f.agent.Menu("agent:open")
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	token := f.coordinatorToken(t)
	f.agent.Shutdown()
	if slices.Contains(f.host.Closed(), agent.Coordinator) {
		t.Fatal("the coordinator pane was stopped")
	}
	path := NewPaths(f.data).Kept()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("no kept file:", err)
	}
	next := newFixture(t, func(c *Config) {
		c.AppSupport = f.data
		c.Hosts = map[string]HostFactory{"herdr": func(HostDeps) (agent.SessionHost, error) { return f.host, nil }}
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the kept file is read once:", err)
	}
	next.agent.Menu("agent:open")
	time.Sleep(200 * time.Millisecond)
	if len(f.host.Starts()) != 1 || len(f.host.Closed()) != 0 {
		t.Fatal("adopted, not replaced:", f.host.Starts(), f.host.Closed())
	}
	if s, _ := next.post(t, "/hooks/stop", token, map[string]any{"hook_event_name": "Stop", "last_assistant_message": "hi"}); s == 401 {
		t.Fatal("the kept token is refused")
	}
}

// agent.openConversationForUpdates: a done reply while no conversation is open makes the agent
// ask the voice loop to open one; its Opened replays the reply once.
func TestADoneReplyWhileClosedOpensAConversationThatSaysIt(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture(t, func(c *Config) { c.Settings.OpenConversationForUpdates = on })
		v := newFakeVoice()
		f.agent.AttachVoice(v)
		job := f.delegate(t, "count the open issues")
		waitFor(t, "the prompt", func() bool { return len(f.host.Prompts()) > 0 })
		f.post(t, "/hooks/stop", f.coordinatorToken(t), map[string]any{
			"last_assistant_message": `<spoken ref="` + string(job) + `" status="done">There are twelve open issues.</spoken>`,
		})
		waitFor(t, "Pixie celebrates", func() bool { c, _ := f.rec.counts(); return c == 1 })
		if !on {
			time.Sleep(50 * time.Millisecond)
			if v.Opens() != 0 {
				t.Fatal("opened with the flag off")
			}
			continue
		}
		waitFor(t, "the open", func() bool { return v.Opens() == 1 })
		v.signals <- voice.VoiceSignal{Kind: voice.SigOpened}
		want := "Coordinator replies while you were away:\n- Coordinator reply: There are twelve open issues."
		waitFor(t, "the reply said", func() bool { return slices.Contains(v.Injected(), want) })
		time.Sleep(50 * time.Millisecond)
		if n := len(v.Injected()); n != 1 || v.Opens() != 1 {
			t.Fatalf("injected %q, %d opens", v.Injected(), v.Opens())
		}
	}
}

func TestTheUpdateQuietTimeIsTheDebounceSetting(t *testing.T) {
	if UpdateQuietMs(settings.NotificationSettings{DebounceSecs: 7}) != 7_000 || UpdateQuietMs(settings.NotificationSettings{}) != 0 {
		t.Fatal("debounceSecs is seconds, 0 stays 0")
	}
}

func TestUpdatesIsTheBubblesCurrentList(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "an empty list", func() bool {
		items, ok := f.agent.Updates().([]agent.UpdateItem)
		return ok && items != nil && len(items) == 0
	})
}

func manualProbes(f *fixture, model string, lists, org bool) {
	f.agent.model, f.agent.coordModel = model, model
	claude := agent.ClaudeLacksAuto
	if lists {
		claude = agent.ClaudeListsAuto
	}
	f.agent.claudeListsAuto = func() agent.ClaudeAuto { return claude }
	f.agent.managedOff = func() bool { return org }
}

func TestManualModeShowsTheCauseAndTheModelFix(t *testing.T) {
	f := newFixture(t, nil)
	manualProbes(f, "claude-sonnet-4-5", true, false)
	f.agent.ManualMode(agent.Coordinator, true)
	waitFor(t, "the cause", func() bool { return f.agent.Tray().Mode != "" })
	tr := f.agent.Tray()
	if tr.Mode != "Coordinator is in Manual mode: its model is too old for auto mode" || tr.ModeFix == nil || tr.ModeFix.ID != "agent:fix-model" {
		t.Fatalf("%+v", tr)
	}
	f.agent.ManualMode(agent.Coordinator, false)
	if tr := f.agent.Tray(); tr.Mode != "" || tr.ModeFix != nil {
		t.Fatalf("not cleared: %+v", tr)
	}
}

func TestOldClaudeOffersTheUpdateCommandToCopy(t *testing.T) {
	f := newFixture(t, nil)
	manualProbes(f, "", false, false)
	f.agent.ManualMode("fix-login", true)
	waitFor(t, "the cause", func() bool { return f.agent.Tray().Mode != "" })
	tr := f.agent.Tray()
	if tr.Mode != "A worker is in Manual mode: Claude Code is too old for auto mode" || tr.ModeFix == nil || tr.ModeFix.Copy != "claude update" || tr.ModeFix.ID != "" {
		t.Fatalf("%+v", tr)
	}
}

func TestAnAgentClearedBeforeTheProbeFinishesShowsNothing(t *testing.T) {
	f := newFixture(t, nil)
	manualProbes(f, "", true, false)
	release := make(chan struct{})
	f.agent.claudeListsAuto = func() agent.ClaudeAuto { <-release; return agent.ClaudeListsAuto }
	f.agent.ManualMode(agent.Coordinator, true)
	f.agent.ManualMode(agent.Coordinator, false)
	close(release)
	time.Sleep(50 * time.Millisecond)
	if tr := f.agent.Tray(); tr.Mode != "" {
		t.Fatalf("%+v", tr)
	}
}

func spawnWorker(t *testing.T, f *fixture, name string) []string {
	t.Helper()
	_ = os.MkdirAll(filepath.Join(f.home, "code", "repo", ".worktrees", name), 0o755)
	p, err := f.agent.recipes.Prepare(agent.WorkerLaunch{Name: name, Kind: agent.Claude, Repo: filepath.Join(f.home, "code", "repo"), Worktree: filepath.Join(f.home, "code", "repo", ".worktrees", name), Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return p.Spec.Args
}

func TestFixWithOnlyAWorkerFlaggedKeepsTheCoordinatorAndFixesNewWorkers(t *testing.T) {
	var cleared, restarted atomic.Int32
	f := newFixture(t, func(c *Config) {
		c.Hooks.ClearModel = func() { cleared.Add(1) }
		c.Settings.Workers = settings.WorkerSettings{Enabled: true, Max: 2, Kinds: []string{"claude"}}
		c.Settings.ProjectsRoot = "~/code"
		_ = os.MkdirAll(filepath.Join(c.Home, "code"), 0o755)
	})
	f.agent.restart = func() { restarted.Add(1) }
	manualProbes(f, "claude-sonnet-4-5", true, false)
	if !slices.Contains(spawnWorker(t, f, "old-one"), "claude-sonnet-4-5") {
		t.Fatal("the old worker starts on the model")
	}
	f.agent.ManualMode("old-one", true)
	waitFor(t, "the cause", func() bool { return f.agent.Tray().ModeFix != nil })
	f.agent.Menu("agent:fix-model")
	if cleared.Load() != 1 || restarted.Load() != 0 {
		t.Fatal("cleared/restarted", cleared.Load(), restarted.Load())
	}
	if slices.Contains(spawnWorker(t, f, "new-one"), "--model") {
		t.Fatal("a new worker uses Claude Code's default")
	}
	tr := f.agent.Tray()
	if tr.Mode != "A worker is in Manual mode: its model is too old for auto mode" || tr.ModeFix != nil {
		t.Fatalf("%+v", tr)
	}
}

func TestFixWithAFlaggedClaudeCoordinatorRestartsItWithoutTheModel(t *testing.T) {
	var restarted atomic.Int32
	f := newFixture(t, nil)
	f.agent.restart = func() { restarted.Add(1) }
	manualProbes(f, "claude-sonnet-4-5", true, false)
	f.agent.coordModel = "claude-sonnet-4-5"
	f.agent.ManualMode(agent.Coordinator, true)
	waitFor(t, "the fix", func() bool { return f.agent.Tray().ModeFix != nil })
	f.agent.Menu("agent:fix-model")
	if restarted.Load() != 1 || f.agent.model != "" || f.agent.coordModel != "" {
		t.Fatal("not restarted", restarted.Load())
	}
	if tr := f.agent.Tray(); tr.ModeFix != nil {
		t.Fatalf("the fix is applied: %+v", tr)
	}
}

func TestFixNeverRestartsACodexCoordinator(t *testing.T) {
	var restarted atomic.Int32
	f := newFixture(t, nil)
	f.agent.restart = func() { restarted.Add(1) }
	f.agent.kind = agent.Codex
	manualProbes(f, "claude-sonnet-4-5", true, false)
	f.agent.ManualMode(agent.Coordinator, true)
	waitFor(t, "the fix", func() bool { return f.agent.Tray().ModeFix != nil })
	f.agent.Menu("agent:fix-model")
	if restarted.Load() != 0 {
		t.Fatal("a Codex coordinator is never restarted")
	}
}

func TestManualModeAfterShutdownStartsNothing(t *testing.T) {
	f := newFixture(t, nil)
	manualProbes(f, "", true, false)
	f.agent.Shutdown()
	f.agent.ManualMode(agent.Coordinator, true) // must not touch the wait group
}
