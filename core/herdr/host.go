package herdr

// Host is the named session's agents over the socket API, and one long-lived event
// subscription re-made whenever an agent starts.
//
// Every (re)subscribe starts with the server replaying its journal. The pump treats the stream
// as a replay from subscription_started until ReplayQuiet passes without an event (or
// ReplayMaxMs, whichever is first), then reconciles with agent.list: an agent that is gone is
// reported exited (once), and a status that changed unseen is sent.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Runtime budgets, verbatim from Rust's host.rs. Rust ties them to the agent runtime's
// budgets with compile-time asserts (ENSURE_BUDGET + PaneBusyLimit + StartTimeout +
// DIALOG_WAIT + POST_START_CHECK < WORKER_START_LIMIT; READY_WINDOW <= PaneBusyLimit +
// StartTimeout + DIALOG_WAIT; WORKER_START_LIMIT < MCP_CALL_LIMIT); the Go agent runtime (P9)
// asserts those relations in its tests.
const (
	// StartTimeout is how long StartAgent waits for the agent's TUI to be ready.
	StartTimeout = 45 * time.Second
	StartPoll    = 250 * time.Millisecond
	// StreamWait is how long StartAgent waits for the event stream to cover the new agent.
	StreamWait = 5 * time.Second
	// ReplayQuiet: a replay is over once the stream has been quiet this long.
	ReplayQuiet = 300 * time.Millisecond
	// PaneBusyLimit: a new tab's shell may take a moment to reach its prompt; until then Herdr
	// refuses agent.start with agent_pane_busy.
	PaneBusyLimit = 10 * time.Second
	// PromptReadyWait is how long Prompt waits for a launch_pending agent before its one retry.
	PromptReadyWait = 10 * time.Second
	// FirstLineTimeout is how long a new subscription may take to say subscription_started.
	FirstLineTimeout = 10 * time.Second
	BackoffMin       = 1 * time.Second
	BackoffMax       = 30 * time.Second
)

// TransportFactory builds the request transport for a socket (tests swap in a scripted one).
type TransportFactory func(socket string) Transport

type liveConn struct {
	client    *Client
	socket    string
	workspace string
}

// Host implements agent.SessionHost over Herdr (registry name "herdr").
type Host struct {
	session string
	// home is the workspace's cwd when it has to be created (the user's home).
	home         string
	supervisor   *Supervisor
	transportFor TransportFactory

	liveMu sync.Mutex // held across attach's calls, like Rust's tokio Mutex
	live   *liveConn

	agentsMu sync.Mutex
	agents   *Agents

	subsMu sync.Mutex
	subs   []*subscriber

	resubscribe chan struct{} // tokio Notify: one stored permit
	// wanted is bumped by start after it records a pane; the pump publishes in covered the
	// highest value whose subscription has finished its replay and reconcile.
	wanted    atomic.Uint64
	coveredMu sync.Mutex
	covered   uint64
	coveredCh chan struct{} // closed and replaced on every change

	pumpMu      sync.Mutex
	pumpStarted bool
	pumpCancel  context.CancelFunc
	done        chan struct{} // closed by Shutdown
	shutdown    sync.Once
	epoch       time.Time

	// paneBusyLimit: how long after tab.create start keeps retrying agent_pane_busy.
	paneBusyLimit time.Duration
	// startTimeout: how long start waits for the agent to be ready (StartTimeout).
	startTimeout time.Duration
	// firstLineTimeout: how long the pump waits for subscription_started before reconnecting.
	firstLineTimeout time.Duration
}

var _ agent.SessionHost = (*Host)(nil)

// NewHost uses a UnixTransport per socket.
func NewHost(session, home string, supervisor *Supervisor) *Host {
	return NewHostWithTransport(session, home, supervisor, func(socket string) Transport { return NewUnixTransport(socket) })
}

