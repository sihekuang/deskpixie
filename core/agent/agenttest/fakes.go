// Package agenttest holds test doubles that fully implement the delegation seams (Liskov: the
// core cannot tell them from real adapters) and the SessionHost conformance suite.
// Port of ai-mascot crates/agent-core/src/{fakes.rs, conformance.rs} (the `fakes` feature).
//
// Hanging fakes (HangStart, HangClose) block until their context is done, so a caller's
// timeout ends them; that keeps them usable inside testing/synctest bubbles.
package agenttest

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// ---------- session host ----------

// Fault is deliberate misbehaviour, so tests can prove the conformance suite catches it.
type Fault int

const (
	// ReplayExitedOnSubscribe: Subscribe replays Exited for every agent already closed.
	ReplayExitedOnSubscribe Fault = iota
	// DoubleExitOnClose: Close reports Exited twice.
	DoubleExitOnClose
	// IgnoreEscape: Escape does nothing.
	IgnoreEscape
	// EnsureKillsPanes: EnsureSession destroys the running panes.
	EnsureKillsPanes
)

// subscriberBuffer is how many pane events a subscriber may fall behind (Rust's channel is
// unbounded; a fake never gets near this).
const subscriberBuffer = 4096

// Prompt is one recorded prompt: the agent and the text.
type Prompt struct{ Agent, Text string }

// Keys is one recorded send_keys call.
type Keys struct {
	Agent string
	Keys  []string
}

type hostState struct {
	ensures       int
	nextPane      uint64
	agents        map[string]agent.AgentInfo
	starts        []agent.AgentSpec
	prompts       []Prompt
	keys          []Keys
	closed        []string
	subscribers   []chan agent.PaneEvent
	failEnsure    error
	failStart     error
	failContinue  bool
	startAttempts int
	faults        []Fault
	screens       map[string]string
	hangStart     bool
	panicStart    bool
	dieOnStart    bool
	failList      bool
	hangClose     bool
	swallow       bool
}

// FakeSessionHost is an in-memory multiplexer. By default a prompt makes the agent Working and,
// at once, Idle again (a turn that finishes immediately); ManualTurns leaves it Working.
type FakeSessionHost struct {
	mu     *sync.Mutex
	s      *hostState
	manual bool
	hold   bool
}

func NewFakeSessionHost() *FakeSessionHost {
	return &FakeSessionHost{mu: &sync.Mutex{}, s: &hostState{agents: map[string]agent.AgentInfo{}, screens: map[string]string{}}}
}

// ManualTurns: prompts leave the agent Working until the test calls SetStatus.
func (f *FakeSessionHost) ManualTurns() *FakeSessionHost {
	c := *f
	c.manual = true
	return &c
}

// HoldTurns: long turns (a prompt starting "Count") stay Working until Escape sends the agent
// Idle; other prompts finish at once. What the conformance suite needs to test interruption.
func (f *FakeSessionHost) HoldTurns() *FakeSessionHost {
	c := *f
	c.hold = true
	return &c
}

func (f *FakeSessionHost) with(fn func(s *hostState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.s)
}

// Inject makes the host misbehave in the given way.
func (f *FakeSessionHost) Inject(fault Fault) {
	f.with(func(s *hostState) { s.faults = append(s.faults, fault) })
}

// FailEnsure: EnsureSession fails with err (nil: works again).
func (f *FakeSessionHost) FailEnsure(err error) { f.with(func(s *hostState) { s.failEnsure = err }) }

// FailStart: StartAgent fails with err (nil: works again).
func (f *FakeSessionHost) FailStart(err error) { f.with(func(s *hostState) { s.failStart = err }) }

// HangStart: StartAgent hangs (until its context is done) from now on.
func (f *FakeSessionHost) HangStart(on bool) { f.with(func(s *hostState) { s.hangStart = on }) }

// DieOnStart: started panes die at once (their process exits during the start window).
func (f *FakeSessionHost) DieOnStart(on bool) { f.with(func(s *hostState) { s.dieOnStart = on }) }

