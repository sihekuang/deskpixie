package tmux

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/agent/proc"
)

var bg = context.Background()

// rig is a Host on a FakeServer whose agents come up at once (Claude's prompt, or Codex's
// empty input box), with short polls.
type rig struct {
	h   *Host
	srv *FakeServer
	run *FakeRunner
}

func newRig(t *testing.T) rig {
	t.Helper()
	srv := NewFakeServer()
	srv.OnStart = func(p *FakePane) {
		if len(p.Argv) > 0 && p.Argv[0] == "codex" {
			p.Screen = codexIdle
		} else {
			p.Screen = claudeIdle
		}
	}
	run := srv.Runner()
	h := NewHost("/opt/homebrew/bin/tmux", "deskpixie", "/Users/me", run)
	h.SetTiming(2*time.Second, 5*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	t.Cleanup(h.Shutdown)
	return rig{h: h, srv: srv, run: run}
}

func claudeSpec(name string) agent.AgentSpec {
	return agent.AgentSpec{Name: name, Kind: agent.Claude, Cwd: "/data/agent/workspace",
		Args: []string{"--permission-mode", "auto", "--settings", "/data/agent/coordinator-settings.json"},
		Env:  []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "tok"}}}
}

func codexSpec(name string) agent.AgentSpec {
	return agent.AgentSpec{Name: name, Kind: agent.Codex, Cwd: "/p/repo/.worktrees/x", Args: []string{"-s", "workspace-write"},
		Env: []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "w"}}}
}

// next waits for the next event on rx.
func next(t *testing.T, rx <-chan agent.PaneEvent) agent.PaneEvent {
	t.Helper()
	select {
	case ev := <-rx:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
		return agent.PaneEvent{}
	}
}

