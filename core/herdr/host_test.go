package herdr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/herdr/herdrtest"
)

// Port of host.rs tests (40). Two are covered differently, each noted at its test:
//   - a_worker_start_fits_in_its_limit asserts relations with agent-core runtime budgets
//     (DIALOG_WAIT, ENSURE_BUDGET, …) that the Go agent runtime (P9) owns; this package exports
//     PaneBusyLimit and StartTimeout and P9 asserts the relations. Here only the relation with
//     the transport is checked (TestAgentStartWaitsLessThanTheTransportAllows) plus the
//     verbatim constant values.
//   - the conformance suite (agent-core conformance.rs, P9) is ported inline below as
//     runConformance, so the host passes the same seven steps today.
//
// The session is the app's default session, renamed ai-mascot → deskpixie (spec §6).

const hostBin = "/Users/x/.local/bin/herdr"
const hostSession = "deskpixie"

func newTestHost(t *testing.T, server *herdrtest.FakeHerdr) *Host {
	return hostWithIntegration(t, server, "claude: current (v7) (/x)")
}

func hostWithIntegration(t *testing.T, server *herdrtest.FakeHerdr, integration string) *Host {
	t.Helper()
	runner := newFakeRunner()
	runner.answer(hostBin+" status --json", runOK(server.StatusJSON(server.Protocol())))
	runner.answer(hostBin+" integration status", runOK(integration))
	sup, err := NewSupervisor(hostBin, pairs("HERDR_SESSION", hostSession), runner)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHost(hostSession, "/Users/x", sup.WithTiming(10*time.Millisecond, 100*time.Millisecond))
	t.Cleanup(h.Shutdown)
	return h
}

func spec(name string) agent.AgentSpec {
	return agent.AgentSpec{
		Name: name,
		Kind: agent.Claude,
		Cwd:  "/data/ws",
		Args: []string{"--model", "haiku"},
		Env:  []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "tok"}},
	}
}

// collect is everything the stream delivers within window.
func collect(events <-chan agent.PaneEvent, window time.Duration) []agent.PaneEvent {
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	var got []agent.PaneEvent
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline.C:
			return got
		}
	}
}

func count(got []agent.PaneEvent, want agent.PaneEvent) int {
	n := 0
	for _, e := range got {
		if e == want {
			n++
		}
	}
	return n
}

func mustStart(t *testing.T, h *Host, name string) agent.AgentInfo {
	t.Helper()
	info, err := h.StartAgent(bg, spec(name))
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return info
}