// PanicStart: StartAgent panics from now on.
func (f *FakeSessionHost) PanicStart(on bool) { f.with(func(s *hostState) { s.panicStart = on }) }

// FailList: List fails from now on.
func (f *FakeSessionHost) FailList(on bool) { f.with(func(s *hostState) { s.failList = on }) }

// HangClose: Close hangs (until its context is done) from now on.
func (f *FakeSessionHost) HangClose(on bool) { f.with(func(s *hostState) { s.hangClose = on }) }

// SwallowPrompts: prompts are recorded but change nothing (a start-up screen swallowed them).
func (f *FakeSessionHost) SwallowPrompts(on bool) { f.with(func(s *hostState) { s.swallow = on }) }

// FailContinue: starts with --continue fail, like `claude --continue` with nothing to continue.
func (f *FakeSessionHost) FailContinue(on bool) { f.with(func(s *hostState) { s.failContinue = on }) }

// StartAttempts counts every StartAgent call, failed or not.
func (f *FakeSessionHost) StartAttempts() (n int) {
	f.with(func(s *hostState) { n = s.startAttempts })
	return
}

func (f *FakeSessionHost) Ensures() (n int) {
	f.with(func(s *hostState) { n = s.ensures })
	return
}

func (f *FakeSessionHost) Starts() (out []agent.AgentSpec) {
	f.with(func(s *hostState) { out = slices.Clone(s.starts) })
	return
}

func (f *FakeSessionHost) Prompts() (out []Prompt) {
	f.with(func(s *hostState) { out = slices.Clone(s.prompts) })
	return
}

func (f *FakeSessionHost) Keys() (out []Keys) {
	f.with(func(s *hostState) { out = slices.Clone(s.keys) })
	return
}

func (f *FakeSessionHost) Closed() (out []string) {
	f.with(func(s *hostState) { out = slices.Clone(s.closed) })
	return
}

// StatusOf is the agent's status, false when it has no pane.
func (f *FakeSessionHost) StatusOf(name string) (st agent.PaneStatus, ok bool) {
	f.with(func(s *hostState) {
		var a agent.AgentInfo
		a, ok = s.agents[name]
		st = a.Status
	})
	return
}

// SetStatus changes the agent's status (reported to subscribers).
func (f *FakeSessionHost) SetStatus(name string, status agent.PaneStatus) {
	f.with(func(s *hostState) {
		if a, ok := s.agents[name]; ok {
			a.Status = status
			s.agents[name] = a
			emit(s, agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: name, Status: status})
		}
	})
}

// SetScreen is what Read returns for name from now on (e.g. a start dialog).
func (f *FakeSessionHost) SetScreen(name, text string) {
	f.with(func(s *hostState) { s.screens[name] = text })
}

// Kill: the agent's process dies; its pane disappears and Exited is reported once.
func (f *FakeSessionHost) Kill(name string) {
	f.with(func(s *hostState) {
		if _, ok := s.agents[name]; ok {
			delete(s.agents, name)
			emit(s, agent.PaneEvent{Kind: agent.PaneEventExited, Agent: name})
		}
	})
}

func emit(s *hostState, ev agent.PaneEvent) {
	for _, tx := range s.subscribers {
		select {
		case tx <- ev:
		default:
		}
	}
}

func known(s *hostState, name string) error {
	if _, ok := s.agents[name]; ok {
		return nil
	}
	return agent.ErrNoAgent(name)
}

func (f *FakeSessionHost) Name() string { return "fake" }

func (f *FakeSessionHost) EnsureSession(context.Context) (err error) {
	f.with(func(s *hostState) {
		if s.failEnsure != nil {
			err = s.failEnsure
			return
		}
		s.ensures++
		if slices.Contains(s.faults, EnsureKillsPanes) {
			clear(s.agents)
		}
	})
	return
}

