package tmux

// The host against a real tmux (whatever `tmux` is on PATH; skipped, not failed, without one),
// each test on its own throwaway server: a private TMUX_TMPDIR, so the socket can never be the
// user's, killed at the end. The agents are the fake agent in fakeagent_test.go, started as
// `claude` or `codex` from wrapper scripts first on the server's PATH.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/agent/proc"
)

type realRig struct {
	t      *testing.T
	bin    string
	socket string
	env    []agent.EnvVar
	run    ExecRunner
	report string // the fake agents' report file
	dir    string // a scratch folder (agent cwds live here)
}

var realSeq atomic.Int32

// newRealRig starts nothing: it finds tmux, writes the wrapper scripts and picks a socket. The
// server any host starts on it is killed at the end.
func newRealRig(t *testing.T) *realRig {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	// Short and private: socket paths are limited to ~100 bytes.
	tmp, err := os.MkdirTemp("", "dpx")
	if err != nil {
		t.Fatal(err)
	}
	tmp, _ = filepath.EvalSymlinks(tmp)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrappers := filepath.Join(tmp, "bin")
	if err := os.Mkdir(wrappers, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude", "codex"} {
		script := fmt.Sprintf("#!/bin/sh\nexec env %s=%s %q \"$@\"\n", fakeAgentEnv, kind, self)
		if err := os.WriteFile(filepath.Join(wrappers, kind), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(tmp, "work")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("dpx-test-%d-%d", os.Getpid(), realSeq.Add(1))
	var parent []agent.EnvVar
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH":
			v = wrappers + ":" + v
		case "TMUX_TMPDIR":
			continue
		}
		parent = append(parent, agent.EnvVar{Key: k, Value: v})
	}
	parent = append(parent, agent.EnvVar{Key: "TMUX_TMPDIR", Value: tmp})
	env, err := ServerEnv(parent, socket, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &realRig{t: t, bin: bin, socket: socket, env: env, report: filepath.Join(tmp, "report.jsonl"), dir: dir,
		run: ExecRunner{Bin: bin, Env: env, Exec: proc.ProcessRunner{}}}
	t.Cleanup(func() { _, _ = r.raw("kill-server") })
	return r
}

// host is a new Host on the rig's server, with short polls.
func (r *realRig) host() *Host {
	h := NewHost(r.bin, r.socket, r.dir, r.run)
	h.SetTiming(15*time.Second, 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)
	r.t.Cleanup(h.Shutdown)
	return h
}

// raw runs one tmux command on the rig's server.
func (r *realRig) raw(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := r.run.Run(ctx, append([]string{"-L", r.socket}, args...)...)
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		return out.Stdout, fmt.Errorf("tmux %s: %s", args[0], strings.TrimSpace(out.Stderr))
	}
	return out.Stdout, nil
}

func (r *realRig) spec(name string, kind agent.AgentKind, args ...string) agent.AgentSpec {
	return agent.AgentSpec{Name: name, Kind: kind, Cwd: r.dir, Args: args,
		Env: []agent.EnvVar{{Key: "FAKE_AGENT_REPORT", Value: r.report}}}
}

// reports is every line the fake agents wrote so far.
func (r *realRig) reports() []map[string]any {
	f, err := os.Open(r.report)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (r *realRig) prompts() []string {
	var out []string
	for _, m := range r.reports() {
		if p, ok := m["prompt"].(string); ok {
			out = append(out, p)
		}
	}
	return out
}

// waitStatus waits for agentName's status event on rx (other events are skipped).
func waitStatus(t *testing.T, rx <-chan agent.PaneEvent, agentName string, want agent.PaneStatus) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-rx:
			if ev.Agent == agentName && ev.Kind == agent.PaneEventStatus && ev.Status == want {
				return
			}
			if ev.Agent == agentName && ev.Kind == agent.PaneEventExited {
				t.Fatalf("%s exited waiting for %v", agentName, want)
			}
		case <-deadline:
			t.Fatalf("%s never %v", agentName, want)
		}
	}
}

// waitExited waits for agentName's Exited on rx, then checks no second one comes.
func waitExitedOnce(t *testing.T, rx <-chan agent.PaneEvent, agentName string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-rx:
			if ev.Agent == agentName && ev.Kind == agent.PaneEventExited {
				settle := time.After(time.Second)
				for {
					select {
					case ev := <-rx:
						if ev.Agent == agentName {
							t.Fatalf("after Exited: %+v", ev)
						}
					case <-settle:
						return
					}
				}
			}
		case <-deadline:
			t.Fatalf("%s never exited", agentName)
		}
	}
}

func listed(t *testing.T, h *Host) map[string]agent.AgentInfo {
	t.Helper()
	list, err := h.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]agent.AgentInfo{}
	for _, a := range list {
		out[a.Name] = a
	}
	return out
}

