//go:build live

package tmux_test

// Opt-in live smokes of the tmux session host against the installed tmux (3.2 or newer),
// Claude Code (and optionally Codex) and git, mirroring core/herdr/live_test.go. Run with:
//
//	go test -count=1 -tags live -run LiveTmux -v -timeout 20m ./tmux/
//
// Everything is throwaway: the tmux server smoketest.SmokeSession (`tmux -L deskpixie-smoke`,
// never the app's "deskpixie" and never the default socket), temp workspaces and projects
// roots, and hook servers on free loopback ports (never the app's 48217). The server is killed
// at the end (SMOKE_KEEP=1 keeps it). Claude Code records folder trust for the temp workspace
// and worktrees in ~/.claude.json, as with the Herdr smokes. No tokens or env values are
// printed.
//
// Knobs: SMOKE_MODEL (worker smoke coordinator, default sonnet); SMOKE_CODEX=1 adds the Codex
// part of the worker smoke (needs codex).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/tmux"
	"github.com/sihekuang/deskpixie/core/tmux/smoketest"
)

const (
	smokeJob  = "job_00000000beef"
	smokeTurn = 2 * time.Minute
)

func info(t *testing.T, question, answer string) { t.Logf("info %s: %s", question, answer) }
func ok(t *testing.T, step string)               { t.Logf("ok   %s", step) }

// hookServer serves the hook/MCP endpoints on a free loopback port and hands every event to
// deliver. Stopped at the end of the test.
func hookServer(t *testing.T, tokens *httpserver.Tokens, deliver func(agent.HookEvent) bool) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("the smoke must never use the app's port 48217")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = httpserver.Serve(ctx, ln, tokens, httpserver.DeliverFunc(deliver))
	}()
	t.Cleanup(func() { cancel(); <-done })
	return port
}

// versions logs tmux's and Claude Code's versions.
func versions(t *testing.T, sm *smoketest.LiveSmoke) {
	if out, err := sm.Runner.Run([]string{sm.Bin, "-V"}, sm.Env); err == nil {
		info(t, "tmux", strings.TrimSpace(out.Stdout))
	}
	if out, err := sm.Runner.Run([]string{"claude", "--version"}, sm.Env); err == nil {
		info(t, "claude", strings.TrimSpace(out.Stdout))
	}
}

// markersIn names the markers of each list that show on screen.
func markersIn(screen string) string {
	var seen []string
	for _, group := range []struct {
		name    string
		markers []string
	}{{"ready", tmux.ClaudeReadyMarkers}, {"dialog", tmux.ClaudeDialogMarkers}, {"busy", agent.BusyMarkers}} {
		for _, m := range group.markers {
			if strings.Contains(screen, m) {
				seen = append(seen, group.name+" "+fmt.Sprintf("%q", m))
			}
		}
	}
	if len(seen) == 0 {
		return "none"
	}
	return strings.Join(seen, ", ")
}

// waitPrompt answers Claude Code's folder-trust dialog if it shows ("No, exit" is preselected:
// Down, Enter) and waits for the ready prompt. It logs which markers the real screens show.
func waitPrompt(ctx context.Context, t *testing.T, host *tmux.Host, name string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	answered := false
	for {
		screen, _ := host.Read(ctx, name, 40)
		if s, up := tmux.StartedScreen(agent.Claude, screen); up && s == agent.PaneIdle {
			info(t, "the ready prompt: markers", markersIn(screen))
			return
		}
		if !answered && strings.Contains(screen, "trust this folder") {
			answered = true
			info(t, "the folder-trust dialog: markers", markersIn(screen))
			info(t, "the folder-trust dialog: Classify / StartedScreen", fmt.Sprint(tmux.Classify(agent.Claude, screen), " / ", startedOf(screen)))
			if !answerTrust(ctx, host, name) {
				t.Fatalf("%s: the trust dialog never took Down", name)
			}
			ok(t, "folder-trust dialog answered")
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached a prompt; pane:\n%s", name, screen)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// answerTrust picks "Yes, I trust this folder" ("No, exit" is preselected) once the dialog
// takes keys: a Down sent as soon as it is drawn can be lost, and Enter would then exit.
func answerTrust(ctx context.Context, host *tmux.Host, name string) bool {
	for range 20 {
		_ = host.SendKeys(ctx, name, []string{"Down"})
		time.Sleep(500 * time.Millisecond)
		screen, _ := host.Read(ctx, name, 40)
		if strings.Contains(screen, "❯ Yes") {
			return host.SendKeys(ctx, name, []string{"Enter"}) == nil
		}
	}
	return false
}

// settled waits until the host lists name idle (its screen poll caught up with the ready
// prompt), so the events that follow are the next turn's.
func settled(ctx context.Context, t *testing.T, host *tmux.Host, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, _ := host.List(ctx)
		if slices.ContainsFunc(list, func(a agent.AgentInfo) bool { return a.Name == name && a.Status == agent.PaneIdle }) {
			time.Sleep(time.Second)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never listed idle: %+v", name, list)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func startedOf(screen string) string {
	s, up := tmux.StartedScreen(agent.Claude, screen)
	return fmt.Sprint(s, " started=", up)
}

func waitStop(t *testing.T, hooks <-chan agent.HookEvent) (string, error) {
	t.Helper()
	timer := time.NewTimer(smokeTurn)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return "", fmt.Errorf("no Stop hook within %v", smokeTurn)
		case ev := <-hooks:
			switch ev.Kind {
			case agent.HookStop:
				return ev.Message, nil
			case agent.HookPermission:
				ev.Reply <- agent.Deny("Unexpected in this step.")
				return "", fmt.Errorf("unexpected permission request for %s", ev.ToolName)
			}
		}
	}
}

func waitPermission(t *testing.T, hooks <-chan agent.HookEvent, d agent.Decision, screen func() string) error {
	t.Helper()
	const q = "permissions.ask raises PermissionRequest under --permission-mode auto"
	timer := time.NewTimer(smokeTurn)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			info(t, q, "no (no hook within 2 minutes)")
			return fmt.Errorf("no PermissionRequest hook within %v", smokeTurn)
		case ev := <-hooks:
			switch ev.Kind {
			case agent.HookPermission:
				info(t, q, "yes")
				s := screen()
				info(t, "screen while the PermissionRequest hook waits: markers / Classify", markersIn(s)+" / "+tmux.Classify(agent.Claude, s).String())
				ev.Reply <- d
				return nil
			case agent.HookStop:
				info(t, q, "no (the turn ended without a permission request)")
				return fmt.Errorf("turn ended without a permission request: %q", ev.Message)
			}
		}
	}
}