func (f *FakeSessionHost) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	var hang, crash bool
	f.with(func(s *hostState) { hang, crash = s.hangStart, s.panicStart })
	if crash {
		panic("fake host crashed")
	}
	if hang {
		<-ctx.Done()
		return agent.AgentInfo{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	s.startAttempts++
	if s.failStart != nil {
		return agent.AgentInfo{}, s.failStart
	}
	if s.failContinue && slices.Contains(spec.Args, "--continue") {
		return agent.AgentInfo{}, agent.HostError{Kind: agent.HostExitedAtStart, Detail: "claude"}
	}
	if _, ok := s.agents[spec.Name]; ok {
		return agent.AgentInfo{}, agent.HostError{Kind: agent.HostOther, Detail: fmt.Sprintf("agent %s already exists", spec.Name)}
	}
	s.nextPane++
	info := agent.AgentInfo{Name: spec.Name, Pane: fmt.Sprintf("w1:p%d", s.nextPane), Cwd: spec.Cwd, Status: agent.PaneIdle}
	s.agents[spec.Name] = info
	spec.Args = slices.Clone(spec.Args)
	spec.Env = slices.Clone(spec.Env)
	s.starts = append(s.starts, spec)
	if s.dieOnStart {
		delete(s.agents, info.Name)
	}
	return info, nil
}

func (f *FakeSessionHost) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.DefaultReplaceAgent(ctx, f, spec)
}

func (f *FakeSessionHost) Prompt(_ context.Context, name, text string) error {
	var err error
	swallow := false
	f.with(func(s *hostState) {
		if err = known(s, name); err != nil {
			return
		}
		s.prompts = append(s.prompts, Prompt{name, text})
		swallow = s.swallow
	})
	if err != nil || swallow {
		return err
	}
	f.SetStatus(name, agent.PaneWorking)
	if !f.manual && !(f.hold && strings.HasPrefix(text, "Count")) {
		f.SetStatus(name, agent.PaneIdle)
	}
	return nil
}

func (f *FakeSessionHost) SendKeys(_ context.Context, name string, keys []string) error {
	var err error
	interrupt := false
	f.with(func(s *hostState) {
		if err = known(s, name); err != nil {
			return
		}
		s.keys = append(s.keys, Keys{name, slices.Clone(keys)})
		interrupt = slices.Contains(keys, "Escape") && !slices.Contains(s.faults, IgnoreEscape) && s.agents[name].Status == agent.PaneWorking
	})
	if err != nil {
		return err
	}
	if interrupt {
		f.SetStatus(name, agent.PaneIdle)
	}
	return nil
}

func (f *FakeSessionHost) Read(_ context.Context, name string, _ uint16) (out string, err error) {
	f.with(func(s *hostState) {
		if err = known(s, name); err != nil {
			return
		}
		if screen, ok := s.screens[name]; ok {
			out = screen
			return
		}
		for i := len(s.prompts) - 1; i >= 0; i-- {
			if s.prompts[i].Agent == name {
				out = s.prompts[i].Text
				return
			}
		}
	})
	return
}

func (f *FakeSessionHost) Close(ctx context.Context, name string) error {
	var hang bool
	f.with(func(s *hostState) { hang = s.hangClose })
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	if err := known(s, name); err != nil {
		return err
	}
	delete(s.agents, name)
	s.closed = append(s.closed, name)
	emit(s, agent.PaneEvent{Kind: agent.PaneEventExited, Agent: name})
	if slices.Contains(s.faults, DoubleExitOnClose) {
		emit(s, agent.PaneEvent{Kind: agent.PaneEventExited, Agent: name})
	}
	return nil
}

func (f *FakeSessionHost) CloseExact(ctx context.Context, name, pane string) error {
	return agent.DefaultCloseExact(ctx, f, name, pane)
}

func (f *FakeSessionHost) List(context.Context) (out []agent.AgentInfo, err error) {
	f.with(func(s *hostState) {
		if s.failList {
			err = agent.HostError{Kind: agent.HostOther, Detail: "list failed"}
			return
		}
		for _, k := range slices.Sorted(maps.Keys(s.agents)) {
			out = append(out, s.agents[k])
		}
	})
	return
}