func TestRealTmuxPassesTheConformanceSuite(t *testing.T) {
	for _, kind := range []agent.AgentKind{agent.Codex, agent.Claude} {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRealRig(t)
			h := r.host()
			steps, err := agenttest.RunConformance(bg, h, r.spec("probe", kind), agenttest.Timeouts{Turn: 10 * time.Second, Settle: time.Second})
			if err != nil {
				screen, _ := r.raw("capture-pane", "-p", "-t", "="+r.socket+":probe")
				t.Fatalf("%v (passed: %q)\n--- probe ---\n%s", err, steps, screen)
			}
			if len(steps) != 7 {
				t.Fatalf("%q", steps)
			}
		})
	}
}

func TestRealTmuxVersionIsSupported(t *testing.T) {
	r := newRealRig(t)
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	v, err := r.run.Run(ctx, "-V")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tmux: %s", strings.TrimSpace(v.Stdout))
	if err := r.host().EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
}

func TestRealTmuxArgumentsArriveUnchanged(t *testing.T) {
	r := newRealRig(t)
	h := r.host()
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	// What tmux's argument reading does to them, through a paste buffer.
	for _, s := range []string{"plain", "a;b", "end;", ";", `x\;`, `x\\;`, "x;;", "#H #{pane_id} ##", "-x", "{ a }", "%if 1", "é;"} {
		if _, err := r.raw("set-buffer", "-b", "probe", "--", literalArg(s)); err != nil {
			t.Fatal(s, err)
		}
		got, err := r.raw("show-buffer", "-b", "probe")
		if err != nil || got != s {
			t.Errorf("%q came back %q (%v)", s, got, err)
		}
	}
	// A folder, arguments and environment that need it, and a label holding formats.
	odd := filepath.Join(r.dir, "odd #H dir;")
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := r.spec("odd", agent.Claude, "--append-system-prompt", "be brief;", "#{pane_id}", `x\;`)
	spec.Cwd = odd
	spec.Env = append(spec.Env, agent.EnvVar{Key: "FAKE_AGENT_VALUE", Value: "v #H;"})
	spec.Tab = agent.TabLabel{Label: "odd · fix #12 #H;", Title: "title #{pane_id};"}
	info, err := h.StartAgent(bg, spec)
	if err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	for _, m := range r.reports() {
		if _, ok := m["args"]; ok {
			start = m
		}
	}
	args, _ := json.Marshal(start["args"])
	if string(args) != `["--append-system-prompt","be brief;","#{pane_id}","x\\;"]` || start["cwd"] != odd || start["env"] != "v #H;" {
		t.Fatalf("the agent got %s in %v with %v", args, start["cwd"], start["env"])
	}
	if info.Cwd != odd || listed(t, h)["odd"].Cwd != odd {
		t.Fatalf("cwd %q / %+v", info.Cwd, listed(t, h)["odd"])
	}
	got, err := r.raw("display-message", "-p", "-t", info.Pane, "#{window_name}|#{pane_title}|#{@deskpixie_label}|#{@deskpixie_title}|#{@deskpixie_kind}")
	if err != nil || strings.TrimSpace(got) != "odd|odd · fix #12 #H;|odd · fix #12 #H;|title #{pane_id};|claude" {
		t.Fatalf("%q %v", got, err)
	}
	if err := h.Prompt(bg, "odd", "one line; #{pane_id} $HOME `x` \\;"); err != nil {
		t.Fatal(err)
	}
	waitPrompts(t, r, 1)
	if p := r.prompts(); p[0] != "one line; #{pane_id} $HOME `x` \\;" {
		t.Fatalf("%q", p[0])
	}
}

// waitPrompts waits until the fake agents have received n prompts.
func waitPrompts(t *testing.T, r *realRig, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(r.prompts()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d prompts arrived, want %d: %q", len(r.prompts()), n, r.prompts())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRealTmuxAPromptOfSeveralLinesArrivesAsOne(t *testing.T) {
	r := newRealRig(t)
	h := r.host()
	rx := h.Subscribe()
	if _, err := h.StartAgent(bg, r.spec("coordinator", agent.Claude)); err != nil {
		t.Fatal(err)
	}
	brief := "New request from the user: what is two plus two?\n[ref job_00000000beef]\nthird line; #{pane_id}"
	if err := h.Prompt(bg, "coordinator", brief); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, rx, "coordinator", agent.PaneWorking)
	waitStatus(t, rx, "coordinator", agent.PaneIdle)
	if p := r.prompts(); len(p) != 1 || p[0] != brief {
		t.Fatalf("%q", p)
	}
	screen, err := h.Read(bg, "coordinator", 40)
	if err != nil || !strings.Contains(screen, "› New request from the user") || !strings.Contains(screen, "  [ref job_00000000beef]") ||
		!strings.Contains(screen, "(turn: 3 line(s))") {
		t.Fatalf("%v\n%s", err, screen)
	}
	// Long briefs: several set-buffer chunks, one prompt; CRLF is a line break.
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "line %03d of the brief: ünïcode; and #{formats}\r\n", i)
	}
	long := b.String() + "end"
	if err := h.Prompt(bg, "coordinator", long); err != nil {
		t.Fatal(err)
	}
	waitPrompts(t, r, 2)
	if p := r.prompts(); p[1] != strings.ReplaceAll(long, "\r\n", "\n") {
		t.Fatalf("the long brief arrived as %d bytes", len(p[1]))
	}
	if bufs, _ := r.raw("list-buffers"); strings.TrimSpace(bufs) != "" {
		t.Fatalf("buffers left: %q", bufs)
	}
}