func NewHostWithTransport(session, home string, supervisor *Supervisor, transportFor TransportFactory) *Host {
	return &Host{
		session:          session,
		home:             home,
		supervisor:       supervisor,
		transportFor:     transportFor,
		agents:           NewAgents(),
		resubscribe:      make(chan struct{}, 1),
		coveredCh:        make(chan struct{}),
		done:             make(chan struct{}),
		epoch:            time.Now(),
		paneBusyLimit:    PaneBusyLimit,
		startTimeout:     StartTimeout,
		firstLineTimeout: FirstLineTimeout,
	}
}

// Shutdown stops the event pump and closes every Subscribe channel (Rust: dropping the host).
// Idempotent.
func (h *Host) Shutdown() {
	h.shutdown.Do(func() {
		h.pumpMu.Lock()
		if h.pumpCancel != nil {
			h.pumpCancel()
		}
		close(h.done)
		h.pumpMu.Unlock()
	})
}

// IntegrationMissing is true once EnsureSession found the Claude integration missing or
// outdated (the app tells the user to run `herdr integration install claude`; we never
// install it).
func (h *Host) IntegrationMissing() bool { return h.supervisor.IntegrationMissing() }

func (h *Host) withAgents(f func(a *Agents)) {
	h.agentsMu.Lock()
	defer h.agentsMu.Unlock()
	f(h.agents)
}

// forgetLive forgets the cached client and workspace: the next call resolves them again.
func (h *Host) forgetLive() {
	h.liveMu.Lock()
	h.live = nil
	h.liveMu.Unlock()
}

func (h *Host) client(ctx context.Context) (*Client, string, error) {
	if err := h.EnsureSession(ctx); err != nil {
		return nil, "", err
	}
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.live == nil {
		return nil, "", agent.HostError{Kind: agent.HostOther, Detail: "herdr session lost"}
	}
	return h.live.client, h.live.workspace, nil
}

// runningClient is the client only if the session's server is already running: never starts
// one (nil when none runs).
func (h *Host) runningClient(ctx context.Context) (*Client, error) {
	socket, ok, err := h.supervisor.Running(ctx)
	if err != nil || !ok {
		return nil, err
	}
	if err := h.attach(ctx, socket); err != nil {
		return nil, err
	}
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.live == nil {
		return nil, nil
	}
	return h.live.client, nil
}

// attach uses the server at socket: resolves (or creates) the session's workspace and starts
// the event pump, unless that server is the one already in use.
func (h *Host) attach(ctx context.Context, socket string) error {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.live != nil && h.live.socket == socket {
		return nil
	}
	client := NewClient(h.transportFor(socket))
	workspaces, err := client.Workspaces(ctx)
	if err != nil {
		return err
	}
	workspace := ""
	for _, w := range workspaces {
		if w.Label == h.session {
			workspace = w.ID
			break
		}
	}
	if workspace == "" {
		if workspace, err = client.CreateWorkspace(ctx, h.session, h.home); err != nil {
			return err
		}
	}
	list, err := client.Agents(ctx)
	if err != nil {
		return err
	}
	h.remember(list)
	h.startPump(socket, client)
	h.live = &liveConn{client: client, socket: socket, workspace: workspace}
	return nil
}

func (h *Host) remember(list []HerdrAgent) {
	h.withAgents(func(a *Agents) {
		for _, ag := range list {
			if ag.Name == "" {
				continue
			}
			if p, ok := a.PaneOf(ag.Name); !ok || p != ag.PaneID {
				a.Insert(ag.PaneID, ag.Name)
			}
			if ag.Agent != "" {
				a.MarkSeen(ag.PaneID)
			}
		}
	})
}

func (h *Host) startPump(socket string, client *Client) {
	h.pumpMu.Lock()
	defer h.pumpMu.Unlock()
	if h.pumpStarted {
		return
	}
	select {
	case <-h.done:
		return
	default:
	}
	h.pumpStarted = true
	ctx, cancel := context.WithCancel(context.Background())
	h.pumpCancel = cancel
	go h.runPump(ctx, socket, client)
}