func listed2(t *testing.T, h *Host) []agent.AgentInfo {
	t.Helper()
	l, err := h.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func hasAgent(l []agent.AgentInfo, pred func(agent.AgentInfo) bool) bool {
	for _, a := range l {
		if pred(a) {
			return true
		}
	}
	return false
}

func named(n string) func(agent.AgentInfo) bool {
	return func(a agent.AgentInfo) bool { return a.Name == n }
}

// --- conformance (agent-core conformance.rs, ported inline; see the file comment) ---

func runConformance(host agent.SessionHost, s agent.AgentSpec, turn, settle time.Duration) ([]string, error) {
	ctx := context.Background()
	var steps []string
	name := s.Name
	if err := host.EnsureSession(ctx); err != nil {
		return nil, fmt.Errorf("ensure_session: %w", err)
	}
	if err := host.EnsureSession(ctx); err != nil {
		return nil, fmt.Errorf("ensure_session (again): %w", err)
	}
	steps = append(steps, "ensure_session is idempotent")

	events := host.Subscribe()
	if _, err := host.StartAgent(ctx, s); err != nil {
		return nil, fmt.Errorf("start_agent: %w", err)
	}
	if err := host.EnsureSession(ctx); err != nil {
		return nil, fmt.Errorf("ensure_session (after start): %w", err)
	}
	if l, err := host.List(ctx); err != nil || !hasAgent(l, named(name)) {
		return nil, fmt.Errorf("%s missing from list after start_agent / ensure_session: %v %v", name, l, err)
	}
	steps = append(steps, "start_agent → list shows it, and a later ensure_session keeps it")

	if err := host.Prompt(ctx, "no-such-agent", "hi"); !agent.IsHostError(err, agent.HostNoAgent) {
		return nil, fmt.Errorf("prompt to a missing agent should be NoAgent, got %v", err)
	}
	if err := host.Close(ctx, "no-such-agent"); !agent.IsHostError(err, agent.HostNoAgent) {
		return nil, fmt.Errorf("close of a missing agent should be NoAgent, got %v", err)
	}
	steps = append(steps, "prompt and close on a missing agent → NoAgent")

	// Startup must finish before the prompt checks, so its Working/Idle cannot satisfy them.
	deadline := time.Now().Add(turn)
	for {
		l, err := host.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		if hasAgent(l, func(a agent.AgentInfo) bool { return a.Name == name && a.Status.IsReady() }) {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s never became ready after start_agent", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
	drain(events)

	first := "Write the numbers 1 to 80 separated by spaces, then the word OK."
	if err := host.Prompt(ctx, name, first); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	if err := waitFor(events, name, turn, func(s agent.PaneStatus) bool { return s == agent.PaneWorking }); err != nil {
		return nil, fmt.Errorf("prompt → Working: %w", err)
	}
	if err := waitFor(events, name, turn, agent.PaneStatus.IsReady); err != nil {
		return nil, fmt.Errorf("prompt → Idle: %w", err)
	}
	pane, err := host.Read(ctx, name, 200)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if !strings.Contains(pane, "Write the numbers 1 to 80") {
		return nil, fmt.Errorf("the prompt text did not reach the pane: %q", pane)
	}
	steps = append(steps, "prompt → text in the pane, Working, then Idle")

	if err := host.Prompt(ctx, name, "Count from 1 to 300, one number per line, slowly."); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	if err := waitFor(events, name, turn, func(s agent.PaneStatus) bool { return s == agent.PaneWorking }); err != nil {
		return nil, fmt.Errorf("long prompt → Working: %w", err)
	}
	l, err := host.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	if !hasAgent(l, func(a agent.AgentInfo) bool { return a.Name == name && a.Status == agent.PaneWorking }) {
		return nil, fmt.Errorf("%s should be Working before Escape: %v", name, l)
	}
	drain(events)
	if err := host.SendKeys(ctx, name, []string{"Escape"}); err != nil {
		return nil, fmt.Errorf("send_keys: %w", err)
	}
	if err := waitFor(events, name, turn, agent.PaneStatus.IsReady); err != nil {
		return nil, fmt.Errorf("Escape → Idle: %w", err)
	}
	steps = append(steps, "send_keys(Escape) interrupts a working agent")

	if err := host.Close(ctx, name); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	if err := waitExit(events, name, turn); err != nil {
		return nil, fmt.Errorf("close → Exited: %w", err)
	}
	if l, err := host.List(ctx); err != nil || hasAgent(l, named(name)) {
		return nil, fmt.Errorf("%s still listed after close (%v)", name, err)
	}
	steps = append(steps, "close → Exited")

	fresh := host.Subscribe()
	time.Sleep(settle)
	for label, rx := range map[string]<-chan agent.PaneEvent{"first": events, "new": fresh} {
		for _, ev := range drain(rx) {
			if ev == (agent.PaneEvent{Kind: agent.PaneEventExited, Agent: name}) {
				return nil, fmt.Errorf("duplicate Exited for %s on the %s stream", name, label)
			}
		}
	}
	steps = append(steps, "no replayed duplicate Exited")
	return steps, nil
}

func drain(rx <-chan agent.PaneEvent) []agent.PaneEvent {
	var out []agent.PaneEvent
	for {
		select {
		case ev, ok := <-rx:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func waitFor(rx <-chan agent.PaneEvent, name string, limit time.Duration, want func(agent.PaneStatus) bool) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errors.New("timed out")
		case ev, ok := <-rx:
			if !ok {
				return errors.New("event stream ended")
			}
			if ev.Agent == name && ev.Kind == agent.PaneEventStatus && want(ev.Status) {
				return nil
			}
			if ev.Agent == name && ev.Kind == agent.PaneEventExited {
				return errors.New("agent exited")
			}
		}
	}
}

func waitExit(rx <-chan agent.PaneEvent, name string, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errors.New("timed out")
		case ev, ok := <-rx:
			if !ok {
				return errors.New("event stream ended")
			}
			if ev.Agent == name && ev.Kind == agent.PaneEventExited {
				return nil
			}
		}
	}
}

// --- tests ---

// a_worker_start_fits_in_its_limit: see the file comment. The runtime-budget relations are
// asserted by P9 (core/agent runtime); here the budgets this package owns are pinned verbatim.
func TestAWorkerStartFitsInItsLimit(t *testing.T) {
	want := map[string][2]time.Duration{
		"StartTimeout":     {StartTimeout, 45 * time.Second},
		"StartPoll":        {StartPoll, 250 * time.Millisecond},
		"StreamWait":       {StreamWait, 5 * time.Second},
		"ReplayQuiet":      {ReplayQuiet, 300 * time.Millisecond},
		"PaneBusyLimit":    {PaneBusyLimit, 10 * time.Second},
		"PromptReadyWait":  {PromptReadyWait, 10 * time.Second},
		"FirstLineTimeout": {FirstLineTimeout, 10 * time.Second},
		"BackoffMin":       {BackoffMin, 1 * time.Second},
		"BackoffMax":       {BackoffMax, 30 * time.Second},
		"RequestTimeout":   {RequestTimeout, 30 * time.Second},
		"DefaultTimeout":   {DefaultTimeout, 10 * time.Second},
	}
	for k, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %v, want %v", k, v[0], v[1])
		}
	}
	if ReplayMaxMs != 2000 {
		t.Error(ReplayMaxMs)
	}
}