// quiet: nothing arrives on rx for a while.
func quiet(t *testing.T, rx <-chan agent.PaneEvent) {
	t.Helper()
	select {
	case ev := <-rx:
		t.Fatalf("unexpected %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEnsureSessionChecksTheVersionOnceAndStartsAPrivateServer(t *testing.T) {
	r := newRig(t)
	if err := r.h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	if err := r.h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	calls := r.run.Calls()
	want := [][]string{
		{"-V"},
		{"-L", "deskpixie", "has-session", "-t", "=deskpixie"},
		{"-L", "deskpixie", "new-session", "-d", "-s", "deskpixie", "-n", "_deskpixie", "-c", "/Users/me",
			";", "set-option", "-gw", "remain-on-exit", "on",
			";", "set-option", "-gw", "automatic-rename", "off",
			";", "set-option", "-gw", "allow-rename", "off"},
		{"-L", "deskpixie", "set-option", "-gw", "allow-set-title", "off"},
		{"-L", "deskpixie", "has-session", "-t", "=deskpixie"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("%q", calls)
	}
	if r.srv.Option("remain-on-exit") != "on" || r.srv.Option("automatic-rename") != "off" {
		t.Fatal("options")
	}
	// Every call but -V names our socket: the user's own tmux is never touched.
	for _, c := range calls[1:] {
		if c[0] != "-L" || c[1] != "deskpixie" {
			t.Fatal(c)
		}
	}
}

func TestAnOldTmuxIsAVersionMismatchNamingTheSupportedVersion(t *testing.T) {
	r := newRig(t)
	r.srv.Version = "tmux 3.1c"
	err := r.h.EnsureSession(bg)
	if !agent.IsHostError(err, agent.HostVersionMismatch) {
		t.Fatal(err)
	}
	if err.Error() != "tmux version mismatch: you have tmux 3.1c; DeskPixie supports tmux 3.2 or newer" {
		t.Fatal(err.Error())
	}
	if len(r.run.Calls()) != 1 || r.srv.Running() {
		t.Fatal("nothing started")
	}
	if _, err := r.h.List(bg); !agent.IsHostError(err, agent.HostVersionMismatch) {
		t.Fatal(err)
	}
}

func TestAMissingTmuxIsNotFound(t *testing.T) {
	run := ExecRunner{Bin: "/definitely/not/here/tmux", Exec: proc.ProcessRunner{}}
	h := NewHost(run.Bin, "deskpixie", "/Users/me", run)
	defer h.Shutdown()
	err := h.EnsureSession(bg)
	if !agent.IsHostError(err, agent.HostNotFound) || err.Error() != "tmux not found" {
		t.Fatal(err)
	}
	if _, err := h.StartAgent(bg, claudeSpec("coordinator")); !agent.IsHostError(err, agent.HostNotFound) {
		t.Fatal(err)
	}
}

func TestStartAgentOpensANamedWindowRunningTheAgentDirectly(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	spec := claudeSpec("coordinator")
	spec.Tab = agent.TabLabel{Label: "coordinator · fix it", Title: "fix it"}
	info, err := r.h.StartAgent(bg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if info != (agent.AgentInfo{Name: "coordinator", Pane: "%1", Cwd: "/data/agent/workspace", Status: agent.PaneIdle}) {
		t.Fatalf("%+v", info)
	}
	nw := r.run.CallsOf("new-window")
	want := []string{"-L", "deskpixie", "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=deskpixie:", "-n", "coordinator",
		"-c", "/data/agent/workspace", "-e", "PIXIE_AGENT_TOKEN=tok",
		"--", "claude", "--permission-mode", "auto", "--settings", "/data/agent/coordinator-settings.json"}
	if len(nw) != 1 || !reflect.DeepEqual(nw[0], want) {
		t.Fatalf("%q", nw)
	}
	p, _ := r.srv.Pane("%1")
	if p.Options["@deskpixie_kind"] != "claude" || p.Options["@deskpixie_label"] != "coordinator · fix it" ||
		p.Options["@deskpixie_title"] != "fix it" || p.Title != "coordinator · fix it" {
		t.Fatalf("%+v", p)
	}
	// The placeholder window is gone once an agent's window is up.
	if got := r.srv.Windows(); !reflect.DeepEqual(got, []string{"coordinator"}) {
		t.Fatal(got)
	}
	if ev := next(t, rx); ev != status("coordinator", agent.PaneIdle) {
		t.Fatalf("%+v", ev)
	}
	// A second agent of the same name is refused.
	if _, err := r.h.StartAgent(bg, spec); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
}

func TestAClaudeTrustDialogCountsAsStarted(t *testing.T) {
	r := newRig(t)
	r.srv.OnStart = func(p *FakePane) { p.Screen = claudeTrust2 }
	info, err := r.h.StartAgent(bg, claudeSpec("w-a"))
	if err != nil || info.Status != agent.PaneBlocked {
		t.Fatal(info, err)
	}
}

func TestCodexIsStartedAtItsReadyMarker(t *testing.T) {
	r := newRig(t)
	r.srv.OnStart = func(p *FakePane) { p.Screen = "Booting MCP server: pixie" }
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.srv.SetScreen("%1", codexIdle)
	}()
	info, err := r.h.StartAgent(bg, codexSpec("w-codex"))
	if err != nil || info.Status != agent.PaneIdle {
		t.Fatal(info, err)
	}
	if len(r.run.CallsOf("capture-pane")) < 2 {
		t.Fatal("polled the screen until ready")
	}
}

func TestAnAgentThatExitsWhileStartingIsExitedAtStart(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	r.srv.OnStart = func(p *FakePane) { p.Dead = true; p.Screen = "No conversation found to continue" }
	_, err := r.h.StartAgent(bg, claudeSpec("coordinator"))
	if !agent.IsHostError(err, agent.HostExitedAtStart) || err.Error() != "claude exited while starting" {
		t.Fatal(err)
	}
	if _, ok := r.srv.PaneNamed("coordinator"); ok {
		t.Fatal("its window is closed")
	}
	quiet(t, rx) // a start that failed is not an exit
}

func TestAStartThatNeverShowsThePromptTimesOutAndClosesItsWindow(t *testing.T) {
	r := newRig(t)
	r.h.SetTiming(100*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	r.srv.OnStart = func(p *FakePane) { p.Screen = "loading" }
	_, err := r.h.StartAgent(bg, claudeSpec("coordinator"))
	if !agent.IsHostError(err, agent.HostTimeout) || err.Error() != "timed out: claude did not start" {
		t.Fatal(err)
	}
	if _, ok := r.srv.PaneNamed("coordinator"); ok {
		t.Fatal("its window is closed")
	}
	// An abandoned start (the runtime's limit) closes it too.
	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	r.h.SetTiming(time.Minute, 5*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	if _, err := r.h.StartAgent(ctx, claudeSpec("w-a")); err == nil {
		t.Fatal("want an error")
	}
	if _, ok := r.srv.PaneNamed("w-a"); ok {
		t.Fatal("its window is closed")
	}
}

func TestPromptTypesTheTextLiterallyThenPressesEnter(t *testing.T) {
	r := newRig(t)
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Prompt(bg, "coordinator", "New request: -t \"quoted\" ; $HOME"); err != nil {
		t.Fatal(err)
	}
	sk := r.run.CallsOf("send-keys")
	want := [][]string{
		{"-L", "deskpixie", "send-keys", "-t", "%1", "-l", "--", "New request: -t \"quoted\" ; $HOME"},
		{"-L", "deskpixie", "send-keys", "-t", "%1", "Enter"},
	}
	if !reflect.DeepEqual(sk, want) {
		t.Fatalf("%q", sk)
	}
	// A long brief goes in pieces, never splitting a character.
	long := strings.Repeat("é", sendChunk) // 2 bytes each
	if err := r.h.Prompt(bg, "coordinator", long); err != nil {
		t.Fatal(err)
	}
	p, _ := r.srv.Pane("%1")
	var typed strings.Builder
	pieces := 0
	for _, k := range p.Keys[2:] {
		if s, ok := strings.CutPrefix(k, "-l:"); ok {
			typed.WriteString(s)
			pieces++
		}
	}
	if typed.String() != long || pieces != 2 || p.Keys[len(p.Keys)-1] != "Enter" {
		t.Fatal(pieces, len(typed.String()), p.Keys[len(p.Keys)-1])
	}
	if err := r.h.SendKeys(bg, "coordinator", []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	if last := r.run.CallsOf("send-keys"); !reflect.DeepEqual(last[len(last)-1], []string{"-L", "deskpixie", "send-keys", "-t", "%1", "Escape"}) {
		t.Fatal(last[len(last)-1])
	}
}

func TestAMissingAgentIsNoAgentWithOrWithoutAServer(t *testing.T) {
	r := newRig(t)
	check := func(when string) {
		t.Helper()
		for what, err := range map[string]error{
			"prompt":  r.h.Prompt(bg, "nobody", "hi"),
			"keys":    r.h.SendKeys(bg, "nobody", []string{"Escape"}),
			"close":   r.h.Close(bg, "nobody"),
			"focus":   r.h.Focus(bg, "nobody"),
			"relabel": r.h.Relabel(bg, "nobody", agent.TabLabel{}),
			"read":    func() error { _, err := r.h.Read(bg, "nobody", 10); return err }(),
		} {
			if !agent.IsHostError(err, agent.HostNoAgent) {
				t.Fatalf("%s %s: %v", when, what, err)
			}
		}
		if err := r.h.CloseExact(bg, "nobody", "%9"); err != nil {
			t.Fatal(when, err)
		}
	}
	check("no server")
	if l, err := r.h.List(bg); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	if r.srv.Running() {
		t.Fatal("list starts no server")
	}
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	check("server")
}

func TestParseRowsReadsListPanesIncludingDeadPanes(t *testing.T) {
	out := "coordinator\t%1\t0\tclaude\t/data/agent/workspace\n" +
		"w-fix\t%4\t1\tcodex\t/p/repo/.worktrees/fix\n" +
		"_deskpixie\t%0\t0\t\t/Users/me\n" +
		"odd\t%7\t0\tclaude\t/tmp/with\ttab\n" +
		"garbage line\n\n"
	want := []Row{
		{Window: "coordinator", Pane: "%1", Kind: "claude", Cwd: "/data/agent/workspace"},
		{Window: "w-fix", Pane: "%4", Dead: true, Kind: "codex", Cwd: "/p/repo/.worktrees/fix"},
		{Window: "_deskpixie", Pane: "%0", Cwd: "/Users/me"},
		{Window: "odd", Pane: "%7", Kind: "claude", Cwd: "/tmp/with\ttab"},
	}
	if got := parseRows(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestListShowsLiveAgentsAndAdoptsThoseLeftByAnEarlierRun(t *testing.T) {
	r := newRig(t)
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.StartAgent(bg, codexSpec("w-codex")); err != nil {
		t.Fatal(err)
	}
	r.srv.SetScreen("%2", codexWorking)
	r.srv.Kill("%1")
	// A fresh host (the app restarted) finds them by name.
	h2 := NewHost("/opt/homebrew/bin/tmux", "deskpixie", "/Users/me", r.run)
	defer h2.Shutdown()
	got, err := h2.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	want := []agent.AgentInfo{{Name: "w-codex", Pane: "%2", Cwd: "/p/repo/.worktrees/x", Status: agent.PaneWorking}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	lp := r.run.CallsOf("list-panes")
	if !reflect.DeepEqual(lp[len(lp)-1], []string{"-L", "deskpixie", "list-panes", "-s", "-t", "=deskpixie", "-F", listFormat}) {
		t.Fatal(lp[len(lp)-1])
	}
}

func TestReadIsTheLastLinesOfTheScreenWithoutTheBlankRowsBelow(t *testing.T) {
	r := newRig(t)
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	r.srv.SetScreen("%1", "one\ntwo\nthree\n❯ hi\n\n\n   \n")
	got, err := r.h.Read(bg, "coordinator", 2)
	if err != nil || got != "three\n❯ hi" {
		t.Fatalf("%q %v", got, err)
	}
	cp := r.run.CallsOf("capture-pane")
	if !reflect.DeepEqual(cp[len(cp)-1], []string{"-L", "deskpixie", "capture-pane", "-p", "-J", "-t", "%1", "-S", "-2"}) {
		t.Fatal(cp[len(cp)-1])
	}
	// An exited agent's last screen can still be read (remain-on-exit).
	r.srv.Kill("%1")
	if got, err := r.h.Read(bg, "coordinator", 1); err != nil || got != "❯ hi" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestFocusRelabelAndAttach(t *testing.T) {
	r := newRig(t)
	if _, err := r.h.StartAgent(bg, claudeSpec("w-a")); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Focus(bg, "w-a"); err != nil || r.srv.Selected() != "%1" {
		t.Fatal(err, r.srv.Selected())
	}
	if err := r.h.Relabel(bg, "w-a", agent.TabLabel{Label: "w-a · job_1 · fix", Title: "fix the bug"}); err != nil {
		t.Fatal(err)
	}
	p, _ := r.srv.Pane("%1")
	if p.Window != "w-a" || p.Title != "w-a · job_1 · fix" || p.Options["@deskpixie_label"] != "w-a · job_1 · fix" || p.Options["@deskpixie_title"] != "fix the bug" {
		t.Fatalf("%+v", p)
	}
	if err := r.h.Relabel(bg, "w-a", agent.TabLabel{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.srv.Pane("%1"); p.Title != "w-a" || p.Options["@deskpixie_title"] != "" {
		t.Fatalf("%+v", p)
	}
	if got := r.h.AttachCommand(); !reflect.DeepEqual(got, []string{"/opt/homebrew/bin/tmux", "-L", "deskpixie", "attach", "-t", "deskpixie"}) {
		t.Fatal(got)
	}
	if r.h.Name() != "tmux" {
		t.Fatal(r.h.Name())
	}
}

func TestExitsAreReportedOnceAndNotReplayedToNewSubscribers(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	if _, err := r.h.StartAgent(bg, claudeSpec("w-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.StartAgent(bg, claudeSpec("w-b")); err != nil {
		t.Fatal(err)
	}
	next(t, rx)
	next(t, rx)
	r.srv.Kill("%1") // its process ended; remain-on-exit keeps the pane
	if ev := next(t, rx); ev != exited("w-a") {
		t.Fatalf("%+v", ev)
	}
	r.srv.Vanish("%2") // closed from outside
	if ev := next(t, rx); ev != exited("w-b") {
		t.Fatalf("%+v", ev)
	}
	fresh := r.h.Subscribe()
	quiet(t, rx)
	quiet(t, fresh)
	if l, _ := r.h.List(bg); len(l) != 0 {
		t.Fatal(l)
	}
	// Closing the dead pane reports nothing more.
	if err := r.h.Close(bg, "w-a"); err != nil {
		t.Fatal(err)
	}
	quiet(t, rx)
}

func TestCloseReportsTheExitOnceAndCloseExactLeavesANewerAgentAlone(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	info, err := r.h.StartAgent(bg, claudeSpec("w-a"))
	if err != nil {
		t.Fatal(err)
	}
	next(t, rx)
	if err := r.h.CloseExact(bg, "w-a", "%99"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.srv.Pane(info.Pane); !ok {
		t.Fatal("not its pane: left alone")
	}
	if err := r.h.CloseExact(bg, "w-a", info.Pane); err != nil {
		t.Fatal(err)
	}
	if ev := next(t, rx); ev != exited("w-a") {
		t.Fatalf("%+v", ev)
	}
	quiet(t, rx)
	if err := r.h.Close(bg, "w-a"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatal(err)
	}
}

func TestReplaceAgentClosesTheOldPaneWithoutAnExit(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	next(t, rx)
	info, err := r.h.ReplaceAgent(bg, claudeSpec("coordinator"))
	if err != nil || info.Pane != "%2" {
		t.Fatal(info, err)
	}
	if _, ok := r.srv.Pane("%1"); ok {
		t.Fatal("old pane closed")
	}
	if ev := next(t, rx); ev != status("coordinator", agent.PaneIdle) {
		t.Fatalf("its name lives on: %+v", ev)
	}
	quiet(t, rx)
}

func TestStartingOverAnExitedAgentReportsItsExitFirst(t *testing.T) {
	r := newRig(t)
	r.h.SetTiming(2*time.Second, 5*time.Millisecond, time.Hour, time.Hour) // no polls
	rx := r.h.Subscribe()
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	next(t, rx)
	r.srv.Kill("%1")
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	if ev := next(t, rx); ev != exited("coordinator") {
		t.Fatalf("%+v", ev)
	}
	if ev := next(t, rx); ev != status("coordinator", agent.PaneIdle) {
		t.Fatalf("%+v", ev)
	}
	if got := r.srv.Windows(); !reflect.DeepEqual(got, []string{"coordinator"}) {
		t.Fatal(got)
	}
}

func TestStatusComesFromHooksForClaudeAndTheScreenForCodex(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.StartAgent(bg, codexSpec("w-codex")); err != nil {
		t.Fatal(err)
	}
	next(t, rx)
	next(t, rx)
	r.h.ReportStatus("coordinator", agent.PaneWorking)
	if ev := next(t, rx); ev != status("coordinator", agent.PaneWorking) {
		t.Fatalf("%+v", ev)
	}
	// Its screen no longer counts once it reported.
	r.srv.SetScreen("%1", claudeIdle)
	r.srv.SetScreen("%2", codexWorking)
	if ev := next(t, rx); ev != status("w-codex", agent.PaneWorking) {
		t.Fatalf("%+v", ev)
	}
	r.srv.SetScreen("%2", codexMcp)
	if ev := next(t, rx); ev != status("w-codex", agent.PaneBlocked) {
		t.Fatalf("%+v", ev)
	}
	quiet(t, rx)
	if l, _ := r.h.List(bg); len(l) != 2 || l[0].Status != agent.PaneWorking || l[1].Status != agent.PaneBlocked {
		t.Fatalf("%+v", l)
	}
}

func TestShutdownClosesTheSubscriptions(t *testing.T) {
	r := newRig(t)
	rx := r.h.Subscribe()
	r.h.Shutdown()
	r.h.Shutdown()
	select {
	case _, ok := <-rx:
		if ok {
			t.Fatal("event")
		}
	case <-time.After(time.Second):
		t.Fatal("not closed")
	}
}

// fakeCodex answers prompts like a Codex pane: the typed text shows, Enter makes it work for a
// moment (until Escape, for a prompt that says "slowly"), then it is idle again.
func fakeCodex() func(s *FakeServer, pane string, keys []string) {
	var mu sync.Mutex
	typed := map[string]string{}
	return func(s *FakeServer, pane string, keys []string) {
		mu.Lock()
		defer mu.Unlock()
		for _, k := range keys {
			switch {
			case strings.HasPrefix(k, "-l:"):
				typed[pane] += strings.TrimPrefix(k, "-l:")
			case k == "Enter":
				text := typed[pane]
				typed[pane] = ""
				s.SetScreen(pane, "› "+text+"\n\n• Working (1s • esc to interrupt)\n\n› Ask Codex to do anything")
				if !strings.Contains(text, "slowly") {
					go func() {
						time.Sleep(150 * time.Millisecond)
						s.SetScreen(pane, "› "+text+"\n\nOK\n\n› Ask Codex to do anything\n\n  ? for shortcuts")
					}()
				}
			case k == "Escape":
				s.SetScreen(pane, "■ Interrupted\n\n› Ask Codex to do anything\n\n  ? for shortcuts")
			}
		}
	}
}

func TestTheTmuxHostPassesTheConformanceSuiteOnAFakeServer(t *testing.T) {
	r := newRig(t)
	r.srv.OnKeys = fakeCodex()
	steps, err := agenttest.RunConformance(bg, r.h, codexSpec("probe"), agenttest.Timeouts{Turn: 3 * time.Second, Settle: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("%v (passed: %q)", err, steps)
	}
	if len(steps) != 7 {
		t.Fatalf("%q", steps)
	}
}

// recordingExec is an agent.CommandRunner that records its one call.
type recordingExec struct {
	argv     []string
	env      []agent.EnvVar
	deadline time.Time
	out      agent.CommandOutput
	err      error
}

func (r *recordingExec) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	return r.RunBy(argv, env, time.Time{})
}

func (r *recordingExec) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	r.argv, r.env, r.deadline = argv, env, deadline
	return r.out, r.err
}

func (r *recordingExec) SpawnDetached([]string, []agent.EnvVar) error { return nil }

func TestExecRunnerRunsTmuxWithExactlyTheServerEnvironment(t *testing.T) {
	env := []agent.EnvVar{{Key: "PATH", Value: "/usr/bin"}}
	rec := &recordingExec{out: agent.CommandOutput{Status: 1, Stdout: "o", Stderr: "e"}}
	run := ExecRunner{Bin: "/opt/homebrew/bin/tmux", Env: env, Exec: rec}
	ctx, cancel := context.WithTimeout(bg, time.Minute)
	defer cancel()
	out, err := run.Run(ctx, "-L", "s", "has-session")
	if err != nil || out != (Output{Code: 1, Stdout: "o", Stderr: "e"}) {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(rec.argv, []string{"/opt/homebrew/bin/tmux", "-L", "s", "has-session"}) || !reflect.DeepEqual(rec.env, env) {
		t.Fatal(rec.argv, rec.env)
	}
	if d, _ := ctx.Deadline(); !rec.deadline.Equal(d) {
		t.Fatal(rec.deadline)
	}
	// Without a deadline, DefaultTimeout bounds it.
	if _, err := run.Run(bg, "-V"); err != nil || time.Until(rec.deadline) > DefaultTimeout || time.Until(rec.deadline) < DefaultTimeout-time.Second {
		t.Fatal(rec.deadline, err)
	}
	rec.err = errors.New("/opt/homebrew/bin/tmux: timed out after 10s")
	if _, err := run.Run(bg, "-V"); !agent.IsHostError(err, agent.HostTimeout) {
		t.Fatal(err)
	}
	rec.err = errors.New("permission denied")
	if _, err := run.Run(bg, "-V"); !agent.IsHostError(err, agent.HostOther) {
		t.Fatal(err)
	}
}

func TestServerEnvIsScrubbedAsForHerdrAndNotNested(t *testing.T) {
	parent := []agent.EnvVar{{Key: "ANTHROPIC_API_KEY", Value: "k"}, {Key: "TMUX", Value: "/tmp/x,1,0"}, {Key: "TMUX_PANE", Value: "%3"},
		{Key: "HERDR_SOCKET_PATH", Value: "/s"}, {Key: "LANG", Value: "en_US.UTF-8"}, {Key: "PATH", Value: "/usr/bin"}}
	env, err := ServerEnv(parent, "deskpixie", "/Users/me")
	if err != nil {
		t.Fatal(err)
	}
	want := []agent.EnvVar{{Key: "LANG", Value: "en_US.UTF-8"}, {Key: "PATH", Value: "/usr/bin:/Users/me/.local/bin:/opt/homebrew/bin:/usr/local/bin"}}
	if !reflect.DeepEqual(env, want) {
		t.Fatal(env)
	}
	for _, bad := range []string{"", "default", "-x", "Upper", strings.Repeat("a", 49)} {
		if _, err := ServerEnv(parent, bad, "/h"); !agent.IsHostError(err, agent.HostOther) {
			t.Fatal(bad, err)
		}
	}
}

func TestFindTmuxLooksInHomebrewAndUsrLocal(t *testing.T) {
	exists := func(p string) bool { return p == "/usr/local/bin/tmux" }
	if p, ok := FindTmux("", "/usr/bin:/bin", exists); !ok || p != "/usr/local/bin/tmux" {
		t.Fatal(p, ok)
	}
	if _, ok := FindTmux("/nope/tmux", "", exists); ok {
		t.Fatal("a configured path that isn't there is not found")
	}
}

func TestArgumentsEndingInASemicolonOrHoldingFormatsReachTmuxUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain", "a;b": "a;b", "end;": `end\;`, ";": `\;`, `x\;`: `x\\;`, "x;;": `x;\;`, "#H": "#H",
	} {
		if got := literalArg(in); got != want {
			t.Errorf("literalArg(%q) = %q, want %q", in, got, want)
		}
	}
	if got := formatArg("PR #67 #{pane_id};"); got != `PR ##67 ##{pane_id}\;` {
		t.Fatal(got)
	}
	r := newRig(t)
	spec := claudeSpec("coordinator")
	spec.Cwd = "/p/odd #H dir"
	spec.Args = append(spec.Args, "--append-system-prompt", "be brief;")
	spec.Env = append(spec.Env, agent.EnvVar{Key: "X", Value: "v;"})
	spec.Tab = agent.TabLabel{Label: "fix #12;", Title: "title;"}
	if _, err := r.h.StartAgent(bg, spec); err != nil {
		t.Fatal(err)
	}
	p, _ := r.srv.PaneNamed("coordinator")
	if p.Cwd != spec.Cwd || p.Argv[len(p.Argv)-1] != "be brief;" || p.Env[len(p.Env)-1] != "X=v;" ||
		p.Title != "fix #12;" || p.Options["@deskpixie_label"] != "fix #12;" || p.Options["@deskpixie_title"] != "title;" {
		t.Fatalf("%+v", p)
	}
	if err := r.h.Prompt(bg, "coordinator", "do it;"); err != nil {
		t.Fatal(err)
	}
	p, _ = r.srv.PaneNamed("coordinator")
	if !slices.Equal(p.Keys, []string{"-l:do it;", "Enter"}) {
		t.Fatalf("%q", p.Keys)
	}
}

func TestAPromptOfSeveralLinesIsPastedBracketedThenSubmitted(t *testing.T) {
	r := newRig(t)
	if _, err := r.h.StartAgent(bg, claudeSpec("coordinator")); err != nil {
		t.Fatal(err)
	}
	brief := "New request from the user: hi;\r\n[ref job_1];"
	if err := r.h.Prompt(bg, "coordinator", brief); err != nil {
		t.Fatal(err)
	}
	p, _ := r.srv.PaneNamed("coordinator")
	if !slices.Equal(p.Keys, []string{"bracketed-paste:New request from the user: hi;\n[ref job_1];", "Enter"}) {
		t.Fatalf("%q", p.Keys)
	}
	if r.srv.Buffers() != 0 {
		t.Fatal("the paste buffer was left behind")
	}
	// A long brief goes into the buffer in pieces.
	long := strings.Repeat("line é\n", sendChunk)
	before := len(r.run.CallsOf("set-buffer"))
	if err := r.h.Prompt(bg, "coordinator", long); err != nil {
		t.Fatal(err)
	}
	p, _ = r.srv.PaneNamed("coordinator")
	if p.Keys[2] != "bracketed-paste:"+long || p.Keys[3] != "Enter" {
		t.Fatal(len(p.Keys[2]))
	}
	sets := r.run.CallsOf("set-buffer")[before:]
	if len(sets) < 3 || slices.Contains(sets[0], "-a") || !slices.Contains(sets[1], "-a") {
		t.Fatalf("%d set-buffer calls", len(sets))
	}
}