func (h *Host) setCovered(gen uint64) {
	h.coveredMu.Lock()
	defer h.coveredMu.Unlock()
	if gen > h.covered {
		h.covered = gen
		close(h.coveredCh)
		h.coveredCh = make(chan struct{})
	}
}

// coverNewPanes resubscribes so the stream covers every recorded pane, and waits (bounded)
// until that subscription's replay and reconcile are done: status changes from then on are
// live.
func (h *Host) coverNewPanes(ctx context.Context) {
	want := h.wanted.Add(1)
	select {
	case h.resubscribe <- struct{}{}:
	default:
	}
	timer := time.NewTimer(StreamWait)
	defer timer.Stop()
	for {
		h.coveredMu.Lock()
		covered, ch := h.covered, h.coveredCh
		h.coveredMu.Unlock()
		if covered >= want {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			log.Printf("agent: herdr events: stream not live after %ds; continuing", int(StreamWait/time.Second))
			return
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}

// agentStartTimeoutMs is agent.start's timeout_ms. Herdr answers agent.start at once
// (launch_pending), but if it ever waited the whole timeout_ms, the reply must still come
// before our transport gives up (RequestTimeout), so it is kept 5 s under that. The ready poll
// in start covers the rest of StartTimeout.
func agentStartTimeoutMs() uint64 {
	return uint64(min(StartTimeout, max(RequestTimeout-5*time.Second, 0)) / time.Millisecond)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// newTab is a tab start made for an agent that has not started yet. Unless the start
// completes it is abandoned: its pane is forgotten without an exit report and the tab closed
// (also when the start's ctx ends, Rust's dropped future).
type newTab struct {
	h      *Host
	client *Client
	pane   string
}

func (t newTab) abandon(ctx context.Context) {
	t.h.withAgents(func(a *Agents) { a.Forget(t.pane) })
	closeQuietly(context.WithoutCancel(ctx), t.client, t.pane)
}

// closeQuietly is a best-effort close of a tab made for an agent that did not start. Logs only
// the error variant (messages may carry paths or names).
func closeQuietly(ctx context.Context, client *Client, pane string) {
	err := client.ClosePane(ctx, pane)
	if err == nil {
		return
	}
	tag := "Other"
	var he agent.HostError
	if errors.As(err, &he) {
		switch he.Kind {
		case agent.HostNoAgent:
			return
		case agent.HostNotFound:
			tag = "NotFound"
		case agent.HostVersionMismatch:
			tag = "VersionMismatch"
		case agent.HostTimeout:
			tag = "Timeout"
		case agent.HostGaveUp:
			tag = "GaveUp"
		case agent.HostExitedAtStart:
			tag = "ExitedAtStart"
		}
	}
	log.Printf("agent: herdr: closing an unstarted tab failed (%s)", tag)
}

// start is StartAgent, or with replaceLive ReplaceAgent. A pane left by an earlier agent of
// this name (whose process exited, or with replaceLive any) is closed only after the new tab
// exists: while it still holds its pane id, the new tab cannot get that id, so a late
// pane_closed for it can never be taken for the new agent.
func (h *Host) start(ctx context.Context, spec agent.AgentSpec, replaceLive bool) (agent.AgentInfo, error) {
	client, workspace, err := h.client(ctx)
	if err != nil {
		return agent.AgentInfo{}, err
	}
	stale, staleLive, hasStale := "", false, false
	old, err := client.GetAgent(ctx, spec.Name)
	switch {
	case err == nil && old.Exited():
		stale, hasStale = old.PaneID, true
	case err == nil && replaceLive:
		stale, staleLive, hasStale = old.PaneID, true, true
	case err == nil || agent.IsHostError(err, agent.HostNoAgent):
	default:
		return agent.AgentInfo{}, err
	}
	pane, err := client.CreateTab(ctx, workspace, spec.Name, spec.Cwd, spec.Env)
	if err != nil && IsWorkspaceNotFound(err) {
		// The server restarted and lost our workspace: resolve it again and retry once.
		h.forgetLive()
		if client, workspace, err = h.client(ctx); err != nil {
			return agent.AgentInfo{}, err
		}
		pane, err = client.CreateTab(ctx, workspace, spec.Name, spec.Cwd, spec.Env)
	}
	if err != nil {
		return agent.AgentInfo{}, err
	}
	// Closes the new tab unless the start completes, including when ctx ends (the runtime's
	// start limit), so an abandoned start never leaks a tab.
	tab := newTab{h: h, client: client, pane: pane}
	tabMade := time.Now()
	if hasStale {
		// A live agent being replaced is not reported as exited: its name lives on.
		if staleLive {
			h.withAgents(func(a *Agents) { a.Forget(stale) })
		}
		// Frees the name for agent.start.
		if err := client.ClosePane(ctx, stale); err != nil && !agent.IsHostError(err, agent.HostNoAgent) {
			if staleLive {
				// Still running: tracked again, and still known to be alive.
				h.withAgents(func(a *Agents) {
					a.Insert(stale, spec.Name)
					a.MarkSeen(stale)
				})
			}
			tab.abandon(ctx)
			return agent.AgentInfo{}, err
		}
	}
	// The new tab's shell may not be at its prompt yet (agent_pane_busy): retry for a while.
	busyUntil := tabMade.Add(h.paneBusyLimit)
	for {
		_, err := client.StartAgent(ctx, spec.Name, spec.Kind.String(), pane, spec.Args, agentStartTimeoutMs())
		if err == nil {
			break
		}
		if IsPaneBusy(err) && time.Now().Before(busyUntil) {
			if sleepCtx(ctx, StartPoll) {
				continue
			}
			err = ctx.Err()
		}
		tab.abandon(ctx)
		return agent.AgentInfo{}, err
	}
	// A reused pane id means its previous agent is gone, even if we missed the close. Known
	// narrow race (left as is): a live status event for the previous agent that is still in
	// flight when this insert happens is credited to the new agent, since Herdr's status
	// events carry no terminal id.
	var gone agent.PaneEvent
	var wasGone bool
	h.withAgents(func(a *Agents) { gone, wasGone = a.Replace(pane, spec.Name) })
	if wasGone {
		h.broadcast(gone)
	}
	deadline := time.Now().Add(h.startTimeout)
	covered := make(chan struct{})
	go func() {
		h.coverNewPanes(ctx)
		close(covered)
	}()
	info, err := h.pollReady(ctx, client, spec, pane, deadline)
	<-covered
	if err != nil {
		// Never started: drop it without an exit report, and don't leak its tab.
		tab.abandon(ctx)
		return agent.AgentInfo{}, err
	}
	return info, nil
}

// pollReady polls agent.get until the new agent is ready. Seen launching or detected on our
// pane: from then on, an agent-less pane that is no longer launching means the process exited
// (e.g. `claude --continue` with nothing to continue), so it fails now rather than at the
// deadline.
func (h *Host) pollReady(ctx context.Context, client *Client, spec agent.AgentSpec, pane string, deadline time.Time) (agent.AgentInfo, error) {
	launched := false
	for {
		a, err := client.GetAgent(ctx, spec.Name)
		switch {
		case err == nil:
			if a.PaneID == pane {
				if a.Agent != "" {
					h.withAgents(func(ag *Agents) { ag.MarkSeen(pane) })
				}
				if launched && a.Exited() {
					return agent.AgentInfo{}, agent.HostError{Kind: agent.HostExitedAtStart, Detail: spec.Kind.String()}
				}
				launched = launched || a.LaunchPending || a.Agent != ""
			}
			// Not while launch_pending: Herdr shows the agent idle a few seconds before it
			// accepts prompts.
			if a.Ready() {
				info, ok := a.Info()
				if !ok {
					return agent.AgentInfo{}, agent.HostError{Kind: agent.HostOther, Detail: "agent lost its name"}
				}
				return info, nil
			}
		case agent.IsHostError(err, agent.HostNoAgent):
		default:
			if ctx.Err() != nil {
				return agent.AgentInfo{}, ctx.Err()
			}
			return agent.AgentInfo{}, err
		}
		if !time.Now().Before(deadline) {
			return agent.AgentInfo{}, agent.HostError{Kind: agent.HostTimeout, Detail: spec.Kind.String() + " did not start"}
		}
		if !sleepCtx(ctx, StartPoll) {
			return agent.AgentInfo{}, ctx.Err()
		}
	}
}

// waitReady polls agent.get until the agent is ready, gone, or limit passes.
func waitReady(ctx context.Context, client *Client, name string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		a, err := client.GetAgent(ctx, name)
		if err != nil || a.Ready() {
			return
		}
		if !sleepCtx(ctx, StartPoll) {
			return
		}
	}
}

// subscriber is one Subscribe channel, unbounded like Rust's mpsc::unbounded_channel.
type subscriber struct {
	mu   sync.Mutex
	q    []agent.PaneEvent
	wake chan struct{}
	out  chan agent.PaneEvent
}

func (s *subscriber) push(ev agent.PaneEvent) {
	s.mu.Lock()
	s.q = append(s.q, ev)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) run(done <-chan struct{}) {
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.q) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-done:
				return
			}
		}
		ev := s.q[0]
		s.q = s.q[1:]
		s.mu.Unlock()
		select {
		case s.out <- ev:
		case <-done:
			return
		}
	}
}