func (f *FakeSessionHost) Subscribe() <-chan agent.PaneEvent {
	tx := make(chan agent.PaneEvent, subscriberBuffer)
	f.with(func(s *hostState) {
		if slices.Contains(s.faults, ReplayExitedOnSubscribe) {
			for _, a := range s.closed {
				tx <- agent.PaneEvent{Kind: agent.PaneEventExited, Agent: a}
			}
		}
		s.subscribers = append(s.subscribers, tx)
	})
	return tx
}

func (f *FakeSessionHost) AttachCommand() []string {
	return []string{"fake-herdr", "session", "attach", "test"}
}

// ---------- launcher, voice, notifier, mascot ----------

// FakeLauncher records every Attach it opens; SetFail makes Open fail.
type FakeLauncher struct {
	mu     sync.Mutex
	opened []agent.Attach
	fail   string
}

func (l *FakeLauncher) Name() string { return "fake" }

func (l *FakeLauncher) Open(a agent.Attach) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != "" {
		return agent.LaunchError(l.fail)
	}
	l.opened = append(l.opened, agent.Attach{Argv: slices.Clone(a.Argv), Dir: a.Dir})
	return nil
}

// Opened is the argv of every Attach opened.
func (l *FakeLauncher) Opened() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]string, len(l.opened))
	for i, a := range l.opened {
		out[i] = slices.Clone(a.Argv)
	}
	return out
}

// Attaches is every Attach opened.
func (l *FakeLauncher) Attaches() []agent.Attach {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.opened)
}

func (l *FakeLauncher) SetFail(why string) {
	l.mu.Lock()
	l.fail = why
	l.mu.Unlock()
}

// VoiceCallKind tells VoiceCall variants apart.
type VoiceCallKind int

const (
	CallInject VoiceCallKind = iota
	CallHoldOpen
)

// VoiceCall is one recorded VoiceLink call: Inject(Text) or HoldOpen(On).
type VoiceCall struct {
	Kind VoiceCallKind
	Text string
	On   bool
}

func InjectCall(text string) VoiceCall { return VoiceCall{Kind: CallInject, Text: text} }
func HoldOpenCall(on bool) VoiceCall   { return VoiceCall{Kind: CallHoldOpen, On: on} }

// RecordingVoiceLink records injects and holds; IsOpen answers what SetOpen set.
type RecordingVoiceLink struct {
	mu    sync.Mutex
	open  bool
	calls []VoiceCall
}

func (v *RecordingVoiceLink) SetOpen(open bool) {
	v.mu.Lock()
	v.open = open
	v.mu.Unlock()
}

func (v *RecordingVoiceLink) Calls() []VoiceCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.calls)
}

func (v *RecordingVoiceLink) IsOpen() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.open
}

func (v *RecordingVoiceLink) Inject(n agent.Notice) {
	v.mu.Lock()
	v.calls = append(v.calls, InjectCall(n.Text))
	v.mu.Unlock()
}

func (v *RecordingVoiceLink) HoldOpen(on bool) {
	v.mu.Lock()
	v.calls = append(v.calls, HoldOpenCall(on))
	v.mu.Unlock()
}

// AlertKind tells Alert variants apart.
type AlertKind int

const (
	AlertChimeKind AlertKind = iota
	AlertNotifyKind
	AlertCueKind
)

// Alert is a chime, a notification (Title, Body) or a mascot cue.
type Alert struct {
	Kind        AlertKind
	Title, Body string
	Cue         agent.Cue
}

func ChimeAlert() Alert { return Alert{Kind: AlertChimeKind} }
func NotifyAlert(title, body string) Alert {
	return Alert{Kind: AlertNotifyKind, Title: title, Body: body}
}
func CueAlert(cue agent.Cue) Alert { return Alert{Kind: AlertCueKind, Cue: cue} }
func (a Alert) String() string     { return fmt.Sprintf("%d:%s:%s:%d", a.Kind, a.Title, a.Body, a.Cue) }
func (a Alert) IsNotify() bool     { return a.Kind == AlertNotifyKind }