func TestHerdrSessionHostPassesTheConformanceSuiteOnAFakeServer(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	steps, err := runConformance(h, spec("probe"), 3*time.Second, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 7 {
		t.Fatal(steps)
	}
	if server.Subscribes() != 2 {
		t.Fatalf("one stream, resubscribed once for the agent: no reconnect loop (%d)", server.Subscribes())
	}
}

func TestTheWorkspaceIsCreatedOnceAndTabsCarryTheTokenEnv(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, server.Requests("workspace.create"), `[{"label":"deskpixie","cwd":"/Users/x","focus":false}]`)
	mustStart(t, h, "coordinator")
	tab := server.Requests("tab.create")[0].(map[string]any)
	if tab["label"] != "coordinator" {
		t.Fatal(tab)
	}
	assertJSON(t, tab["env"], `{"MASCOT_AGENT_TOKEN":"tok"}`)
	assertJSON(t, server.Requests("agent.start")[0].(map[string]any)["args"], `["--model","haiku"]`)
}

func TestTheWorkspacesEmptyTabIsClosedOnceTheFirstAgentTabIsUp(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	if got := server.Tabs(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("before: %q", got)
	}
	mustStart(t, h, "coordinator")
	if got := server.Tabs(); !reflect.DeepEqual(got, []string{"coordinator"}) {
		t.Fatalf("after: %q", got)
	}
	mustStart(t, h, "w-b")
	assertJSON(t, server.Requests("tab.close"), `[{"tab_id":"w1:t1"}]`)
}

func TestAWorkerTabCarriesItsLabelAndMetadata(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	s := spec("deskpixie-fix-it")
	s.Tab = agent.WorkerTab(agent.WorkerPlan{Name: "deskpixie-fix-it", Slug: "fix-it", RepoName: "deskpixie",
		Job: "job_7", Task: "Fix the   tray icon\nso that it never flickers when the app restarts at night"})
	if _, err := h.StartAgent(bg, s); err != nil {
		t.Fatal(err)
	}
	want := "deskpixie-fix-it · job_7 · Fix the tray icon so that it never flic…"
	if got := server.TabLabel("deskpixie-fix-it"); got != want {
		t.Fatalf("%q", got)
	}
	title, meta := server.Metadata("deskpixie-fix-it")
	wantMeta := map[string]string{"role": "worker", "job": "job_7", "repo": "deskpixie", "branch": "mascot/fix-it",
		"task": "Fix the tray icon so that it never flickers when the app restarts at night"}
	if title != "" || !reflect.DeepEqual(meta, wantMeta) {
		t.Fatalf("%q %v", title, meta)
	}
	// Still found by name, not by label.
	list, err := h.List(bg)
	if err != nil || len(list) != 1 || list[0].Name != "deskpixie-fix-it" {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestRelabelRenamesTheTabAndSetsItsTitle(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	mustStart(t, h, "coordinator")
	job := agent.Job{ID: "job_3", Task: "Update the application icon before the next restart"}
	if err := h.Relabel(bg, "coordinator", agent.CoordinatorTab(&job)); err != nil {
		t.Fatal(err)
	}
	if got := server.TabLabel("coordinator"); got != "coordinator · Update the application icon before the…" {
		t.Fatalf("%q", got)
	}
	title, meta := server.Metadata("coordinator")
	if title != job.Task || meta["job"] != "job_3" || meta["task"] != job.Task {
		t.Fatalf("%q %v", title, meta)
	}
	// Idle: back to the plain label, title and job cleared.
	if err := h.Relabel(bg, "coordinator", agent.CoordinatorTab(nil)); err != nil {
		t.Fatal(err)
	}
	title, meta = server.Metadata("coordinator")
	if got := server.TabLabel("coordinator"); got != "coordinator" || title != "" || !reflect.DeepEqual(meta, map[string]string{"role": "coordinator"}) {
		t.Fatalf("%q %q %v", got, title, meta)
	}
	if err := h.Relabel(bg, "nobody", agent.CoordinatorTab(nil)); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatalf("%v", err)
	}
}

func TestAnAgentProcessExitIsReportedOnceEvenAcrossResubscribes(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	info := mustStart(t, h, "coordinator")
	server.Exit(info.Pane)
	// Starting another agent forces a resubscribe, which replays the journal.
	mustStart(t, h, "other")
	got := collect(events, 500*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestAttachUsesTheAbsoluteBinaryAndTheSessionName(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	if got := newTestHost(t, server).AttachCommand(); !reflect.DeepEqual(got, []string{hostBin, "session", "attach", "deskpixie"}) {
		t.Fatal(got)
	}
}

func TestReadAndUnknownAgents(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	mustStart(t, h, "coordinator")
	if got, err := h.Read(bg, "coordinator", 2); err != nil || got != "line 2\n❯ " {
		t.Fatalf("%q %v", got, err)
	}
	if err := h.Prompt(bg, "ghost", "hi"); !reflect.DeepEqual(err, hostErr(agent.HostNoAgent, "ghost")) {
		t.Fatalf("%#v", err)
	}
	if err := h.Close(bg, "ghost"); !reflect.DeepEqual(err, hostErr(agent.HostNoAgent, "ghost")) {
		t.Fatalf("%#v", err)
	}
}

// Replay start: subscription_started begins a replay, so an old journal line about a live
// agent's pane is not taken as news.
func TestAStaleJournalExitForALiveAgentIsNotReported(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	server.Journal(map[string]any{"data": map[string]any{"pane_id": a.Pane, "type": "pane_exited", "workspace_id": "w1"}, "event": "pane_exited"})
	mustStart(t, h, "other")
	got := collect(events, 500*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Replay end: after ReplayQuiet without events the stream is live, so StartAgent returns with
// the new agent's status changes flowing.
func TestTheReplayEndsAfterAQuietPeriodAndEventsFlowLive(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	started := time.Now()
	mustStart(t, h, "coordinator")
	took := time.Since(started)
	if took < ReplayQuiet || took >= 3000*time.Millisecond {
		t.Fatal(took)
	}
	collect(events, 50*time.Millisecond)
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got := collect(events, 600*time.Millisecond)
	wantEvents(t, got, []agent.PaneEvent{st("coordinator", agent.PaneWorking), st("coordinator", agent.PaneIdle)}, "live")
}

// Replay end: steady traffic cannot hold the replay open past ReplayMaxMs.
func TestSteadyTrafficCannotHoldTheReplayPastItsCap(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.Chatter(50 * time.Millisecond)
	h := newTestHost(t, server)
	events := h.Subscribe()
	started := time.Now()
	mustStart(t, h, "coordinator")
	took := time.Since(started)
	limit := time.Duration(ReplayMaxMs) * time.Millisecond
	if took < limit-100*time.Millisecond || took >= limit+1600*time.Millisecond {
		t.Fatal(took)
	}
	collect(events, 50*time.Millisecond)
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got := collect(events, 600*time.Millisecond)
	wantEvents(t, got, []agent.PaneEvent{st("coordinator", agent.PaneWorking), st("coordinator", agent.PaneIdle)}, "live")
}

// Reconcile: a known agent missing from agent.list is reported exited, once.
func TestAnAgentGoneWithoutAnEventIsReportedExitedOnce(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	server.Vanish(a.Pane)
	server.HangUp()
	got := collect(events, BackoffMin+1600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("reconcile after the reconnect: %+v", got)
	}
	b := mustStart(t, h, "second")
	if b.Pane != a.Pane {
		t.Fatal(b.Pane)
	}
	mustStart(t, h, "third")
	got = collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("already reported: %+v", got)
	}
}

// A new agent on a pane id we still map to an older agent: that agent is gone, once.
func TestAnAgentWhosePaneIDIsReusedUnseenIsReportedExitedOnce(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	server.Vanish(a.Pane)
	b := mustStart(t, h, "second")
	if b.Pane != a.Pane {
		t.Fatal("the fake reuses the pane id")
	}
	mustStart(t, h, "third")
	got := collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 || count(got, exitedEv("second")) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Reconcile: a listed status that differs from the last one sent is sent, once.
func TestAStatusMissedByTheStreamIsSentByReconcileOnce(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	server.SetStatusQuietly(a.Pane, "blocked")
	mustStart(t, h, "second")
	mustStart(t, h, "third")
	got := collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneBlocked)) != 1 {
		t.Fatalf("%+v", got)
	}
}

// Resubscribe: a reconnect after the stream drops replays and reconciles again too.
func TestEveryResubscribeReplaysAndReconciles(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	server.SetStatusQuietly(a.Pane, "blocked")
	server.HangUp()
	got := collect(events, BackoffMin+1600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneBlocked)) != 1 {
		t.Fatalf("after a reconnect: %+v", got)
	}
	server.SetStatusQuietly(a.Pane, "done")
	mustStart(t, h, "second")
	got = collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneDone)) != 1 {
		t.Fatalf("after a new agent: %+v", got)
	}
}