func (h *Host) broadcast(ev agent.PaneEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for _, s := range h.subs {
		s.push(ev)
	}
}

// isSubscriptionStarted: {"result":{"type":"subscription_started"}}, which ParseLine hides.
func isSubscriptionStarted(line string) bool {
	var v struct {
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	return json.Unmarshal([]byte(line), &v) == nil && v.Result.Type == "subscription_started"
}

// eventStream is one events.subscribe connection. Its write half stays open as long as it is
// used: closing it half-closes the socket, and Herdr then ends the subscription (about 1 s
// later).
type eventStream struct {
	conn  net.Conn
	lines chan string // closed at EOF or error
	stop  chan struct{}
}

func (s *eventStream) close() {
	close(s.stop)
	s.conn.Close()
}

func (h *Host) connect(ctx context.Context, socket string, panes []string) (*eventStream, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	req, _ := json.Marshal(map[string]any{"id": "am_events", "method": "events.subscribe", "params": SubscribeParams(panes)})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	s := &eventStream{conn: conn, lines: make(chan string), stop: make(chan struct{})}
	go func() {
		defer close(s.lines)
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				select {
				case s.lines <- strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"):
				case <-s.stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return s, nil
}

// endReplay: the replay is over; events are live; catch up with agent.list.
func (h *Host) endReplay(ctx context.Context, client *Client) {
	var known []Generation
	h.withAgents(func(a *Agents) {
		a.EndReplay()
		known = a.Generations()
	})
	list, err := client.Agents(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("agent: herdr events: agent.list after replay: %v", err)
		}
		return
	}
	// A pane without a detected agent (launching, or its process exited) has no status.
	listed := make([]Listed, 0, len(list))
	for _, a := range list {
		listed = append(listed, Listed{Pane: a.PaneID, Status: a.Status, HasAgent: a.Agent != ""})
	}
	var events []agent.PaneEvent
	h.withAgents(func(a *Agents) { events = a.Reconcile(known, listed) })
	for _, ev := range events {
		h.broadcast(ev)
	}
}

// runPump is the event loop: subscribe, forward translated events, reconnect with backoff on
// loss and at once when an agent starts.
func (h *Host) runPump(ctx context.Context, socket string, client *Client) {
	backoff := BackoffMin
	// backOff sleeps the current backoff and doubles it; false once the host is shut down.
	backOff := func() bool {
		ok := sleepCtx(ctx, backoff)
		backoff = min(backoff*2, BackoffMax)
		return ok
	}
	nowMs := func() uint64 { return uint64(time.Since(h.epoch) / time.Millisecond) }
	for ctx.Err() == nil {
		generation := h.wanted.Load()
		var panes []string
		h.withAgents(func(a *Agents) { panes = a.Panes() })
		stream, err := h.connect(ctx, socket, panes)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("agent: herdr events: %v; retrying in %ds", err, int(backoff/time.Second))
			if !backOff() {
				return
			}
			continue
		}
		if !h.readStream(ctx, stream, client, generation, &backoff, backOff, nowMs) {
			stream.close()
			return
		}
		stream.close()
	}
}

// readStream reads one subscription until it must be re-made; false once the host is shut
// down.
func (h *Host) readStream(ctx context.Context, stream *eventStream, client *Client, generation uint64,
	backoff *time.Duration, backOff func() bool, nowMs func() uint64) bool {
	// The replay window: from subscription_started until quiet or the cap.
	replaying := false
	var began, last time.Time
	started := false
	firstLineBy := time.Now().Add(h.firstLineTimeout)
	replayMax := time.Duration(ReplayMaxMs) * time.Millisecond
	for {
		// Biased like Rust's select: the first-line timer, then the replay end, then lines.
		now := time.Now()
		if !started && !now.Before(firstLineBy) {
			log.Printf("agent: herdr events: no answer to subscribe in %ds; retrying in %ds",
				int(h.firstLineTimeout/time.Second), int(*backoff/time.Second))
			return backOff()
		}
		var wake time.Time
		if !started {
			wake = firstLineBy
		}
		if replaying {
			endAt := last.Add(ReplayQuiet)
			if limit := began.Add(replayMax); limit.Before(endAt) {
				endAt = limit
			}
			if !now.Before(endAt) {
				replaying = false
				h.endReplay(ctx, client)
				h.setCovered(generation)
				continue
			}
			if wake.IsZero() || endAt.Before(wake) {
				wake = endAt
			}
		}
		var timerC <-chan time.Time
		var timer *time.Timer
		if !wake.IsZero() {
			timer = time.NewTimer(time.Until(wake))
			timerC = timer.C
		}
		line, ok, outcome := h.nextLine(ctx, stream, timerC)
		if timer != nil {
			timer.Stop()
		}
		switch outcome {
		case waitShutdown:
			return false
		case waitResubscribe:
			return true
		case waitTimer:
			continue
		}
		if !ok {
			return backOff()
		}
		now = time.Now()
		if isSubscriptionStarted(line) {
			started = true
			*backoff = BackoffMin
			h.withAgents(func(a *Agents) { a.BeginReplay(nowMs()) })
			replaying, began, last = true, now, now
			continue
		}
		if !started {
			if len(line) > 200 {
				line = line[:200]
			}
			log.Printf("agent: herdr events: subscribe refused: %s", strings.ToValidUTF8(line, ""))
			return backOff()
		}
		if replaying {
			last = now
		}
		if raw, ok := ParseLine(line); ok {
			var ev agent.PaneEvent
			var send bool
			ms := nowMs()
			h.withAgents(func(a *Agents) { ev, send = a.TranslateAt(raw, ms) })
			if send {
				h.broadcast(ev)
			}
		}
	}
}

type waitOutcome int

const (
	waitLine waitOutcome = iota
	waitTimer
	waitResubscribe
	waitShutdown
)

// nextLine waits for the stream's next line (ok false at its end), the timer, a resubscribe
// request, or shutdown. Lines come before a resubscribe when both are ready.
func (h *Host) nextLine(ctx context.Context, stream *eventStream, timerC <-chan time.Time) (string, bool, waitOutcome) {
	select {
	case line, ok := <-stream.lines:
		return line, ok, waitLine
	default:
	}
	select {
	case <-ctx.Done():
		return "", false, waitShutdown
	case <-timerC:
		return "", false, waitTimer
	case line, ok := <-stream.lines:
		return line, ok, waitLine
	case <-h.resubscribe:
		return "", false, waitResubscribe
	}
}

// Name is the registry name.
func (h *Host) Name() string { return "herdr" }

func (h *Host) EnsureSession(ctx context.Context) error {
	socket, err := h.supervisor.Ensure(ctx)
	if err != nil {
		return err
	}
	return h.attach(ctx, socket)
}

func (h *Host) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.start(ctx, spec, false)
}

