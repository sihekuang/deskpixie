package pixhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// Name is the wrapper host's registry name.
const Name = "pix"

// UnreachableFor is how long a session whose connection dropped (without exiting) stays
// listed, so a pix that is reconnecting keeps its place.
const UnreachableFor = 10 * time.Second

// CommandLimit bounds one command's round trip to a pix.
const CommandLimit = 10 * time.Second

var errUnsupported = agent.HostError{Kind: agent.HostOther, Detail: "not supported for sessions you started"}

type session struct {
	addr      string
	hello     wire.Msg
	sessionID string
	status    agent.PaneStatus
	desc      Descriptions
	started   time.Time
	active    time.Time // its last message
	conn      net.Conn  // nil while unreachable
	wmu       sync.Mutex
	pending   map[string]chan wire.Msg
	dropTimer *time.Timer
}

// Host is the daemon side of pix: the sessions you started yourself, as a SessionHost. The
// sessions live in their pix processes; the host keeps nothing on disk, and each pix
// registers again after the app restarts.
type Host struct {
	sock           string
	taken          func() []string
	unreachableFor time.Duration

	mu       sync.Mutex
	l        net.Listener
	sessions map[string]*session // by address
	subs     []chan agent.PaneEvent
	changes  chan struct{}
	seq      atomic.Uint64
}

// New is a host listening on sock once Listen is called; taken lists the names already in
// use elsewhere (Pixie's workers), so addresses never collide with them.
func New(sock string, taken func() []string) *Host {
	return &Host{sock: sock, taken: taken, unreachableFor: UnreachableFor, sessions: map[string]*session{}, changes: make(chan struct{}, 1)}
}

// Changes signals that List may have changed (coalesced: one pending signal at most).
func (h *Host) Changes() <-chan struct{} { return h.changes }

func (h *Host) changed() {
	select {
	case h.changes <- struct{}{}:
	default:
	}
}

// Listen opens the socket (its folder 0700, the socket 0600: only your user can connect).
func (h *Host) Listen() error {
	if err := os.MkdirAll(filepath.Dir(h.sock), 0o700); err != nil {
		return err
	}
	os.Remove(h.sock)
	l, err := net.Listen("unix", h.sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(h.sock, 0o600); err != nil {
		l.Close()
		return err
	}
	h.mu.Lock()
	h.l = l
	h.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return nil
}

// Shutdown stops listening. pix sessions keep running and reconnect when the app is back.
func (h *Host) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.l != nil {
		h.l.Close()
		h.l = nil
		os.Remove(h.sock)
	}
	for _, s := range h.sessions {
		if s.conn != nil {
			s.conn.Close()
		}
		if s.dropTimer != nil {
			s.dropTimer.Stop()
		}
	}
	h.sessions = map[string]*session{}
}

func (h *Host) serve(c net.Conn) {
	defer c.Close()
	dec := wire.NewDecoder(c)
	hello, err := dec.Next()
	if err != nil {
		return
	}
	if hello.V != wire.Version || hello.Kind != wire.KindRegister {
		wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: fmt.Sprintf(
			"pix speaks protocol %d, DeskPixie %d: reinstall the pix command from Settings", hello.V, wire.Version)})
		return
	}
	s, reason := h.register(hello, c)
	if s == nil {
		wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: reason})
		return
	}
	h.changed()
	s.wmu.Lock()
	err = wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: s.addr})
	s.wmu.Unlock()
	if err == nil {
		for {
			m, err := dec.Next()
			if err != nil {
				break
			}
			if h.onMsg(s, m) {
				return
			}
		}
	}
	h.lost(s, c)
}

// duplicateReason refuses a second pix on a conversation already connected.
const duplicateReason = "this conversation is already connected to DeskPixie from another pix"

func (h *Host) register(hello wire.Msg, c net.Conn) (*session, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hello.SessionID != "" {
		for _, s := range h.sessions {
			if s.sessionID != hello.SessionID {
				continue
			}
			// Only the pix that held it may take back an unreachable session (it asks for its
			// own address); another pix on the same conversation is a duplicate.
			if s.conn != nil || hello.Address != s.addr {
				return nil, duplicateReason
			}
			if s.dropTimer != nil {
				s.dropTimer.Stop()
				s.dropTimer = nil
			}
			s.conn, s.hello, s.active = c, hello, time.Now()
			return s, ""
		}
	}
	taken := append([]string(nil), h.taken()...)
	for a := range h.sessions {
		taken = append(taken, a)
	}
	addr, ok := hello.Address, agent.IsWorkerName(hello.Address) && !slices.Contains(taken, hello.Address)
	if !ok {
		addr, ok = Address(hello.GitRoot, hello.Tool, taken)
	}
	if !ok {
		return nil, "no free name for this session"
	}
	now := time.Now()
	s := &session{addr: addr, hello: hello, sessionID: hello.SessionID, conn: c, pending: map[string]chan wire.Msg{},
		started: now, active: now}
	if hello.Description != "" {
		s.desc.Set(SourceUser, hello.Description, time.Now())
	}
	h.sessions[addr] = s
	return s, ""
}