// RecordingNotifier records chimes, notifications and mascot cues in one ordered list.
type RecordingNotifier struct {
	mu     sync.Mutex
	alerts []Alert
}

func (n *RecordingNotifier) Alerts() []Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.alerts)
}

func (n *RecordingNotifier) push(a Alert) {
	n.mu.Lock()
	n.alerts = append(n.alerts, a)
	n.mu.Unlock()
}

func (n *RecordingNotifier) Chime()                    { n.push(ChimeAlert()) }
func (n *RecordingNotifier) Notify(title, body string) { n.push(NotifyAlert(title, body)) }
func (n *RecordingNotifier) Cue(cue agent.Cue)         { n.push(CueAlert(cue)) }

// RecordingStatus records every status line ("" is a clear: Rust's None).
type RecordingStatus struct {
	mu    sync.Mutex
	lines []string
}

func (r *RecordingStatus) SetStatus(line string) {
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
}

func (r *RecordingStatus) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

// ---------- store, clock, ids, trust ----------

// MemStore keeps line files in memory. The zero value is ready to use; copies share nothing,
// so pass a pointer.
type MemStore struct {
	mu    sync.Mutex
	files map[string][]string
}

func (m *MemStore) Lines(file string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.files[file])
}

// Has: file was ever written (Rust test reads `files.contains_key`).
func (m *MemStore) Has(file string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[file]
	return ok
}

func (m *MemStore) Load(file string) []string { return m.Lines(file) }

func (m *MemStore) Append(file, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]string{}
	}
	m.files[file] = append(m.files[file], line)
}

func (m *MemStore) Rewrite(file string, lines []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]string{}
	}
	m.files[file] = slices.Clone(lines)
	if m.files[file] == nil {
		m.files[file] = []string{}
	}
}

// ManualClock is a clock the test moves.
type ManualClock struct{ ms atomic.Uint64 }

func ClockAt(ms agent.Millis) *ManualClock {
	c := &ManualClock{}
	c.ms.Store(ms)
	return c
}

func (c *ManualClock) Advance(ms agent.Millis) { c.ms.Add(ms) }
func (c *ManualClock) Set(ms agent.Millis)     { c.ms.Store(ms) }
func (c *ManualClock) NowMs() agent.Millis     { return c.ms.Load() }

// SeqIds gives job_000000000001, job_000000000002, …
type SeqIds struct{ n atomic.Uint64 }

func (s *SeqIds) JobID() agent.JobID { return agent.JobIDFromBits(s.n.Add(1)) }

// RecordingTrust grants every folder and records it.
type RecordingTrust struct {
	mu      sync.Mutex
	granted []string
}

func (t *RecordingTrust) Grant(dir string) ([]string, error) {
	t.mu.Lock()
	t.granted = append(t.granted, dir)
	t.mu.Unlock()
	return []string{dir}, nil
}

func (t *RecordingTrust) Granted() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.granted)
}

// ---------- workers: worktrees, tokens, recipes ----------

// RepoSlug is one (repo, slug) pair recorded by FakeWorktrees.
type RepoSlug struct{ Repo, Slug string }

// FakeWorktrees knows repos by name (ResolveRepo); Ensure answers `<repo>/.worktrees/<slug>`
// and records it. Its gates (HoldEnsure, HoldAudit) block on channels, so a held call is
// "durably blocked" inside a synctest bubble (fake time can pass while it waits).
type FakeWorktrees struct {
	mu         sync.Mutex
	repos      map[string]string
	ensured    []RepoSlug
	failEnsure string
	audited    []RepoSlug
	foreign    []string
	gate       chan struct{}
	entered    atomic.Bool
	auditGate  chan struct{}
}

// FakeWorktreesWithRepo knows one repo, name, at /projects/<name>.
func FakeWorktreesWithRepo(name string) *FakeWorktrees {
	return &FakeWorktrees{repos: map[string]string{name: "/projects/" + name}}
}

// HoldEnsure makes Ensure block until ReleaseEnsure (a git call that hangs).
func (w *FakeWorktrees) HoldEnsure() {
	w.mu.Lock()
	if w.gate == nil {
		w.gate = make(chan struct{})
	}
	w.mu.Unlock()
}