func TestCloseExactClosesOnlyTheAgentStillInThatPane(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	a := mustStart(t, h, "a")
	if err := h.Close(bg, "a"); err != nil {
		t.Fatal(err)
	}
	b := mustStart(t, h, "b")
	if b.Pane != a.Pane {
		t.Fatal("the fake reuses the pane id")
	}
	if err := h.CloseExact(bg, "a", a.Pane); err != nil {
		t.Fatal(err)
	}
	if !hasAgent(listed2(t, h), named("b")) {
		t.Fatal("b took the pane: left alone")
	}
	if err := h.CloseExact(bg, "b", "w9:p9"); err != nil {
		t.Fatal(err)
	}
	if !hasAgent(listed2(t, h), named("b")) {
		t.Fatal("another pane: left alone")
	}
	if err := h.CloseExact(bg, "b", b.Pane); err != nil {
		t.Fatal(err)
	}
	if hasAgent(listed2(t, h), named("b")) {
		t.Fatal("b still listed")
	}
}

// The reused-pane case end to end: A's old close, replayed after B took A's pane id, must not
// end B.
func TestAReplayedCloseDoesNotEndAnAgentOnAReusedPaneID(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "a")
	if err := h.Close(bg, "a"); err != nil {
		t.Fatal(err)
	}
	b := mustStart(t, h, "b")
	if b.Pane != a.Pane {
		t.Fatal("the fake reuses the pane id")
	}
	got := collect(events, 500*time.Millisecond)
	if count(got, exitedEv("a")) != 1 || count(got, exitedEv("b")) != 0 {
		t.Fatalf("%+v", got)
	}
	if !hasAgent(listed2(t, h), named("b")) {
		t.Fatal("b not listed")
	}
	if err := h.Close(bg, "b"); err != nil {
		t.Fatal(err)
	}
	got = collect(events, 600*time.Millisecond)
	if count(got, exitedEv("b")) != 1 {
		t.Fatalf("a live close still ends b: %+v", got)
	}
}