// onMsg handles one message from pix; true when the session ended.
func (h *Host) onMsg(s *session, m wire.Msg) bool {
	h.mu.Lock()
	s.active = time.Now()
	h.mu.Unlock()
	switch m.Kind {
	case wire.KindResult:
		h.mu.Lock()
		ch := s.pending[m.ID]
		delete(s.pending, m.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case wire.KindBye:
		h.remove(s)
		return true
	case wire.KindEvent:
		switch m.Event {
		case "status":
			st := agent.ParsePaneStatus(m.Status)
			h.mu.Lock()
			s.status = st
			h.mu.Unlock()
			h.broadcast(agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: s.addr, Status: st})
		case "description":
			h.mu.Lock()
			switch m.Source {
			case "auto":
				s.desc.Set(SourceAuto, m.Description, time.Now()) // pix picked it from a prompt you typed
			case "agent":
				s.desc.Set(SourceAgent, m.Description, time.Now())
			}
			h.mu.Unlock()
			h.changed()
		case "session":
			// The id usually arrives after registering (at the first hook), so the duplicate
			// check runs here too: the later pix is refused and runs on without Pixie.
			h.mu.Lock()
			dup := false
			for _, o := range h.sessions {
				dup = dup || (o != s && m.SessionID != "" && o.sessionID == m.SessionID)
			}
			if !dup {
				s.sessionID = m.SessionID
			}
			conn := s.conn
			h.mu.Unlock()
			if dup {
				if conn != nil {
					s.wmu.Lock()
					wire.Encode(conn, wire.Msg{Kind: wire.KindRefused, Reason: duplicateReason})
					s.wmu.Unlock()
				}
				h.remove(s)
				return true
			}
		case "question":
			h.mu.Lock()
			q := agent.SessionQuestion{ID: m.ID, Label: s.label(), App: s.hello.App, Kind: m.Source,
				Tool: m.ToolName, Input: m.Input, Text: m.Text, Options: m.Keys, Body: m.Body, Prompt: m.Prompt}
			h.mu.Unlock()
			h.broadcast(agent.PaneEvent{Kind: agent.PaneEventQuestion, Agent: s.addr, Question: &q})
		case "question-gone":
			h.broadcast(agent.PaneEvent{Kind: agent.PaneEventQuestionGone, Agent: s.addr, Question: &agent.SessionQuestion{ID: m.ID}})
		case "exited":
			h.remove(s)
			return true
		}
	}
	return false
}

// lost: c dropped without saying goodbye. The session stays listed as unreachable for a
// while, so a pix that is reconnecting keeps its address.
func (h *Host) lost(s *session, c net.Conn) {
	dropped := false
	defer func() {
		if dropped {
			h.changed()
		}
	}()
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.conn != c || h.sessions[s.addr] != s {
		return // replaced by a re-registration, or already removed
	}
	s.conn = nil
	dropped = true
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.dropTimer = time.AfterFunc(h.unreachableFor, func() {
		h.mu.Lock()
		gone := s.conn == nil
		h.mu.Unlock()
		if gone {
			h.remove(s)
		}
	})
}

// remove drops s and reports it Exited.
func (h *Host) remove(s *session) {
	h.mu.Lock()
	if h.sessions[s.addr] != s {
		h.mu.Unlock()
		return
	}
	delete(h.sessions, s.addr)
	h.mu.Unlock()
	h.broadcast(agent.PaneEvent{Kind: agent.PaneEventExited, Agent: s.addr})
}

func (h *Host) broadcast(ev agent.PaneEvent) {
	h.changed()
	h.mu.Lock()
	subs := append([]chan agent.PaneEvent(nil), h.subs...)
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // a stuck subscriber misses an event rather than stalling every pix
		}
	}
}

// command sends m to name's pix and waits for its result.
func (h *Host) command(ctx context.Context, name string, m wire.Msg) error {
	h.mu.Lock()
	s := h.sessions[name]
	if s == nil || s.conn == nil {
		h.mu.Unlock()
		return agent.ErrNoAgent(name)
	}
	m.Kind, m.ID = wire.KindCommand, strconv.FormatUint(h.seq.Add(1), 10)
	ch := make(chan wire.Msg, 1)
	s.pending[m.ID] = ch
	conn := s.conn
	h.mu.Unlock()
	s.wmu.Lock()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	err := wire.Encode(conn, m)
	s.wmu.Unlock()
	if err != nil {
		return agent.ErrNoAgent(name)
	}
	ctx, cancel := context.WithTimeout(ctx, CommandLimit)
	defer cancel()
	select {
	case r, ok := <-ch:
		if !ok {
			return agent.ErrNoAgent(name)
		}
		if r.Err != "" {
			return agent.HostError{Kind: agent.HostOther, Detail: r.Err}
		}
		return nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(s.pending, m.ID)
		h.mu.Unlock()
		return agent.HostError{Kind: agent.HostTimeout, Detail: "pix session " + name}
	}
}