// EnsureIsHeld: an Ensure is now blocked (after HoldEnsure).
func (w *FakeWorktrees) EnsureIsHeld() bool { return w.entered.Load() }

func (w *FakeWorktrees) ReleaseEnsure() {
	w.mu.Lock()
	if w.gate != nil {
		close(w.gate)
		w.gate = nil
	}
	w.mu.Unlock()
}

// HoldAudit makes Audit block until ReleaseAudit.
func (w *FakeWorktrees) HoldAudit() {
	w.mu.Lock()
	if w.auditGate == nil {
		w.auditGate = make(chan struct{})
	}
	w.mu.Unlock()
}

func (w *FakeWorktrees) ReleaseAudit() {
	w.mu.Lock()
	if w.auditGate != nil {
		close(w.auditGate)
		w.auditGate = nil
	}
	w.mu.Unlock()
}

func (w *FakeWorktrees) SetFailEnsure(why string) {
	w.mu.Lock()
	w.failEnsure = why
	w.mu.Unlock()
}

// AddForeign adds a name ForeignSlugs answers.
func (w *FakeWorktrees) AddForeign(slug string) {
	w.mu.Lock()
	w.foreign = append(w.foreign, slug)
	w.mu.Unlock()
}

func (w *FakeWorktrees) Ensured() []RepoSlug {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.ensured)
}

// Audited is every Audit call, in order.
func (w *FakeWorktrees) Audited() []RepoSlug {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.audited)
}

func (w *FakeWorktrees) ResolveRepo(repo string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.repos[repo]; ok {
		return p, nil
	}
	for _, p := range w.repos {
		if p == repo {
			return p, nil
		}
	}
	return "", agent.WorktreeError(fmt.Sprintf("No repository %s under the projects root.", repo))
}

func (w *FakeWorktrees) Ensure(repo, slug string) (string, error) {
	w.mu.Lock()
	gate := w.gate
	if gate != nil {
		w.entered.Store(true)
	}
	w.mu.Unlock()
	if gate != nil {
		<-gate
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failEnsure != "" {
		return "", agent.WorktreeError(w.failEnsure)
	}
	w.ensured = append(w.ensured, RepoSlug{repo, slug})
	return filepath.Join(repo, ".worktrees", slug), nil
}

func (w *FakeWorktrees) Audit(repo, slug string) error {
	w.mu.Lock()
	gate := w.auditGate
	w.mu.Unlock()
	if gate != nil {
		<-gate
	}
	w.mu.Lock()
	w.audited = append(w.audited, RepoSlug{repo, slug})
	w.mu.Unlock()
	return nil
}

func (w *FakeWorktrees) ForeignSlugs(string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.foreign)
}

// FakeTokens issues `tok-<identity>` and records issues and revocations.
type FakeTokens struct {
	mu      sync.Mutex
	issued  []string
	revoked []string
}

func (t *FakeTokens) Issue(identity string) string {
	t.mu.Lock()
	t.issued = append(t.issued, identity)
	t.mu.Unlock()
	return "tok-" + identity
}

func (t *FakeTokens) Revoke(identity string) {
	t.mu.Lock()
	t.revoked = append(t.revoked, identity)
	t.mu.Unlock()
}

func (t *FakeTokens) Issued() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.issued)
}

func (t *FakeTokens) Revoked() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.revoked)
}

// FakeRecipes launches any kind as AgentSpec{Cwd: worktree, Env: [MASCOT_AGENT_TOKEN]}.
type FakeRecipes struct {
	mu        sync.Mutex
	prepared  []agent.WorkerLaunch
	discarded []string
	fail      string
	refusal   *agent.StartRefusal
	ready     *agent.StartReady
}

func (r *FakeRecipes) SetFail(why string) {
	r.mu.Lock()
	r.fail = why
	r.mu.Unlock()
}

func (r *FakeRecipes) SetRefusal(x *agent.StartRefusal) {
	r.mu.Lock()
	r.refusal = x
	r.mu.Unlock()
}