// A restarted server has lost the workspace: StartAgent resolves it again and retries.
func TestStartAgentSurvivesAServerThatLostItsWorkspace(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	mustStart(t, h, "coordinator")
	server.DropWorkspaces()
	b := mustStart(t, h, "second")
	if b.Name != "second" {
		t.Fatal(b)
	}
	if n := len(server.Requests("workspace.create")); n != 2 {
		t.Fatal(n)
	}
	if n := len(server.Requests("tab.create")); n != 3 {
		t.Fatalf("one failed attempt, one retry: %d", n)
	}
}

type startResult struct {
	info agent.AgentInfo
	err  error
}

func startAsync(h *Host, ctx context.Context, s agent.AgentSpec) <-chan startResult {
	ch := make(chan startResult, 1)
	go func() {
		info, err := h.StartAgent(ctx, s)
		ch <- startResult{info, err}
	}()
	return ch
}

// Reconcile while an agent is still launching (not yet in agent.list) must not end it.
func TestALaunchingAgentIsNotEndedByReconcile(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	h := newTestHost(t, server)
	events := h.Subscribe()
	res := startAsync(h, bg, spec("coordinator"))
	// Long enough for the replay window and its reconcile to pass while launching.
	got := collect(events, 800*time.Millisecond)
	server.Ready("w1:p1")
	r := <-res
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
	if r.err != nil || r.info.Status != agent.PaneIdle {
		t.Fatalf("%+v %v", r.info, r.err)
	}
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got = collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneWorking)) != 1 || count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
}

// An agent process exit whose event falls in a replay window (and is dropped there) is
// reported once by the reconcile: its pane stays listed without an agent.
func TestAnExitDuringTheReplayIsReportedOnceAfterReconcile(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	a := mustStart(t, h, "coordinator")
	res := startAsync(h, bg, spec("second"))
	// The resubscribe for "second" is under way: its replay window is open.
	time.Sleep(100 * time.Millisecond)
	server.Exit(a.Pane)
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
	mustStart(t, h, "third")
	got := collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("%+v", got)
	}
}

// Dropping the host (Rust Drop; Go Shutdown) stops its event pump: no resubscribe after the
// stream ends.
func TestDroppingTheHostStopsItsEventPump(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if server.Subscribes() != 1 {
		t.Fatal(server.Subscribes())
	}
	h.Shutdown()
	server.HangUp()
	time.Sleep(BackoffMin + 1000*time.Millisecond)
	if server.Subscribes() != 1 {
		t.Fatal(server.Subscribes())
	}
}

// A subscribe that is never answered times out and is retried.
func TestAnUnansweredSubscribeIsRetried(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.MuteSubscribe(true)
	h := newTestHost(t, server)
	h.firstLineTimeout = 200 * time.Millisecond
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	server.MuteSubscribe(false)
	events := h.Subscribe()
	time.Sleep(BackoffMin + 1000*time.Millisecond)
	if server.Subscribes() < 2 {
		t.Fatal(server.Subscribes())
	}
	// The new stream works: an agent's status changes arrive.
	mustStart(t, h, "coordinator")
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got := collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneWorking)) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestAgentStartWaitsLessThanTheTransportAllows(t *testing.T) {
	if time.Duration(agentStartTimeoutMs())*time.Millisecond >= RequestTimeout {
		t.Fatal(agentStartTimeoutMs())
	}
	if agentStartTimeoutMs() != 25000 {
		t.Fatal(agentStartTimeoutMs())
	}
}

// The coordinator's process exits but its pane stays: it is no longer listed, and starting it
// again replaces the stale pane (no "already exists").
func TestAnExitedAgentIsUnlistedAndCanBeStartedAgain(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	a := mustStart(t, h, "coordinator")
	server.Exit(a.Pane)
	if l := listed2(t, h); hasAgent(l, named("coordinator")) {
		t.Fatalf("%+v", l)
	}
	b := mustStart(t, h, "coordinator")
	if b.Status != agent.PaneIdle {
		t.Fatal(b)
	}
	if server.PanesNamed("coordinator") != 1 {
		t.Fatal(server.PanesNamed("coordinator"))
	}
	n := 0
	for _, i := range listed2(t, h) {
		if i.Name == "coordinator" {
			n++
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}

// A launching agent (no agent detected yet, launch_pending) is not taken for an exited one.
func TestALaunchingAgentIsStillListed(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	server.ListLaunching()
	h := newTestHost(t, server)
	res := startAsync(h, bg, spec("coordinator"))
	time.Sleep(100 * time.Millisecond)
	l := listed2(t, h)
	server.Ready("w1:p1")
	<-res
	if !hasAgent(l, named("coordinator")) {
		t.Fatalf("%+v", l)
	}
}

// Re-creating an agent after /exit: the old pane's pane_closed arrives late, after the new
// agent started. It must not end the new agent.
func TestALateCloseOfTheStalePaneDoesNotEndTheNewAgent(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	old := mustStart(t, h, "coordinator")
	server.Exit(old.Pane)
	collect(events, 300*time.Millisecond)
	server.HoldCloses()
	nw := mustStart(t, h, "coordinator")
	if nw.Pane == old.Pane {
		t.Fatal("the new tab is made while the stale pane still holds its id")
	}
	server.ReleaseCloses()
	got := collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
	if !hasAgent(listed2(t, h), func(i agent.AgentInfo) bool { return i.Name == "coordinator" && i.Pane == nw.Pane }) {
		t.Fatal("new agent not listed")
	}
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got = collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneWorking)) != 1 {
		t.Fatalf("still tracked: %+v", got)
	}
}