func TestRealTmuxADeadPaneIsExitedOnceAndAFailedStartLeavesNothing(t *testing.T) {
	r := newRealRig(t)
	h := r.host()
	rx := h.Subscribe()
	if _, err := h.StartAgent(bg, r.spec("keeper", agent.Codex)); err != nil {
		t.Fatal(err)
	}
	info, err := h.StartAgent(bg, r.spec("w-a", agent.Claude))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Prompt(bg, "w-a", "/exit"); err != nil {
		t.Fatal(err)
	}
	waitExitedOnce(t, rx, "w-a")
	if _, ok := listed(t, h)["w-a"]; ok {
		t.Fatal("an exited agent is listed")
	}
	// remain-on-exit kept the pane; its last screen can still be read.
	dead, err := r.raw("display-message", "-p", "-t", info.Pane, "#{pane_dead}")
	if err != nil || strings.TrimSpace(dead) != "1" {
		t.Fatalf("pane_dead %q %v", dead, err)
	}
	screen, err := h.Read(bg, "w-a", 40)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a dead pane reads:\n%s", screen)
	if err := h.Prompt(bg, "w-a", "hi"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatal(err)
	}
	// A new agent of the same name replaces the dead pane, without another Exited.
	if _, err := h.StartAgent(bg, r.spec("w-a", agent.Claude)); err != nil {
		t.Fatal(err)
	}
	panes, _ := r.raw("list-panes", "-s", "-t", "="+r.socket, "-F", "#{window_name} #{pane_id}")
	if strings.Contains(panes, info.Pane+"\n") || strings.Count(panes, "w-a ") != 1 {
		t.Fatalf("%q", panes)
	}
	// An agent that exits while starting: ExitedAtStart, and its window is gone.
	_, err = h.StartAgent(bg, r.spec("w-b", agent.Claude, "--exit-at-start"))
	if !agent.IsHostError(err, agent.HostExitedAtStart) {
		t.Fatal(err)
	}
	if windows, _ := r.raw("list-windows", "-t", "="+r.socket, "-F", "#{window_name}"); strings.Contains(windows, "w-b") {
		t.Fatalf("%q", windows)
	}
	select {
	case ev := <-rx:
		if ev.Agent == "w-b" || (ev.Agent == "w-a" && ev.Kind == agent.PaneEventExited) {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(time.Second):
	}
	// A pane killed from outside is Exited once too.
	if _, err := r.raw("kill-pane", "-t", listed(t, h)["keeper"].Pane); err != nil {
		t.Fatal(err)
	}
	waitExitedOnce(t, rx, "keeper")
}

func TestRealTmuxAgentsAreAdoptedByANewHostOnTheSameServer(t *testing.T) {
	r := newRealRig(t)
	first := r.host()
	coord, err := first.StartAgent(bg, r.spec("coordinator", agent.Claude))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := first.StartAgent(bg, r.spec("w-x", agent.Codex))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Prompt(bg, "w-x", "Count from 1 to 300, slowly."); err != nil {
		t.Fatal(err)
	}
	waitPrompts(t, r, 1)
	time.Sleep(200 * time.Millisecond)
	first.Shutdown() // the app quits; the server and the agents stay

	second := r.host()
	rx := second.Subscribe()
	got := listed(t, second)
	if len(got) != 2 || got["coordinator"].Pane != coord.Pane || got["coordinator"].Status != agent.PaneIdle ||
		got["w-x"].Pane != worker.Pane || got["w-x"].Status != agent.PaneWorking || got["w-x"].Cwd != r.dir {
		t.Fatalf("%+v", got)
	}
	// The adopted Codex worker's screen is watched (its kind came from @deskpixie_kind).
	if err := second.SendKeys(bg, "w-x", []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, rx, "w-x", agent.PaneIdle)
	// The adopted Claude agent: from its screen until a hook reports, then from hooks.
	if err := second.Prompt(bg, "coordinator", "Write the numbers 1 to 80."); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, rx, "coordinator", agent.PaneWorking)
	waitStatus(t, rx, "coordinator", agent.PaneIdle)
	second.ReportStatus("coordinator", agent.PaneBlocked)
	waitStatus(t, rx, "coordinator", agent.PaneBlocked)
	if got := listed(t, second)["coordinator"].Status; got != agent.PaneBlocked {
		t.Fatal(got)
	}
	// And its exit.
	if err := second.Prompt(bg, "coordinator", "/exit"); err != nil {
		t.Fatal(err)
	}
	waitExitedOnce(t, rx, "coordinator")
	// A start under the second host refuses the live name, replaces it with ReplaceAgent.
	if _, err := second.StartAgent(bg, r.spec("w-x", agent.Codex)); err == nil {
		t.Fatal("started over a live agent")
	}
	if _, err := second.ReplaceAgent(bg, r.spec("w-x", agent.Codex)); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, second); got["w-x"].Pane == worker.Pane {
		t.Fatalf("%+v", got)
	}
}