func (r *FakeRecipes) SetReady(x *agent.StartReady) {
	r.mu.Lock()
	r.ready = x
	r.mu.Unlock()
}

func (r *FakeRecipes) Prepared() []agent.WorkerLaunch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.prepared)
}

func (r *FakeRecipes) Discarded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.discarded)
}

func (r *FakeRecipes) Prepare(launch agent.WorkerLaunch) (agent.PreparedWorker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != "" {
		return agent.PreparedWorker{}, fmt.Errorf("%s", r.fail)
	}
	r.prepared = append(r.prepared, launch)
	p := agent.PreparedWorker{Spec: agent.AgentSpec{
		Name: launch.Name, Kind: launch.Kind, Cwd: launch.Worktree,
		Env: []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: launch.Token}},
	}}
	if r.refusal != nil {
		c := *r.refusal
		p.Refusal = &c
	}
	if r.ready != nil {
		c := *r.ready
		p.Ready = &c
	}
	return p, nil
}

func (r *FakeRecipes) Discard(name string) {
	r.mu.Lock()
	r.discarded = append(r.discarded, name)
	r.mu.Unlock()
}

// ---------- command runner ----------

// Ran is one recorded command: its argv and the env it was given.
type Ran struct {
	Argv []string
	Env  []agent.EnvVar
}

type answer struct {
	out agent.CommandOutput
	err error
}

// FakeRunner records every command and answers Run from a script keyed by the argv joined
// with spaces.
type FakeRunner struct {
	mu      sync.Mutex
	ran     []Ran
	spawned []Ran
	answers map[string][]answer
}

// Answer queues an answer for argv (joined by spaces). The last answer repeats.
func (r *FakeRunner) Answer(argv string, out agent.CommandOutput, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answers == nil {
		r.answers = map[string][]answer{}
	}
	r.answers[argv] = append(r.answers[argv], answer{out, err})
}

// Ok is a successful output with stdout.
func Ok(stdout string) agent.CommandOutput { return agent.CommandOutput{Stdout: stdout} }

func (r *FakeRunner) Ran() []Ran {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ran)
}

func (r *FakeRunner) Spawned() []Ran {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spawned)
}

func (r *FakeRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, Ran{slices.Clone(argv), slices.Clone(env)})
	key := strings.Join(argv, " ")
	q := r.answers[key]
	switch {
	case len(q) > 1:
		r.answers[key] = q[1:]
		return q[0].out, q[0].err
	case len(q) == 1:
		return q[0].out, q[0].err
	}
	return agent.CommandOutput{}, fmt.Errorf("no scripted answer for %q", key)
}

func (r *FakeRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(r, argv, env, deadline)
}

func (r *FakeRunner) SpawnDetached(argv []string, env []agent.EnvVar) error {
	r.mu.Lock()
	r.spawned = append(r.spawned, Ran{slices.Clone(argv), slices.Clone(env)})
	r.mu.Unlock()
	return nil
}

// Interface checks.
var (
	_ agent.SessionHost      = (*FakeSessionHost)(nil)
	_ agent.TerminalLauncher = (*FakeLauncher)(nil)
	_ agent.VoiceLink        = (*RecordingVoiceLink)(nil)
	_ agent.Notifier         = (*RecordingNotifier)(nil)
	_ agent.MascotCue        = (*RecordingNotifier)(nil)
	_ agent.StatusSink       = (*RecordingStatus)(nil)
	_ agent.Store            = (*MemStore)(nil)
	_ agent.Clock            = (*ManualClock)(nil)
	_ agent.IdSource         = (*SeqIds)(nil)
	_ agent.TrustStore       = (*RecordingTrust)(nil)
	_ agent.WorktreeFs       = (*FakeWorktrees)(nil)
	_ agent.TokenIssuer      = (*FakeTokens)(nil)
	_ agent.WorkerRecipes    = (*FakeRecipes)(nil)
	_ agent.CommandRunner    = (*FakeRunner)(nil)
)