// Quitting stops the coordinator with Escape, then a close. With no server running there is
// nothing to stop, and neither call may start one.
func TestCloseAndKeysNeverStartAServer(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner()
	runner.answer(hostBin+" status --json", runOK(`{"client":{"protocol":17},"server":{"running":false,"protocol":null,"compatible":null,"socket":"/tmp/shh-none.sock"}}`))
	sup, err := NewSupervisor(hostBin, pairs("HERDR_SESSION", hostSession), runner)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHost(hostSession, "/Users/x", sup.WithTiming(10*time.Millisecond, 100*time.Millisecond))
	defer h.Shutdown()
	if err := h.SendKeys(bg, "coordinator", []string{"Escape"}); !reflect.DeepEqual(err, hostErr(agent.HostNoAgent, "coordinator")) {
		t.Fatalf("%#v", err)
	}
	if err := h.Close(bg, "coordinator"); err != nil {
		t.Fatal(err)
	}
	if runner.spawnedCount() != 0 {
		t.Fatal("no server was started")
	}
}

// Replacing a live agent (a coordinator left by an earlier run): the new tab opens before the
// old pane closes, so a late close of the old pane can't end the new agent, and the old
// agent's close is not reported as an exit of the name.
func TestReplacingALiveAgentOpensTheNewTabFirstAndReportsNoExit(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	old := mustStart(t, h, "coordinator")
	collect(events, 300*time.Millisecond)
	server.HoldCloses()
	nw, err := h.ReplaceAgent(bg, spec("coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	if nw.Pane == old.Pane {
		t.Fatal("the new tab is made while the old pane still holds its id")
	}
	server.ReleaseCloses()
	got := collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
	if server.PanesNamed("coordinator") != 1 {
		t.Fatal(server.PanesNamed("coordinator"))
	}
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got = collect(events, 600*time.Millisecond)
	if count(got, st("coordinator", agent.PaneWorking)) != 1 {
		t.Fatalf("still tracked: %+v", got)
	}
}

// claude --continue with nothing to continue exits during launch: detected, then gone. The
// ready poll fails at once instead of waiting out the start timeout.
func TestAnAgentThatExitsWhileStartingFailsFast(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.HoldPending()
	h := newTestHost(t, server)
	h.startTimeout = 10 * time.Second
	began := time.Now()
	res := startAsync(h, bg, spec("coordinator"))
	// Detected after 50 ms, still launch_pending; then the process exits.
	time.Sleep(300 * time.Millisecond)
	server.Exit("w1:p1")
	r := <-res
	if !reflect.DeepEqual(r.err, hostErr(agent.HostExitedAtStart, "claude")) {
		t.Fatalf("%#v", r.err)
	}
	if time.Since(began) >= 3*time.Second {
		t.Fatal(time.Since(began))
	}
	if server.PaneCount() != 0 {
		t.Fatal("its tab is closed")
	}
}

// The same when the launch ends without the agent ever being detected (back at the shell).
func TestALaunchThatEndsAtTheShellFailsFast(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	h := newTestHost(t, server)
	h.startTimeout = 10 * time.Second
	began := time.Now()
	res := startAsync(h, bg, spec("coordinator"))
	time.Sleep(300 * time.Millisecond)
	server.Exit("w1:p1")
	r := <-res
	if !reflect.DeepEqual(r.err, hostErr(agent.HostExitedAtStart, "claude")) {
		t.Fatalf("%#v", r.err)
	}
	if time.Since(began) >= 3*time.Second {
		t.Fatal(time.Since(began))
	}
}

// A start abandoned mid-way (Rust: the runtime's start limit drops the future; Go: its context
// ends) leaves no tab.
func TestAStartDroppedMidWayClosesItsTab(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	h := newTestHost(t, server)
	ctx, cancel := context.WithTimeout(bg, 400*time.Millisecond)
	defer cancel()
	if _, err := h.StartAgent(ctx, spec("coordinator")); err == nil {
		t.Fatal("want the start to be cut short")
	}
	time.Sleep(300 * time.Millisecond)
	if server.PaneCount() != 0 {
		t.Fatal(server.PaneCount())
	}
}

// Replacing a live agent whose close fails keeps it tracked as seen alive, so its later
// disappearance is still reported.
func TestAFailedReplaceKeepsTheOldAgentSeenAlive(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	old := mustStart(t, h, "coordinator")
	server.FailPaneCloses(1)
	if _, err := h.ReplaceAgent(bg, spec("coordinator")); err == nil {
		t.Fatal("want an error")
	}
	collect(events, 300*time.Millisecond)
	server.Vanish(old.Pane)
	server.HangUp()
	got := collect(events, BackoffMin+1600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("%+v", got)
	}
}

// The ready poll seeing the agent counts as seen alive, so a later vanish is an exit.
func TestTheReadyPollMarksTheAgentSeenAlive(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	h := newTestHost(t, server)
	events := h.Subscribe()
	res := startAsync(h, bg, spec("coordinator"))
	// The replay and its reconcile pass while launching; then it is ready, with no event.
	time.Sleep(800 * time.Millisecond)
	server.ReadyQuietly("w1:p1")
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
	server.Vanish("w1:p1")
	server.HangUp()
	got := collect(events, BackoffMin+1600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("%+v", got)
	}
}

// Agents found running by EnsureSession count as seen alive.
func TestAgentsFoundAtStartupAreSeenAlive(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	first := newTestHost(t, server)
	a := mustStart(t, first, "coordinator")
	first.Shutdown()
	h := newTestHost(t, server)
	events := h.Subscribe()
	if err := h.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	server.Vanish(a.Pane)
	got := collect(events, 1200*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 1 {
		t.Fatalf("%+v", got)
	}
}

// A failed agent.start closes the tab it was given.
func TestAFailedAgentStartClosesItsTab(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.FailAgentStart()
	h := newTestHost(t, server)
	if _, err := h.StartAgent(bg, spec("coordinator")); err == nil {
		t.Fatal("want an error")
	}
	if server.PaneCount() != 0 {
		t.Fatal(server.PaneCount())
	}
}

// An agent that never gets ready: StartAgent times out, closes the tab, and reports no exit.
func TestAStartThatNeverGetsReadyClosesItsTabQuietly(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.ManualReady()
	h := newTestHost(t, server)
	h.startTimeout = 600 * time.Millisecond
	events := h.Subscribe()
	if _, err := h.StartAgent(bg, spec("coordinator")); !agent.IsHostError(err, agent.HostTimeout) {
		t.Fatalf("%#v", err)
	}
	if server.PaneCount() != 0 {
		t.Fatal(server.PaneCount())
	}
	got := collect(events, 600*time.Millisecond)
	if count(got, exitedEv("coordinator")) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestIntegrationMissingComesFromTheSupervisor(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	ok := newTestHost(t, server)
	if err := ok.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	if ok.IntegrationMissing() {
		t.Fatal("current")
	}
	missing := hostWithIntegration(t, server, "claude: not installed")
	if err := missing.EnsureSession(bg); err != nil {
		t.Fatal(err)
	}
	if !missing.IntegrationMissing() {
		t.Fatal("missing")
	}
}

// Herdr 0.7.5 reports the agent detected and idle while still launch_pending, and refuses
// prompts then: StartAgent must not return inside that window.
func TestStartAgentWaitsOutTheLaunchPendingWindow(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.HoldPending()
	h := newTestHost(t, server)
	var finished atomic.Bool
	type out struct {
		err        error
		windowOver bool
	}
	ch := make(chan out, 1)
	go func() {
		_, err := h.StartAgent(bg, spec("coordinator"))
		ch <- out{err, finished.Load()}
	}()
	// Detected and idle after 50 ms; the window stays open well past a poll.
	time.Sleep(800 * time.Millisecond)
	finished.Store(true)
	server.FinishLaunch("w1:p1")
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !r.windowOver {
		t.Fatal("StartAgent returned inside the launch_pending window")
	}
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
}

// An agent restarted outside StartAgent is launch_pending again: Prompt waits for it and
// retries once.
func TestPromptRetriesOnceThroughTheLaunchPendingWindow(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	a := mustStart(t, h, "coordinator")
	server.Relaunch(a.Pane)
	go func() {
		time.Sleep(600 * time.Millisecond)
		server.FinishLaunch(a.Pane)
	}()
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	if n := len(server.Requests("agent.prompt")); n != 2 {
		t.Fatalf("one refused, one retried: %d", n)
	}
}

// The subscription is one long-lived connection: events still arrive on it well after the
// subscribe, and there is no reconnect loop (Herdr ends a subscription on client half-close).
func TestTheEventStreamStaysOpenOnOneConnection(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	h := newTestHost(t, server)
	events := h.Subscribe()
	mustStart(t, h, "coordinator")
	if server.Subscribes() != 2 {
		t.Fatalf("the first subscribe, then one for the new agent: %d", server.Subscribes())
	}
	time.Sleep(2000 * time.Millisecond)
	collect(events, 50*time.Millisecond)
	if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
		t.Fatal(err)
	}
	got := collect(events, 600*time.Millisecond)
	wantEvents(t, got, []agent.PaneEvent{st("coordinator", agent.PaneWorking), st("coordinator", agent.PaneIdle)}, "live")
	if server.Subscribes() != 2 {
		t.Fatalf("no reconnects: %d", server.Subscribes())
	}
}

// Herdr 0.7.5 refuses agent.start with agent_pane_busy until the new tab's shell is at its
// prompt: StartAgent retries.
func TestAgentStartIsRetriedWhileThePaneIsBusy(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.BusyAgentStarts(2)
	h := newTestHost(t, server)
	info := mustStart(t, h, "coordinator")
	if info.Status != agent.PaneIdle {
		t.Fatal(info)
	}
	if n := len(server.Requests("agent.start")); n != 3 {
		t.Fatal(n)
	}
	if server.PaneCount() != 1 {
		t.Fatal(server.PaneCount())
	}
}

// A pane busy past the limit: StartAgent fails with the busy error and leaves no tab.
func TestAPaneBusyPastTheLimitFailsWithoutLeakingItsTab(t *testing.T) {
	t.Parallel()
	server := herdrtest.Start(t)
	server.BusyAgentStarts(1 << 30)
	h := newTestHost(t, server)
	h.paneBusyLimit = 700 * time.Millisecond
	started := time.Now()
	_, err := h.StartAgent(bg, spec("coordinator"))
	if !IsPaneBusy(err) {
		t.Fatalf("%#v", err)
	}
	if time.Since(started) < 700*time.Millisecond {
		t.Fatal(time.Since(started))
	}
	if n := len(server.Requests("agent.start")); n < 2 {
		t.Fatal(n)
	}
	if server.PaneCount() != 0 {
		t.Fatal(server.PaneCount())
	}
}

// The Go host satisfies the agent seam.
var _ agent.SessionHost = (*Host)(nil)

// hostOn is a host whose CLI reports cli and whose fake server speaks the server's protocol
// (status in that Herdr version's captured shape).
func hostOn(t *testing.T, server *herdrtest.FakeHerdr, cli uint32) (*Host, *Supervisor) {
	t.Helper()
	runner := newFakeRunner()
	runner.answer(hostBin+" status --json", runOK(server.StatusJSON(cli)))
	runner.answer(hostBin+" integration status", runOK("claude: current (v7) (/x)"))
	sup, err := NewSupervisor(hostBin, pairs("HERDR_SESSION", hostSession), runner)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHost(hostSession, "/Users/x", sup.WithTiming(10*time.Millisecond, 100*time.Millisecond))
	t.Cleanup(h.Shutdown)
	return h, sup
}

// The adapter runs the SessionHost conformance suite, a read and a close against a fake server
// of each supported Herdr version, and negotiates that version's protocol.
func TestTheHostWorksOnEverySupportedProtocol(t *testing.T) {
	for _, p := range []uint32{17, 22} {
		t.Run(fmt.Sprint(p), func(t *testing.T) {
			t.Parallel()
			server := herdrtest.StartProtocol(t, p)
			h, sup := hostOn(t, server, p)
			steps, err := runConformance(h, spec("probe"), 3*time.Second, 300*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 7 {
				t.Fatal(steps)
			}
			if sup.Protocol() != p {
				t.Fatalf("negotiated %d, want %d", sup.Protocol(), p)
			}
			mustStart(t, h, "coordinator")
			if err := h.Prompt(bg, "coordinator", "hi"); err != nil {
				t.Fatal(err)
			}
			if screen, err := h.Read(bg, "coordinator", 5); err != nil || !strings.Contains(screen, "❯ hi") {
				t.Fatalf("%q %v", screen, err)
			}
			if err := h.Close(bg, "coordinator"); err != nil {
				t.Fatal(err)
			}
			if server.PaneCount() != 0 {
				t.Fatal(server.PaneCount())
			}
		})
	}
}

// A CLI and server of different supported versions (or an unknown one) never get a workspace
// or a tab: every call is a version mismatch naming the supported versions.
func TestTheHostRefusesMismatchedVersions(t *testing.T) {
	for _, c := range []struct{ cli, server uint32 }{{17, 22}, {22, 17}, {99, 99}} {
		t.Run(fmt.Sprintf("cli%d-server%d", c.cli, c.server), func(t *testing.T) {
			t.Parallel()
			server := herdrtest.StartProtocol(t, c.server)
			h, sup := hostOn(t, server, c.cli)
			for _, err := range []error{h.EnsureSession(bg), func() error { _, err := h.StartAgent(bg, spec("coordinator")); return err }()} {
				var he agent.HostError
				if !errors.As(err, &he) || he.Kind != agent.HostVersionMismatch || he.Client != c.cli || he.Server != c.server {
					t.Fatalf("%#v", err)
				}
				if !strings.Contains(err.Error(), SupportedVersions) {
					t.Fatalf("%q does not name the supported versions", err)
				}
			}
			if sup.Protocol() != 0 || len(server.Requests("workspace.create")) != 0 || server.PaneCount() != 0 {
				t.Fatal("nothing may be done on a mismatched server")
			}
		})
	}
}