func (h *Host) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.start(ctx, spec, true)
}

func (h *Host) Prompt(ctx context.Context, agentName, text string) error {
	client, _, err := h.client(ctx)
	if err != nil {
		return err
	}
	err = client.Prompt(ctx, agentName, text)
	if IsAgentNotReady(err) {
		// Still launching (e.g. restarted outside StartAgent): wait for it, then once more.
		waitReady(ctx, client, agentName, PromptReadyWait)
		return client.Prompt(ctx, agentName, text)
	}
	return err
}

// SendKeys: with no server running there is no agent to press keys in (and none is started).
func (h *Host) SendKeys(ctx context.Context, agentName string, keys []string) error {
	client, err := h.runningClient(ctx)
	if err != nil {
		return err
	}
	if client == nil {
		return agent.ErrNoAgent(agentName)
	}
	return client.SendKeys(ctx, agentName, keys)
}

func (h *Host) Read(ctx context.Context, agentName string, lines uint16) (string, error) {
	client, _, err := h.client(ctx)
	if err != nil {
		return "", err
	}
	return client.Read(ctx, agentName, lines)
}

// Close: with no server running there is nothing to close (and none is started).
func (h *Host) Close(ctx context.Context, agentName string) error {
	client, err := h.runningClient(ctx)
	if err != nil || client == nil {
		return err
	}
	a, err := client.GetAgent(ctx, agentName)
	if err != nil {
		return err
	}
	return client.ClosePane(ctx, a.PaneID)
}