// stateLog records the state hooks (agent and status, in order).
type stateLog struct {
	mu  sync.Mutex
	got []string
}

func (s *stateLog) add(ev agent.HookEvent) {
	s.mu.Lock()
	s.got = append(s.got, ev.Agent+" "+ev.Status.String())
	s.mu.Unlock()
}

func (s *stateLog) since(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got[min(n, len(s.got)):])
}

func (s *stateLog) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// events collects a host's PaneEvents for one agent.
func collect(rx <-chan agent.PaneEvent) (func() []string, func()) {
	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	go func() {
		for {
			select {
			case ev, ok := <-rx:
				if !ok {
					return
				}
				mu.Lock()
				if ev.Kind == agent.PaneEventExited {
					got = append(got, ev.Agent+" exited")
				} else {
					got = append(got, ev.Agent+" "+ev.Status.String())
				}
				mu.Unlock()
			case <-done:
				return
			}
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}, func() { close(done) }
}

// TestLiveTmuxAgentSmoke: a coordinator on the subscription (the server env scrubs the
// parent's API key) started by the tmux host; its state hooks (working, idle) reach the hook
// server and the host; a multi-line prompt arrives as one; a Stop-hook reply with its
// <spoken ref>; a PermissionRequest allowed then denied; a real permission dialog (no
// PermissionRequest hook) blocks, and Escape ends that turn; /exit is Exited once; and the
// SessionHost conformance suite, live, with the probe's own hooks.
func TestLiveTmuxAgentSmoke(t *testing.T) {
	sm := smoketest.Smoke(t, agent.EnvVar{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-smoke-not-a-real-key"})
	for _, e := range sm.Env {
		if e.Key == "ANTHROPIC_API_KEY" {
			t.Fatal("ANTHROPIC_API_KEY not scrubbed from the server env")
		}
	}
	versions(t, sm)
	ctx := context.Background()
	tmp := smoketest.CanonicalTemp(t)
	ws, canvas := filepath.Join(tmp, "workspace"), filepath.Join(tmp, "canvas")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte(agentapp.RenderClaudeMd(canvas, "")), 0o644); err != nil {
		t.Fatal(err)
	}

	host := sm.Host()
	t.Cleanup(host.Shutdown)
	// Hook server on a free loopback port: state hooks go to the host (as the runtime does),
	// everything else to the test.
	tokens := httpserver.NewTokens()
	token := tokens.Issue(agent.Coordinator)
	hooks := make(chan agent.HookEvent, 256)
	states := &stateLog{}
	port := hookServer(t, tokens, func(ev agent.HookEvent) bool {
		if ev.Kind == agent.HookState {
			states.add(ev)
			host.ReportStatus(ev.Agent, ev.Status)
			return true
		}
		hooks <- ev
		return true
	})
	// Settings: the shipped template, an ask rule for `touch`, and the state hooks (as the app
	// installs them under tmux).
	const anchor = `"Bash(git push:*)",`
	base := agentapp.RenderSettings(port)
	if !strings.Contains(base, anchor) {
		t.Fatal("the settings template no longer has the git push ask rule to anchor on")
	}
	base = strings.Replace(base, anchor, anchor+"\n      \"Bash(touch:*)\",", 1)
	settings, err := agentapp.WithStateHooks(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentapp.ValidateStateHooks(settings, port); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := host.EnsureSession(ctx); err != nil {
		t.Fatalf("ensure_session: %v", err)
	}
	ok(t, "tmux server "+smoketest.SmokeSession+" running")
	events, stopEvents := collect(host.Subscribe())
	defer stopEvents()

	args := []string{"--model", "haiku", "--permission-mode", "auto", "--settings", settingsPath}
	spec := func(name, token string) agent.AgentSpec {
		return agent.AgentSpec{Name: name, Kind: agent.Claude, Cwd: ws, Args: args,
			Env: []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: token}}}
	}
	info0, err := host.StartAgent(ctx, spec(agent.Coordinator, token))
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	info(t, "StartAgent returned (status)", info0.Status.String())
	waitPrompt(ctx, t, host, agent.Coordinator)
	banner, err := host.Read(ctx, agent.Coordinator, 40)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(banner, "API Usage Billing") {
		t.Fatal("the coordinator runs on an API key, not the subscription")
	}
	ok(t, "coordinator started on the subscription (env scrub holds)")
	settled(ctx, t, host, agent.Coordinator)

	// 1. A multi-line request (pasted) is one prompt; the reply comes through the Stop hook
	// with the job's ref; the state hooks said working, then idle.
	before, beforeEv := states.len(), len(events())
	if err := host.Prompt(ctx, agent.Coordinator, "New request from the user: what is two plus two?\n[ref "+smokeJob+"]"); err != nil {
		t.Fatal(err)
	}
	message, err := waitStop(t, hooks)
	if err != nil {
		screen, _ := host.Read(ctx, agent.Coordinator, 40)
		t.Fatalf("%v\n--- coordinator pane ---\n%s\n---", err, screen)
	}
	blocks := agent.ParseSpoken(message)
	matching := false
	for _, b := range blocks {
		matching = matching || (b.Reference != nil && *b.Reference == smokeJob)
	}
	if !matching {
		t.Fatalf("no <spoken ref=%q> in the Stop hook message: %q", smokeJob, message)
	}
	ok(t, "a two-line prompt went in as one; Stop hook delivered <spoken ref=…>")
	time.Sleep(time.Second)
	gotStates := states.since(before)
	info(t, "state hooks for the turn", fmt.Sprint(gotStates))
	if !slices.Equal(gotStates, []string{"coordinator working", "coordinator idle"}) {
		t.Fatalf("state hooks %q, want working then idle", gotStates)
	}
	gotEvents := events()[beforeEv:]
	info(t, "host events for the turn", fmt.Sprint(gotEvents))
	if !slices.Equal(gotEvents, []string{"coordinator working", "coordinator idle"}) {
		t.Fatalf("host events %q, want working then idle", gotEvents)
	}
	ok(t, "state hooks reached the hook server and the host (working, then idle)")

	// 2. An ask-rule command goes through the PermissionRequest hook: allowed once, then denied.
	read := func() string { s, _ := host.Read(ctx, agent.Coordinator, 40); return s }
	for _, c := range []struct {
		file  string
		allow bool
	}{{"smoke-allowed.txt", true}, {"smoke-denied.txt", false}} {
		if err := host.Prompt(ctx, agent.Coordinator, "Run this exact shell command and nothing else: touch "+c.file+"\n[ref "+smokeJob+"]"); err != nil {
			t.Fatal(err)
		}
		d := agent.Allow()
		if !c.allow {
			d = agent.Deny("Smoke test says no.")
		}
		if err := waitPermission(t, hooks, d, read); err != nil {
			t.Fatal(err)
		}
		if _, err := waitStop(t, hooks); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(ws, c.file)); (err == nil) != c.allow {
			t.Fatalf("%s: exists = %v, expected %v", c.file, err == nil, c.allow)
		}
	}
	ok(t, "PermissionRequest hook: allow ran the command, deny blocked it")

	// 3. /exit: the pane stays (remain-on-exit) but the agent is Exited, once, and not listed.
	if err := host.Prompt(ctx, agent.Coordinator, "/exit"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !slices.Contains(events(), "coordinator exited") {
		if time.Now().After(deadline) {
			t.Fatalf("no Exited after /exit; events %q", events())
		}
		time.Sleep(200 * time.Millisecond)
	}
	list, err := host.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Name == agent.Coordinator {
			t.Fatal("the exited coordinator is listed")
		}
	}
	_ = host.Close(ctx, agent.Coordinator)
	time.Sleep(time.Second)
	exits := 0
	for _, e := range events() {
		if e == "coordinator exited" {
			exits++
		}
	}
	if exits != 1 {
		t.Fatalf("%d Exited for the coordinator", exits)
	}
	ok(t, "/exit: Exited once, not listed")

	// 4. A real permission dialog: an agent whose settings have no PermissionRequest hook shows
	// Claude Code's own prompt. Its Notification state hook says blocked; Escape ends the turn
	// (no Stop hook comes for it) and the screen brings it back to idle.
	var plain map[string]any
	if err := json.Unmarshal([]byte(settings), &plain); err != nil {
		t.Fatal(err)
	}
	delete(plain["hooks"].(map[string]any), "PermissionRequest")
	b, _ := json.MarshalIndent(plain, "", "  ")
	askPath := filepath.Join(tmp, "ask-settings.json")
	if err := os.WriteFile(askPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	askToken := tokens.Issue("asker")
	ask := spec("asker", askToken)
	ask.Args = []string{"--model", "haiku", "--permission-mode", "auto", "--settings", askPath}
	if _, err := host.StartAgent(ctx, ask); err != nil {
		t.Fatalf("start asker: %v", err)
	}
	waitPrompt(ctx, t, host, "asker")
	settled(ctx, t, host, "asker")
	beforeEv = len(events())
	if err := host.Prompt(ctx, "asker", "Run this exact shell command and nothing else: touch smoke-asked.txt"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(smokeTurn)
	var dialog string
	for {
		s, _ := host.Read(ctx, "asker", 40)
		if tmux.Classify(agent.Claude, s) == agent.PaneBlocked {
			dialog = s
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no permission dialog; pane:\n%s", s)
		}
		time.Sleep(300 * time.Millisecond)
	}
	info(t, "the permission dialog: markers", markersIn(dialog))
	deadline = time.Now().Add(20 * time.Second)
	for !slices.Contains(events()[beforeEv:], "asker blocked") {
		if time.Now().After(deadline) {
			t.Fatalf("the asker never reported blocked; events %q, states %q", events()[beforeEv:], states.since(0))
		}
		time.Sleep(200 * time.Millisecond)
	}
	ok(t, "a real permission dialog: Classify blocked, and the host reported blocked")
	statesAtEscape := states.len()
	if err := host.SendKeys(ctx, "asker", []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for events()[len(events())-1] != "asker idle" {
		if time.Now().After(deadline) {
			s, _ := host.Read(ctx, "asker", 40)
			t.Fatalf("Escape did not bring the asker back to idle; events %q\n%s", events()[beforeEv:], s)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(ws, "smoke-asked.txt")); err == nil {
		t.Fatal("the cancelled command ran")
	}
	info(t, "asker events (dialog, Escape)", fmt.Sprint(events()[beforeEv:]))
	time.Sleep(2 * time.Second)
	info(t, "state hooks after Escape on the dialog (none = no Stop for it)", fmt.Sprint(states.since(statesAtEscape)))
	ok(t, "Escape on the dialog: idle again, the command did not run")
	_ = host.Close(ctx, "asker")

	// 5. The SessionHost conformance suite, live, with the probe's own state hooks.
	probeToken := tokens.Issue("probe")
	beforeStates := states.len()
	for len(hooks) > 0 {
		<-hooks
	}
	steps, err := agenttest.RunConformance(ctx, host, spec("probe", probeToken), agenttest.Timeouts{Turn: smokeTurn, Settle: 3 * time.Second})
	for _, s := range steps {
		ok(t, "conformance: "+s)
	}
	if err != nil {
		screen, _ := host.Read(ctx, "probe", 30)
		t.Fatalf("conformance: %v\n--- probe pane ---\n%s\n---", err, screen)
	}
	info(t, "probe state hooks during conformance", fmt.Sprint(states.since(beforeStates)))
	stops := 0
	for len(hooks) > 0 {
		if ev := <-hooks; ev.Kind == agent.HookStop && ev.Agent == "probe" {
			stops++
		}
	}
	info(t, "the probe's Stop hooks during conformance (1 = Escape fires no Stop)", fmt.Sprint(stops))
	_ = host.Close(ctx, "probe")
}

// --- the worker smoke ---

const (
	jobLimit     = 8 * time.Minute
	overallLimit = 20 * time.Minute
	gitPath      = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
)

type wallClock struct{}

func (wallClock) NowMs() agent.Millis { return agent.Millis(time.Now().UnixMilli()) }

func gitIn(dir, home string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "user.name=smoke", "-c", "user.email=smoke@example.invalid", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = []string{"PATH=" + gitPath, "HOME=" + home}
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("git %v: %v %s", args, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

// tomlString is a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// trustOverride is the smoke-only Codex override that trusts repo for the run, written
// nowhere: `-c projects={"<repo>"={trust_level="trusted"}}`.
func trustOverride(repo string) []string {
	return []string{"-c", fmt.Sprintf(`projects={%s={trust_level="trusted"}}`, tomlString(repo))}
}

var callNames = map[agent.McpCallKind]string{
	agent.CallStartWorker: "start_worker", agent.CallMessageWorker: "message_worker", agent.CallStopWorker: "stop_worker",
	agent.CallListWorkers: "list_workers", agent.CallReportProgress: "report_progress", agent.CallReportResult: "report_result",
	agent.CallAskCoordinator: "ask_coordinator", agent.CallReportBlocked: "report_blocked",
}

type startReply struct {
	name string
	err  string
}

type workerSmoke struct {
	t         *testing.T
	handle    *agent.AgentHandle
	host      *tmux.Host
	mu        sync.Mutex
	seen      []string
	starts    []startReply
	repo      string
	untrusted string
	codexConf string
	home      string
	mcpDir    string
}

func (s *workerSmoke) saw(entry string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.seen, entry)
}

func (s *workerSmoke) view(id agent.JobID) (agent.JobView, bool) {
	for _, j := range s.handle.JobStatus() {
		if j.JobID == string(id) {
			return j, true
		}
	}
	return agent.JobView{}, false
}

// job delegates task and waits until the job closes (or jobLimit).
func (s *workerSmoke) job(task string) (agent.JobID, agent.JobView, error) {
	id, err := s.handle.Delegate(task, "")
	if err != nil {
		return "", agent.JobView{}, err
	}
	deadline := time.Now().Add(jobLimit)
	for {
		time.Sleep(2 * time.Second)
		if v, ok := s.view(id); ok && !v.Status.IsOpen() {
			return id, v, nil
		}
		if time.Now().After(deadline) {
			return id, agent.JobView{}, fmt.Errorf("%s did not finish within %v", id, jobLimit)
		}
	}
}

// dump logs the agents, every pane's screen, the hook/MCP traffic and the jobs.
func (s *workerSmoke) dump() {
	ctx := context.Background()
	list, err := s.host.List(ctx)
	if err != nil {
		s.t.Logf("list failed: %v", err)
	}
	for _, a := range list {
		screen, err := s.host.Read(ctx, a.Name, 40)
		s.t.Logf("list: %s status=%v\n--- %s ---\n%s\n--- (%v)", a.Name, a.Status, a.Name, screen, err)
	}
	s.mu.Lock()
	s.t.Logf("hook/MCP traffic: %q", s.seen)
	s.mu.Unlock()
	for _, j := range s.handle.JobStatus() {
		s.t.Logf("job %s %v workers=%+v last_progress=%v", j.JobID, j.Status, j.Workers, j.LastProgress)
	}
}

func (s *workerSmoke) gone(name string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		list, err := s.host.List(context.Background())
		if err == nil && !slices.ContainsFunc(list, func(a agent.AgentInfo) bool { return a.Name == name }) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// committed: the worker's worktree has file committed on mascot/<slug>, and the main checkout
// is untouched.
func (s *workerSmoke) committed(slug, file string) error {
	wt := filepath.Join(s.repo, ".worktrees", slug)
	branch, err := gitIn(wt, s.home, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if branch != "mascot/"+slug {
		return fmt.Errorf("the worktree is on %s, not mascot/%s", branch, slug)
	}
	if log, err := gitIn(wt, s.home, "log", "--oneline", "-1", "--", file); err != nil || log == "" {
		return fmt.Errorf("%s was not committed in the worktree (%v)", file, err)
	}
	if st, err := gitIn(s.repo, s.home, "status", "--porcelain"); err != nil || st != "" {
		return fmt.Errorf("the main checkout changed: %q (%v)", st, err)
	}
	exclude, _ := os.ReadFile(filepath.Join(s.repo, ".git/info/exclude"))
	if !strings.Contains("\n"+string(exclude), "\n.worktrees/\n") && !strings.HasSuffix(string(exclude), "\n.worktrees/") {
		return fmt.Errorf(".worktrees/ is not in .git/info/exclude")
	}
	return nil
}

// answerTrustDialogs answers Claude Code's folder-trust dialog in any pane younger than a
// minute, once per pane, until ctx ends.
func answerTrustDialogs(ctx context.Context, host *tmux.Host, answered *[]string, mu *sync.Mutex) {
	firstSeen := map[string]time.Time{}
	done := map[string]bool{}
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		list, err := host.List(ctx)
		if err != nil {
			continue
		}
		for _, a := range list {
			if _, ok := firstSeen[a.Name]; !ok {
				firstSeen[a.Name] = time.Now()
			}
			if done[a.Name] || time.Since(firstSeen[a.Name]) >= time.Minute {
				continue
			}
			screen, _ := host.Read(ctx, a.Name, 40)
			if strings.Contains(screen, "trust this folder") && strings.Contains(screen, "No, exit") &&
				!strings.Contains(screen, "for shortcuts") && !strings.Contains(screen, "Do you want to") {
				done[a.Name] = true
				if !answerTrust(ctx, host, a.Name) {
					continue
				}
				mu.Lock()
				*answered = append(*answered, a.Name)
				mu.Unlock()
			}
		}
	}
}

// TestLiveTmuxWorkerSmoke: the real delegation runtime with workers on the tmux host and the
// real adapters (httpserver with the state hooks, GitWorktrees, FileWorkerRecipes,
// ClaudeTrustStore); only voice, the notifier and the job store are in-memory fakes. A Claude
// worker commits a file on its branch in a throwaway repo's worktree, a cancelled job stops
// its worker, and shutdown closes every pane.
func TestLiveTmuxWorkerSmoke(t *testing.T) {
	codex := os.Getenv("SMOKE_CODEX") == "1"
	if codex {
		if _, err := exec.LookPath("codex"); err != nil {
			if _, err := os.Stat("/opt/homebrew/bin/codex"); err != nil {
				t.Skip("SMOKE_CODEX=1 but codex not found")
			}
		}
	}
	sm := smoketest.Smoke(t)
	versions(t, sm)
	ctx := context.Background()
	tmp := smoketest.CanonicalTemp(t)
	projects := filepath.Join(tmp, "projects")
	repo, untrusted := filepath.Join(projects, "demo"), filepath.Join(projects, "untrusted")
	for _, r := range []string{repo, untrusted} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, nil, {"add", "README.md"}, {"commit", "-q", "-m", "first"}} {
			if args == nil {
				if err := os.WriteFile(filepath.Join(r, "README.md"), []byte(filepath.Base(r)+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if _, err := gitIn(r, sm.Home, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(sm.Home, ".codex")
	}

	paths := agentapp.NewPaths(filepath.Join(tmp, "data"))
	tokens := httpserver.NewTokens()
	coordinatorToken := tokens.Issue(agent.Coordinator)

	ws := &workerSmoke{t: t, repo: repo, untrusted: untrusted, codexConf: filepath.Join(codexHome, "config.toml"), home: sm.Home, mcpDir: paths.McpDir}
	var handle *agent.AgentHandle
	handleReady := make(chan struct{})
	port := hookServer(t, tokens, func(ev agent.HookEvent) bool {
		<-handleReady
		entry := ev.Agent
		switch ev.Kind {
		case agent.HookMcp:
			entry += " " + callNames[ev.Call.Kind]
			if ev.Call.Kind == agent.CallStartWorker {
				toServer := ev.McpReply
				mine := make(chan agent.McpReply, 1)
				ev.McpReply = mine
				go func() {
					r, okr := <-mine
					if !okr {
						return
					}
					rep := startReply{}
					if r.Err != nil {
						rep.err = r.Err.Error()
					} else {
						b, _ := json.Marshal(r.Value)
						var m map[string]any
						_ = json.Unmarshal(b, &m)
						rep.name, _ = m["name"].(string)
					}
					ws.mu.Lock()
					ws.starts = append(ws.starts, rep)
					ws.mu.Unlock()
					toServer <- r
				}()
			}
		case agent.HookStop:
			entry += " stop"
		case agent.HookPermission:
			entry += " permission " + ev.ToolName
		case agent.HookPostTool:
			entry += " post-tool " + ev.ToolName
		case agent.HookState:
			entry += " state " + ev.Status.String()
		}
		ws.mu.Lock()
		ws.seen = append(ws.seen, entry)
		ws.mu.Unlock()
		return handle.Deliver(ev)
	})
	// As the app installs them under tmux: with the state hooks.
	if err := agentapp.InstallFor(paths, port, projects, true); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.McpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agentapp.WriteCoordinatorMcp(paths, port, coordinatorToken); err != nil {
		t.Fatal(err)
	}
	claudeJSON := filepath.Join(sm.Home, ".claude.json")
	if _, err := agentfs.NewClaudeTrustStore(claudeJSON, []string{filepath.Join(tmp, "data")}).Grant(paths.Workspace); err != nil {
		t.Fatalf("trust the workspace: %v", err)
	}

	host := sm.Host()
	t.Cleanup(host.Shutdown)
	ws.host = host
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatalf("ensure_session: %v", err)
	}
	ok(t, "tmux server "+smoketest.SmokeSession+" running")
	bctx, stopBabysitter := context.WithCancel(ctx)
	var trustedMu sync.Mutex
	var trusted []string
	babysat := make(chan struct{})
	go func() { defer close(babysat); answerTrustDialogs(bctx, host, &trusted, &trustedMu) }()

	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "sonnet"
	}
	recipe := agent.CoordinatorRecipe{
		Cwd:  paths.Workspace,
		Args: agentapp.CoordinatorArgs(paths, model, true),
		Env:  []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: coordinatorToken}},
	}
	deps := agent.Deps{
		Host:     agent.NewAgentHost(host, wallClock{}, recipe),
		Voice:    &agenttest.RecordingVoiceLink{},
		Notifier: &agenttest.RecordingNotifier{},
		Mascot:   &agenttest.RecordingNotifier{},
		Status:   &agenttest.RecordingStatus{},
		Launcher: &agenttest.FakeLauncher{},
		Store:    &agenttest.MemStore{},
		Clock:    wallClock{},
		IDs:      &agenttest.SeqIds{},
	}
	workerTrust := agentfs.NewClaudeTrustStore(claudeJSON, []string{projects})
	worktrees := agentfs.NewGitWorktrees(projects, paths.Workspace, sm.Home, proc.PatientRunner(agentapp.GitTimeout)).
		WithCleanupRunner(proc.PatientRunner(agentapp.GitCleanupTimeout))
	extra := agentfs.CodexExtraArgs(func(r string) []string {
		if r == repo {
			return trustOverride(repo)
		}
		return nil
	})
	kinds := []agent.AgentKind{agent.Claude}
	if codex {
		kinds = append(kinds, agent.Codex)
	}
	settings := agent.DefaultSettings()
	settings.PermissionTimeoutMs = 10_000
	settings.Workers = agent.WorkerPolicy{Enabled: true, Max: 2, Kinds: kinds}
	handle = agent.NewRuntimeWithWorkers(deps, agent.RuntimeConfig{Settings: settings}, &agent.WorkerDeps{
		Worktrees: worktrees,
		Tokens:    tokens,
		Recipes:   agentfs.NewFileWorkerRecipes(paths.McpDir, paths.WorkerSettings, port, "haiku", workerTrust).WithCodexExtraArgs(extra),
	})
	ws.handle = handle
	close(handleReady)
	rctx, stopRuntime := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() { defer close(ran); handle.Run(rctx) }()

	errc := make(chan error, 1)
	go func() { errc <- ws.scenarios(codex) }()
	var outcome error
	select {
	case outcome = <-errc:
	case <-time.After(overallLimit):
		outcome = fmt.Errorf("the whole run did not finish within %v", overallLimit)
	}
	if outcome != nil {
		ws.dump()
		handle.Shutdown()
	}
	trustedMu.Lock()
	info(t, "folder-trust dialogs answered by the smoke", fmt.Sprint(trusted))
	trustedMu.Unlock()
	stopBabysitter()
	<-babysat
	stopRuntime()
	<-ran
	if outcome != nil {
		t.Fatal(outcome)
	}
}

func (s *workerSmoke) scenarios(codex bool) error {
	t := s.t
	ctx := context.Background()
	// 1. A Claude worker does a small repo task end to end.
	id, view, err := s.job("In the repository demo, have a claude worker create a file HELLO.txt containing the single word hi and commit it on its branch. Wait for its report, then tell me what it did.")
	if err != nil {
		return err
	}
	info(t, "the coordinator called start_worker over MCP", yesNo(s.saw("coordinator start_worker")))
	info(t, "the coordinator's state hooks reached the app", yesNo(s.saw("coordinator state working") && s.saw("coordinator state idle")))
	if len(view.Workers) == 0 {
		return fmt.Errorf("%s closed without a worker (status %v)", id, view.Status)
	}
	worker := view.Workers[0].Name
	slug := strings.TrimPrefix(worker, "demo-")
	info(t, "the Claude worker reported with report_result", yesNo(s.saw(worker+" report_result")))
	info(t, "the Claude worker's Stop hook reached the app", yesNo(s.saw(worker+" stop")))
	info(t, "the Claude worker's state hooks reached the app", yesNo(s.saw(worker+" state working") && s.saw(worker+" state idle")))
	if view.Status != agent.JobDone {
		return fmt.Errorf("%s ended %v (last progress: %v)", id, view.Status, view.LastProgress)
	}
	if err := s.committed(slug, "HELLO.txt"); err != nil {
		return err
	}
	ok(t, fmt.Sprintf("claude worker %s: HELLO.txt committed on mascot/%s; the main checkout is untouched; .worktrees/ excluded", worker, slug))
	if !s.gone(worker, 20*time.Second) {
		return fmt.Errorf("%s's pane is still open after its job ended", worker)
	}
	if _, err := os.Stat(filepath.Join(s.mcpDir, worker+".json")); err == nil {
		return fmt.Errorf("%s's MCP config was not deleted", worker)
	}
	ok(t, "the job's end stopped its worker and deleted its MCP config")

	// 2. Cancelling a job stops its worker.
	long, err := s.handle.Delegate("In the repository demo, have a claude worker write the numbers 1 to 2000 to COUNT.txt, one per line, one shell command per number, reporting progress every 200 numbers.", "")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(jobLimit)
	running := ""
	for running == "" {
		time.Sleep(2 * time.Second)
		if v, ok := s.view(long); ok {
			for _, w := range v.Workers {
				if w.Status == agent.WorkerRunning {
					running = w.Name
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no worker started for the long job")
		}
	}
	if _, err := s.handle.Cancel(string(long)); err != nil {
		return err
	}
	if !s.gone(running, 30*time.Second) {
		return fmt.Errorf("jobs cancel left %s running", running)
	}
	ok(t, fmt.Sprintf("jobs cancel stopped worker %s (Escape, then close)", running))

	if codex {
		if err := s.codexPart(); err != nil {
			return err
		}
	}

	// Quit: every pane closes.
	s.handle.Shutdown()
	list, _ := s.host.List(ctx)
	if len(list) != 0 {
		var names []string
		for _, a := range list {
			names = append(names, a.Name)
		}
		return fmt.Errorf("panes left after shutdown: %v", names)
	}
	ok(t, "shutdown closed every agent pane")
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// watchTrust records the first worker pane that shows Codex's trust screen, until ctx ends.
func (s *workerSmoke) watchTrust(ctx context.Context, seen *string, mu *sync.Mutex) {
	for ctx.Err() == nil {
		if list, err := s.host.List(ctx); err == nil {
			for _, a := range list {
				if !strings.HasPrefix(a.Name, "demo-") && !strings.HasPrefix(a.Name, "untrusted-") {
					continue
				}
				if screen, err := s.host.Read(ctx, a.Name, 40); err == nil && agent.ShowsCodexTrust(screen) {
					mu.Lock()
					if *seen == "" {
						*seen = a.Name
					}
					mu.Unlock()
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// codexPart (SMOKE_CODEX=1; as in the Herdr worker smoke, not yet run on tmux): a Codex start
// in a repo Codex does not trust fails with the trust message and types nothing; then a worker
// in the trusted repo works end to end.
func (s *workerSmoke) codexPart() error {
	t := s.t
	saved := func(path string) bool {
		b, _ := os.ReadFile(s.codexConf)
		return strings.Contains(string(b), path)
	}
	if saved(s.untrusted) {
		return fmt.Errorf("the untrusted repo is already in the Codex config")
	}
	var mu sync.Mutex
	seen := ""
	wctx, stop := context.WithCancel(context.Background())
	go s.watchTrust(wctx, &seen, &mu)
	s.mu.Lock()
	before := len(s.starts)
	s.mu.Unlock()
	id, _, err := s.job("In the repository untrusted, have a codex worker (kind codex) create a file CODEX.txt containing the single word hi. If start_worker fails, do not retry and do not try another kind: end the job as failed and tell me the reason word for word.")
	stop()
	if err != nil {
		return err
	}
	s.mu.Lock()
	replies := append([]startReply(nil), s.starts[before:]...)
	s.mu.Unlock()
	mu.Lock()
	info(t, "codex (untrusted repo): the trust screen showed", map[bool]string{true: "yes (" + seen + ")", false: "not seen"}[seen != ""])
	mu.Unlock()
	refused, anyOK := false, false
	for _, r := range replies {
		refused = refused || strings.HasPrefix(r.err, "Codex asks to trust this repo")
		anyOK = anyOK || r.err == ""
	}
	if !refused || anyOK {
		s.dump()
		return fmt.Errorf("%s: a codex start in the untrusted repo did not fail on trust: %+v", id, replies)
	}
	if saved(s.untrusted) {
		return fmt.Errorf("the untrusted repo's trust was saved: a key reached the trust screen")
	}
	ok(t, "codex start in an untrusted repo failed with the trust message; nothing typed (no trust saved)")

	seen = ""
	wctx, stop = context.WithCancel(context.Background())
	go s.watchTrust(wctx, &seen, &mu)
	id, view, err := s.job("In the repository demo, have a codex worker (kind codex) create a file CODEX.txt containing the single word hi and commit it on its branch. Wait for its report, then tell me what it did.")
	stop()
	if err != nil {
		return err
	}
	worker := ""
	for _, w := range view.Workers {
		if w.Kind == agent.Codex {
			worker = w.Name
		}
	}
	if worker == "" {
		s.dump()
		return fmt.Errorf("%s closed without a codex worker (status %v)", id, view.Status)
	}
	slug := strings.TrimPrefix(worker, "demo-")
	committed := s.committed(slug, "CODEX.txt")
	if view.Status != agent.JobDone {
		s.dump()
		return fmt.Errorf("%s ended %v (last progress: %v)", id, view.Status, view.LastProgress)
	}
	if committed != nil {
		return committed
	}
	if !s.gone(worker, 20*time.Second) {
		return fmt.Errorf("%s's pane is still open after its job ended", worker)
	}
	ok(t, fmt.Sprintf("codex worker %s: CODEX.txt committed on mascot/%s; stopped with its job", worker, slug))
	return nil
}