// Owns reports whether name is a session you started.
func (h *Host) Owns(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[name] != nil
}

// SetDescription sets your own description of a session ("" clears it).
func (h *Host) SetDescription(name, text string) error {
	h.mu.Lock()
	s := h.sessions[name]
	if s == nil {
		h.mu.Unlock()
		return agent.ErrNoAgent(name)
	}
	if text == "" {
		s.desc.Clear(SourceUser)
	} else {
		s.desc.Set(SourceUser, text, time.Now())
	}
	h.mu.Unlock()
	h.changed()
	return nil
}

func (h *Host) Name() string                        { return Name }
func (h *Host) EnsureSession(context.Context) error { return nil }
func (h *Host) StartAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, errUnsupported
}
func (h *Host) ReplaceAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, errUnsupported
}
func (h *Host) Prompt(ctx context.Context, name, text string) error {
	return h.command(ctx, name, wire.Msg{Command: "prompt", Text: text})
}
func (h *Host) SendKeys(ctx context.Context, name string, keys []string) error {
	return h.command(ctx, name, wire.Msg{Command: "keys", Keys: keys})
}

// AnswerQuestion types your answer to session name's prompt id ("yes", "no" or a menu
// option's number, #105); pix refuses when that prompt is no longer waiting on screen.
func (h *Host) AnswerQuestion(ctx context.Context, name, id, answer string) error {
	return h.command(ctx, name, wire.Msg{Command: "answer", Text: id, Source: answer})
}
func (h *Host) Read(context.Context, string, uint16) (string, error) { return "", errUnsupported }
func (h *Host) Close(ctx context.Context, name string) error {
	return h.command(ctx, name, wire.Msg{Command: "close"})
}

// CloseExact closes name; a session already gone is fine (pix addresses are never reused
// while listed).
func (h *Host) CloseExact(ctx context.Context, name, _ string) error {
	if err := h.Close(ctx, name); err != nil && !agent.IsHostError(err, agent.HostNoAgent) {
		return err
	}
	return nil
}

func (h *Host) List(context.Context) ([]agent.AgentInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]agent.AgentInfo, 0, len(h.sessions))
	for _, s := range h.sessions {
		repo := ""
		if s.hello.GitRoot != "" {
			repo = filepath.Base(s.hello.GitRoot)
		}
		desc := s.desc.Current()
		if desc == "" {
			desc = noTaskYet(s.hello.Tool, repo, s.hello.Cwd)
		}
		out = append(out, agent.AgentInfo{
			Name: s.addr, Pane: "pix:" + s.addr, Cwd: s.hello.Cwd, Status: s.status, Origin: agent.OriginUser,
			User: &agent.UserSession{Tool: s.hello.Tool, Repo: repo, Where: s.hello.Where, App: s.hello.App,
				Description: desc, PastDescriptions: s.desc.Past(), Reachable: s.conn != nil,
				Started: s.started, LastActive: s.active},
		})
	}
	return out, nil
}

// label names s in what Pixie says (h.mu held): its description, else its repo's or folder's
// name, else its tool.
func (s *session) label() string {
	if d := s.desc.Current(); d != "" {
		return d
	}
	for _, p := range []string{s.hello.GitRoot, s.hello.Cwd} {
		if p != "" {
			return filepath.Base(p)
		}
	}
	return s.hello.Tool
}

// noTaskYet describes a session before it has a description: what it is and where, so it is
// never blank ("Claude Code in focus, no task yet").
func noTaskYet(tool, repo, cwd string) string {
	name := map[string]string{"claude": "Claude Code", "codex": "Codex"}[tool]
	if name == "" {
		name = tool
	}
	if repo == "" && cwd != "" {
		repo = filepath.Base(cwd)
	}
	if repo == "" {
		return name + ", no task yet"
	}
	return name + " in " + repo + ", no task yet"
}

func (h *Host) Subscribe() <-chan agent.PaneEvent {
	ch := make(chan agent.PaneEvent, 64)
	h.mu.Lock()
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	return ch
}

func (h *Host) AttachCommand() []string { return nil }

var _ agent.SecondaryHost = (*Host)(nil)