func TestRealTmuxPlaceholderLabelsFocusAndAttach(t *testing.T) {
	r := newRealRig(t)
	h := r.host()
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	windows := func() string {
		out, _ := r.raw("list-windows", "-t", "="+r.socket, "-F", "#{window_active} #{window_name}")
		return strings.TrimSpace(out)
	}
	if w := windows(); w != "1 "+placeholder {
		t.Fatalf("a new session: %q", w)
	}
	opts, err := r.raw("show-options", "-gw")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"remain-on-exit on", "automatic-rename off", "allow-rename off"} {
		if !strings.Contains(opts, want+"\n") {
			t.Fatalf("missing %q in\n%s", want, opts)
		}
	}
	a, err := h.StartAgent(bg, r.spec("a", agent.Claude))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.StartAgent(bg, r.spec("b", agent.Codex)); err != nil {
		t.Fatal(err)
	}
	if w := windows(); strings.Contains(w, placeholder) {
		t.Fatalf("the placeholder stayed: %q", w)
	}
	if got := listed(t, h); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	// Labels: in the pane title and the window's options; the window keeps the agent's name,
	// and the agent's own title (it sets one) does not replace the label on tmux 3.4+.
	if err := h.Relabel(bg, "a", agent.TabLabel{Label: "a · job_1 · fix", Title: "fix the bug"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.raw("display-message", "-p", "-t", a.Pane, "#{window_name}|#{pane_title}|#{@deskpixie_label}|#{@deskpixie_title}")
	if strings.TrimSpace(got) != "a|a · job_1 · fix|a · job_1 · fix|fix the bug" {
		t.Fatalf("%q", got)
	}
	if err := h.Focus(bg, "b"); err != nil {
		t.Fatal(err)
	}
	if w := windows(); !strings.Contains(w, "1 b") || !strings.Contains(w, "0 a") {
		t.Fatalf("focus b: %q", w)
	}
	if err := h.Focus(bg, "a"); err != nil {
		t.Fatal(err)
	}
	if w := windows(); !strings.Contains(w, "1 a") {
		t.Fatalf("focus a: %q", w)
	}
	if !slices.Equal(h.AttachCommand(), []string{r.bin, "-L", r.socket, "attach", "-t", r.socket}) {
		t.Fatal(h.AttachCommand())
	}
	// Closing the last agents ends the session; the next start brings it back.
	for _, name := range []string{"a", "b"} {
		if err := h.Close(bg, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.raw("has-session", "-t", "="+r.socket); err == nil {
		t.Fatal("the session outlived its last agent")
	}
	if got := listed(t, h); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if _, err := h.StartAgent(bg, r.spec("c", agent.Claude)); err != nil {
		t.Fatal(err)
	}
	if w := windows(); w != "0 c" && w != "1 c" {
		t.Fatalf("after a restart: %q", w)
	}
}

func TestRealTmuxEscapeEndsAHookedClaudeTurn(t *testing.T) {
	r := newRealRig(t)
	h := r.host()
	rx := h.Subscribe()
	if _, err := h.StartAgent(bg, r.spec("coordinator", agent.Claude)); err != nil {
		t.Fatal(err)
	}
	if err := h.Prompt(bg, "coordinator", "Count from 1 to 300, slowly."); err != nil {
		t.Fatal(err)
	}
	h.ReportStatus("coordinator", agent.PaneWorking) // its UserPromptSubmit hook
	waitStatus(t, rx, "coordinator", agent.PaneWorking)
	// Claude Code fires no Stop hook for an interrupted turn: the screen ends it.
	if err := h.SendKeys(bg, "coordinator", []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, rx, "coordinator", agent.PaneIdle)
}