// CloseExact closes the pane by its id, and only while agentName is still the one in it (pane
// ids are reused).
func (h *Host) CloseExact(ctx context.Context, agentName, pane string) error {
	client, err := h.runningClient(ctx)
	if err != nil || client == nil {
		return err
	}
	a, err := client.GetAgent(ctx, agentName)
	switch {
	case err == nil && a.PaneID == pane:
		return client.ClosePane(ctx, pane)
	case err == nil || agent.IsHostError(err, agent.HostNoAgent):
		return nil
	}
	return err
}

func (h *Host) List(ctx context.Context) ([]agent.AgentInfo, error) {
	client, _, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	list, err := client.Agents(ctx)
	if err != nil {
		return nil, err
	}
	h.remember(list)
	// An agent whose process exited (its pane stays open) is not listed: callers re-create it.
	var out []agent.AgentInfo
	for _, a := range list {
		if a.Exited() {
			continue
		}
		if info, ok := a.Info(); ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// Subscribe returns status changes and exits of named agents, without replayed duplicates.
// The channel is unbounded (never drops) and closes on Shutdown.
func (h *Host) Subscribe() <-chan agent.PaneEvent {
	s := &subscriber{wake: make(chan struct{}, 1), out: make(chan agent.PaneEvent)}
	h.subsMu.Lock()
	h.subs = append(h.subs, s)
	h.subsMu.Unlock()
	go s.run(h.done)
	return s.out
}

// AttachCommand is the argv that attaches a terminal to the session.
func (h *Host) AttachCommand() []string {
	return []string{h.supervisor.Bin(), "session", "attach", h.session}
}
