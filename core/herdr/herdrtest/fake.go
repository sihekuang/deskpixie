// Package herdrtest is a fake Herdr on a Unix socket (port of session-herdr's fake_server.rs),
// speaking protocol 17 (Herdr 0.7.5) or 22 (0.9.x), for offline tests of the real transport,
// client, event pump and herdr.Host. It replays its journal of global pane events on every subscribe, like the real
// server (command-center #57/#69/#70), and reuses freed pane ids. It never runs herdr.
package herdrtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ReadyAfter is how long a started agent takes to be detected, unless ManualReady.
const ReadyAfter = 50 * time.Millisecond

// LaunchWindow is how long a detected agent stays launch_pending (the live window was about
// 3 s), unless HoldPending.
const LaunchWindow = 100 * time.Millisecond

type pane struct {
	workspace string
	name      string
	named     bool
	status    string
	cwd       string
	// prompts typed into the pane, shown by agent.read.
	prompts []string
	// detected: the agent process is detected (running); false while launching and after it exits.
	detected bool
	// launched: detected at least once; agent.list shows the pane from then on.
	launched bool
	// pending: launch_pending, from agent.start until the agent accepts prompts.
	pending bool
	// label is the pane's tab label; title and tokens its reported metadata.
	label  string
	title  string
	tokens map[string]string
}

// tabOf is the id of pane id's tab (each tab here holds one pane).
func tabOf(id string) string { return id + "-tab" }

type subscriber struct {
	q     []string
	panes map[string]bool
	wake  chan struct{}
	// hung: the server ended this stream (HangUp); dead: the connection is gone.
	hung, dead bool
}

func (s *subscriber) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

type request struct {
	method string
	params any
}

// FakeHerdr is the fake server. All methods are safe for concurrent use.
type FakeHerdr struct {
	// Socket is the server's Unix socket path.
	Socket string
	// protocol is the socket protocol it speaks (17: Herdr 0.7.5, 22: 0.9.4 shapes).
	protocol uint32

	mu         sync.Mutex
	workspaces [][2]string // (id, label)
	// defaultTabs: the empty tab "1" each workspace.create opens, by tab id (value: workspace).
	defaultTabs map[string]string
	panes       map[string]*pane
	journal     []string
	subscribers []*subscriber
	requests    []request
	subscribes  int
	// manualReady: agents become ready only through Ready (otherwise after ReadyAfter).
	manualReady bool
	// muteSubscribe: events.subscribe is accepted but never answered.
	muteSubscribe bool
	// listLaunching: agent.list also shows agents still launching.
	listLaunching bool
	// holdCloses: live pane_closed lines wait in held until ReleaseCloses (journaled at once).
	holdCloses bool
	held       []string
	// failAgentStart: agent.start fails.
	failAgentStart bool
	// busyStarts: this many more agent.start calls answer agent_pane_busy.
	busyStarts int
	// holdPending: launch_pending is cleared only by FinishLaunch.
	holdPending bool
	// failCloses: this many more pane.close calls fail.
	failCloses int

	listener net.Listener
	conns    map[net.Conn]bool
	closed   chan struct{}
	once     sync.Once
}

// Start listens on a fresh short socket path (macOS limits them to 104 bytes) and stops the
// server when tb ends.
func Start(tb testing.TB) *FakeHerdr {
	tb.Helper()
	root := os.TempDir()
	if len(root) >= 60 {
		root = "/tmp"
	}
	dir, err := os.MkdirTemp(root, "shh-")
	if err != nil {
		tb.Fatal(err)
	}
	f, err := StartAt(filepath.Join(dir, "herdr.sock"))
	if err != nil {
		os.RemoveAll(dir)
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		f.Close()
		os.RemoveAll(dir)
	})
	return f
}

// StartProtocol is Start speaking protocol (17: Herdr 0.7.5, 22: 0.9.x; any other is reported
// as is, with 0.9.4's shapes).
func StartProtocol(tb testing.TB, protocol uint32) *FakeHerdr {
	f := Start(tb)
	f.mu.Lock()
	f.protocol = protocol
	f.mu.Unlock()
	return f
}

// Protocol is the socket protocol the server speaks.
func (f *FakeHerdr) Protocol() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.protocol
}

// releases are the versions the fake reports per protocol (as `herdr status --json` does).
var releases = map[uint32]string{17: "0.7.5", 22: "0.9.4", 99: "1.2.0"}

func release(p uint32) string {
	if r, ok := releases[p]; ok {
		return r
	}
	return fmt.Sprintf("0.0.%d", p)
}

// StatusJSON is `herdr status --json` for a CLI speaking client while this server runs, in the
// captured shape of that CLI's version (0.7.5 for 17; 0.9.4, with its extra fields, otherwise).
// compatible is whether the two protocols match.
func (f *FakeHerdr) StatusJSON(client uint32) string {
	server := f.Protocol()
	cli := map[string]any{"version": release(client), "channel": "stable", "protocol": client, "binary": "/x/herdr", "session": "deskpixie"}
	srv := map[string]any{"status": "running", "running": true, "version": release(server), "protocol": server,
		"capabilities": map[string]any{"live_handoff": true, "detached_server_daemon": false},
		"compatible":   client == server, "socket": f.Socket, "session": "deskpixie", "restart_needed": false}
	if client != 17 {
		cli["endpoint_protocol_generation"] = 1
		cli["endpoint_capabilities"] = []string{"surface_interest", "health_check"}
		srv["endpoint_compatible"] = client == server
		srv["server_binary_stale"] = false
	}
	b, _ := json.Marshal(map[string]any{"client": cli, "server": srv, "update": map[string]any{"restart_needed": false}})
	return string(b)
}

// StartAt listens on socket (protocol 17).
func StartAt(socket string) (*FakeHerdr, error) {
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	f := &FakeHerdr{Socket: socket, protocol: 17, panes: map[string]*pane{}, defaultTabs: map[string]string{}, listener: l, conns: map[net.Conn]bool{}, closed: make(chan struct{})}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns[c] = true
			f.mu.Unlock()
			go func() {
				f.serve(c)
				c.Close()
				f.mu.Lock()
				delete(f.conns, c)
				f.mu.Unlock()
			}()
		}
	}()
	return f, nil
}

// Close stops the server and drops every connection.
func (f *FakeHerdr) Close() {
	f.once.Do(func() {
		close(f.closed)
		f.listener.Close()
		f.mu.Lock()
		for c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
	})
}

func (f *FakeHerdr) serve(c net.Conn) {
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if line == "" && err != nil {
		return
	}
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return
	}
	var params map[string]any
	_ = json.Unmarshal(req.Params, &params)
	if req.Method == "events.subscribe" {
		f.serveEvents(c, r, params)
		return
	}
	result, code, msg := f.handle(req.Method, params)
	var reply map[string]any
	if code != "" {
		reply = map[string]any{"id": req.ID, "error": map[string]any{"code": code, "message": msg}}
	} else {
		reply = map[string]any{"id": req.ID, "result": result}
	}
	b, _ := json.Marshal(reply)
	c.Write(append(b, '\n'))
}

func (f *FakeHerdr) serveEvents(c net.Conn, r *bufio.Reader, params map[string]any) {
	f.mu.Lock()
	f.subscribes++
	muted := f.muteSubscribe
	f.mu.Unlock()
	if muted {
		// Hold the connection open and say nothing.
		<-f.closed
		return
	}
	if _, err := c.Write([]byte(`{"id":"am_events","result":{"type":"subscription_started"}}` + "\n")); err != nil {
		return
	}
	sub := &subscriber{panes: map[string]bool{}, wake: make(chan struct{}, 1)}
	f.subscribe(params, sub)
	// Like Herdr 0.7.5: the subscription lives while the client keeps its side open; a client
	// half-close (EOF on our read side) ends it.
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := r.Read(buf); err != nil {
				break
			}
		}
		f.mu.Lock()
		sub.dead = true
		f.mu.Unlock()
		sub.signal()
	}()
	for {
		select {
		case <-sub.wake:
		case <-f.closed:
			return
		}
		f.mu.Lock()
		lines := sub.q
		sub.q = nil
		hung, dead := sub.hung, sub.dead
		f.mu.Unlock()
		for _, l := range lines {
			if _, err := c.Write([]byte(l + "\n")); err != nil {
				dead = true
				break
			}
		}
		if hung || dead {
			f.mu.Lock()
			sub.dead = true
			f.mu.Unlock()
			return
		}
	}
}

// Requests returns the params of every request for method, oldest first.
func (f *FakeHerdr) Requests(method string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []any
	for _, r := range f.requests {
		if r.method == method {
			out = append(out, r.params)
		}
	}
	return out
}

func eventLine(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Exit kills the agent process: its pane stays, without an agent, and reports pane_exited.
// Herdr 0.7.5 keeps the pane's name; 0.9.x drops it (agent.get: agent_not_found).
func (f *FakeHerdr) Exit(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[paneID]
	if !ok {
		return
	}
	if f.protocol != 17 {
		p.named = false
	}
	p.detected = false
	p.pending = false
	p.status = "unknown"
	line := eventLine(map[string]any{"data": map[string]any{"pane_id": paneID, "type": "pane_exited", "workspace_id": p.workspace}, "event": "pane_exited"})
	f.journal = append(f.journal, line)
	f.emit(line, "")
}

// ManualReady: agents become ready only when Ready is called.
func (f *FakeHerdr) ManualReady() {
	f.mu.Lock()
	f.manualReady = true
	f.mu.Unlock()
}

// Ready: the launching agent on paneID is detected and idle (its status event is sent).
func (f *FakeHerdr) Ready(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[paneID]
	if !ok || !p.named || p.launched {
		return
	}
	p.detected = true
	p.launched = true
	f.setStatus(paneID, "idle")
	if !f.holdPending {
		time.AfterFunc(LaunchWindow, func() { f.FinishLaunch(paneID) })
	}
}

// BlockOnDialog: the launching agent on paneID is detected and blocked on a dialog (e.g.
// Claude Code's folder trust), still launch_pending (as 0.9.x reports it); its status event is
// sent. FinishLaunch ends the launch.
func (f *FakeHerdr) BlockOnDialog(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[paneID]
	if !ok || !p.named {
		return
	}
	p.detected = true
	p.launched = true
	p.pending = true
	f.setStatus(paneID, "blocked")
}

// DropWorkspaces: the server forgets its workspaces (it restarted).
func (f *FakeHerdr) DropWorkspaces() {
	f.mu.Lock()
	f.workspaces = nil
	clear(f.defaultTabs)
	f.mu.Unlock()
}

// MuteSubscribe: while true, events.subscribe is accepted but never answered.
func (f *FakeHerdr) MuteSubscribe(mute bool) {
	f.mu.Lock()
	f.muteSubscribe = mute
	f.mu.Unlock()
}

// ListLaunching: agent.list shows launching agents too (Herdr may; the host must cope).
func (f *FakeHerdr) ListLaunching() {
	f.mu.Lock()
	f.listLaunching = true
	f.mu.Unlock()
}

// HoldCloses: live pane_closed events are held back until ReleaseCloses (a late close).
func (f *FakeHerdr) HoldCloses() {
	f.mu.Lock()
	f.holdCloses = true
	f.mu.Unlock()
}

// ReleaseCloses sends the held pane_closed events now.
func (f *FakeHerdr) ReleaseCloses() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holdCloses = false
	held := f.held
	f.held = nil
	for _, l := range held {
		f.emit(l, "")
	}
}

// ReadyQuietly: the launching agent on paneID is detected and idle, without a status event.
func (f *FakeHerdr) ReadyQuietly(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.panes[paneID]; ok {
		p.detected = true
		p.launched = true
		p.pending = false
		p.status = "idle"
	}
}

// HoldPending: agents stay launch_pending (detected, idle) until FinishLaunch.
func (f *FakeHerdr) HoldPending() {
	f.mu.Lock()
	f.holdPending = true
	f.mu.Unlock()
}

// FinishLaunch: the agent on paneID accepts prompts (launch_pending clears).
func (f *FakeHerdr) FinishLaunch(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.panes[paneID]; ok {
		p.pending = false
	}
}

// Relaunch: the agent on paneID is restarted outside StartAgent: detected and idle, but
// launch_pending again until FinishLaunch.
func (f *FakeHerdr) Relaunch(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.panes[paneID]; ok {
		p.detected = true
		p.pending = true
		p.status = "idle"
	}
}

// BusyAgentStarts: the next n agent.start calls answer agent_pane_busy, like Herdr 0.7.5 when
// the new tab's shell has not reached its prompt yet.
func (f *FakeHerdr) BusyAgentStarts(n int) {
	f.mu.Lock()
	f.busyStarts = n
	f.mu.Unlock()
}

// FailPaneCloses: the next n pane.close calls fail (the pane stays).
func (f *FakeHerdr) FailPaneCloses(n int) {
	f.mu.Lock()
	f.failCloses = n
	f.mu.Unlock()
}

// FailAgentStart: agent.start fails from now on.
func (f *FakeHerdr) FailAgentStart() {
	f.mu.Lock()
	f.failAgentStart = true
	f.mu.Unlock()
}

// PaneCount is how many panes exist.
func (f *FakeHerdr) PaneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.panes)
}

// PanesNamed is how many panes carry this agent name.
func (f *FakeHerdr) PanesNamed(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.panes {
		if p.named && p.name == name {
			n++
		}
	}
	return n
}

// Subscribes is how many events.subscribe connections were made.
func (f *FakeHerdr) Subscribes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribes
}

// Journal adds a line to the journal without sending it live (an old event the next subscribe
// replays).
func (f *FakeHerdr) Journal(line any) {
	b, _ := json.Marshal(line)
	f.mu.Lock()
	f.journal = append(f.journal, string(b))
	f.mu.Unlock()
}

// Vanish: the pane disappears without any event (as if missed while disconnected).
func (f *FakeHerdr) Vanish(paneID string) {
	f.mu.Lock()
	delete(f.panes, paneID)
	f.mu.Unlock()
}

// SetStatusQuietly changes an agent's status without sending the event.
func (f *FakeHerdr) SetStatusQuietly(paneID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.panes[paneID]; ok {
		p.status = status
	}
}

// HangUp ends every event stream (the client sees EOF and must resubscribe).
func (f *FakeHerdr) HangUp() {
	f.mu.Lock()
	subs := f.subscribers
	f.subscribers = nil
	for _, s := range subs {
		s.hung = true
	}
	f.mu.Unlock()
	for _, s := range subs {
		s.signal()
	}
}

// Chatter sends an unrelated global event to every stream every interval, until Close.
func (f *FakeHerdr) Chatter(every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-f.closed:
				return
			case <-t.C:
			}
			line := eventLine(map[string]any{"data": map[string]any{"pane": map[string]any{"pane_id": "w9:p99", "workspace_id": "w9"}, "type": "pane_created"}, "event": "pane_created"})
			f.mu.Lock()
			f.emit(line, "")
			f.mu.Unlock()
		}
	}()
}

func (f *FakeHerdr) subscribe(params map[string]any, sub *subscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	subs, _ := params["subscriptions"].([]any)
	for _, s := range subs {
		if m, ok := s.(map[string]any); ok {
			if p, ok := m["pane_id"].(string); ok {
				sub.panes[p] = true
			}
		}
	}
	sub.q = append(sub.q, f.journal...)
	f.subscribers = append(f.subscribers, sub)
	sub.signal()
}

// emit: global events go to everyone; status events only to streams subscribed to that pane.
// Callers hold f.mu.
func (f *FakeHerdr) emit(line, statusPane string) {
	kept := f.subscribers[:0]
	for _, s := range f.subscribers {
		if s.dead {
			continue
		}
		if statusPane == "" || s.panes[statusPane] {
			s.q = append(s.q, line)
			s.signal()
		}
		kept = append(kept, s)
	}
	f.subscribers = kept
}

// setStatus: callers hold f.mu.
func (f *FakeHerdr) setStatus(paneID, status string) {
	p := f.panes[paneID]
	p.status = status
	line := eventLine(map[string]any{"data": map[string]any{"agent": "claude", "agent_status": status, "pane_id": paneID, "workspace_id": p.workspace},
		"event": "pane.agent_status_changed"})
	f.emit(line, paneID)
}

// agentJSON is the captured shapes (0.9.x adds terminal_id, display_agent, state_change_seq and
// completion_seq, which the adapter ignores): a detected agent carries "agent": "claude"; a launching
// one has no agent field and launch_pending: true; then it is detected (agent, idle) but still
// launch_pending with no interactive_ready, then ready; an exited one keeps its pane and name
// but loses the agent field.
func (f *FakeHerdr) agentJSON(id string, p *pane) map[string]any {
	v := agentJSON(id, p)
	if f.protocol != 17 {
		v["terminal_id"] = "term_" + id
		v["display_agent"] = v["agent"]
		v["state_change_seq"] = 1
		v["completion_seq"] = nil
	}
	return v
}

func agentJSON(id string, p *pane) map[string]any {
	var name any
	if p.named {
		name = p.name
	}
	v := map[string]any{"name": name, "agent_status": p.status, "workspace_id": p.workspace,
		"tab_id": tabOf(id), "pane_id": id, "focused": false, "cwd": p.cwd, "revision": 1}
	if p.detected {
		v["agent"] = "claude"
		if !p.pending {
			v["interactive_ready"] = true
		}
	}
	if p.pending {
		v["launch_pending"] = true
	}
	if p.title != "" {
		v["title"] = p.title
	}
	if len(p.tokens) > 0 {
		v["tokens"] = maps.Clone(p.tokens)
	}
	return v
}

func (f *FakeHerdr) sortedPaneIDs() []string {
	ids := make([]string, 0, len(f.panes))
	for id := range f.panes {
		ids = append(ids, id)
	}
	sort.Strings(ids) // Rust's BTreeMap order
	return ids
}

func (f *FakeHerdr) find(target string) (string, string, string) {
	for _, id := range f.sortedPaneIDs() {
		p := f.panes[id]
		if (p.named && p.name == target) || id == target {
			return id, "", ""
		}
	}
	return "", "agent_not_found", fmt.Sprintf("agent target %s not found", target)
}

// active is find, refusing an agent still launch_pending like Herdr 0.7.5 does (0.9.x reads
// its screen and takes keys then: screenOnly).
func (f *FakeHerdr) active(target string, screenOnly bool) (string, string, string) {
	id, code, msg := f.find(target)
	if code != "" {
		return id, code, msg
	}
	if f.panes[id].pending && !(screenOnly && f.protocol != 17) {
		return "", "agent_not_ready", fmt.Sprintf("agent %s is not an active named agent", target)
	}
	return id, "", ""
}

func (f *FakeHerdr) handle(method string, p map[string]any) (result any, code, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request{method, p})
	str := func(k string) string { s, _ := p[k].(string); return s }
	switch method {
	case "workspace.list":
		list := []any{}
		for _, w := range f.workspaces {
			list = append(list, map[string]any{"workspace_id": w[0], "label": w[1]})
		}
		return map[string]any{"type": "workspace_list", "workspaces": list}, "", ""
	case "workspace.create":
		id := fmt.Sprintf("w%d", len(f.workspaces)+1)
		f.workspaces = append(f.workspaces, [2]string{id, str("label")})
		// Like Herdr, the workspace opens with an empty tab "1".
		tab := id + ":t1"
		f.defaultTabs[tab] = id
		return map[string]any{"type": "workspace_created", "workspace": map[string]any{"workspace_id": id},
			"tab":       map[string]any{"tab_id": tab, "workspace_id": id, "label": "1"},
			"root_pane": map[string]any{"pane_id": id + ":p0", "tab_id": tab}}, "", ""
	case "tab.create":
		ws := str("workspace_id")
		found := false
		for _, w := range f.workspaces {
			found = found || w[0] == ws
		}
		if !found {
			return nil, "workspace_not_found", "no active workspace"
		}
		// Like Herdr, the lowest free pane id is reused.
		var id string
		for n := 1; ; n++ {
			id = fmt.Sprintf("%s:p%d", ws, n)
			if _, taken := f.panes[id]; !taken {
				break
			}
		}
		f.panes[id] = &pane{workspace: ws, status: "unknown", cwd: str("cwd"), label: str("label")}
		line := eventLine(map[string]any{"data": map[string]any{"pane": map[string]any{"pane_id": id, "workspace_id": ws}, "type": "pane_created"}, "event": "pane_created"})
		f.journal = append(f.journal, line)
		f.emit(line, "")
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": tabOf(id), "workspace_id": ws, "label": str("label")},
			"root_pane": map[string]any{"pane_id": id, "tab_id": tabOf(id)}}, "", ""
	case "tab.rename":
		id, ok := f.paneOfTab(str("tab_id"))
		if !ok {
			return nil, "tab_not_found", fmt.Sprintf("tab %s not found", str("tab_id"))
		}
		if id != "" {
			f.panes[id].label = str("label")
		}
		return map[string]any{"type": "tab_info", "tab": map[string]any{"tab_id": str("tab_id"), "label": str("label")}}, "", ""
	case "tab.close":
		tab := str("tab_id")
		id, ok := f.paneOfTab(tab)
		if !ok {
			return nil, "tab_not_found", fmt.Sprintf("tab %s not found", tab)
		}
		if id == "" {
			delete(f.defaultTabs, tab)
		} else {
			f.closePane(id)
		}
		return map[string]any{"type": "ok"}, "", ""
	case "pane.report_metadata":
		pn, ok := f.panes[str("pane_id")]
		if !ok {
			return nil, "pane_not_found", str("pane_id")
		}
		if b, _ := p["clear_title"].(bool); b {
			pn.title = ""
		}
		if t, ok := p["title"].(string); ok {
			pn.title = cut(t)
		}
		toks, _ := p["tokens"].(map[string]any)
		for k, v := range toks {
			if pn.tokens == nil {
				pn.tokens = map[string]string{}
			}
			if v, ok := v.(string); ok {
				pn.tokens[k] = cut(v)
			} else {
				delete(pn.tokens, k)
			}
		}
		return map[string]any{"type": "ok"}, "", ""
	case "agent.start":
		paneID, name := str("pane_id"), str("name")
		if f.failAgentStart {
			return nil, "launch_failed", "could not start the agent"
		}
		if f.busyStarts > 0 {
			f.busyStarts--
			return nil, "agent_pane_busy", fmt.Sprintf("agent target pane %s is not an available shell", paneID)
		}
		for id, q := range f.panes {
			if id != paneID && q.named && q.name == name {
				return nil, "agent_exists", fmt.Sprintf("agent %s already exists", name)
			}
		}
		pn, ok := f.panes[paneID]
		if !ok {
			return nil, "pane_not_found", paneID
		}
		pn.name, pn.named, pn.pending = name, true, true
		// Like Herdr: the launch is pending, the agent is detected a little later.
		a := f.agentJSON(paneID, pn)
		if !f.manualReady {
			time.AfterFunc(ReadyAfter, func() { f.Ready(paneID) })
		}
		return map[string]any{"type": "agent_started", "agent": a, "argv": []any{}}, "", ""
	case "agent.get":
		id, code, msg := f.find(str("target"))
		if code != "" {
			return nil, code, msg
		}
		return map[string]any{"type": "agent_info", "agent": f.agentJSON(id, f.panes[id])}, "", ""
	case "agent.list":
		// A launching agent is not listed until it is detected.
		list := []any{}
		for _, id := range f.sortedPaneIDs() {
			q := f.panes[id]
			if q.named && (q.launched || f.listLaunching) {
				list = append(list, f.agentJSON(id, q))
			}
		}
		return map[string]any{"type": "agent_list", "agents": list}, "", ""
	case "agent.prompt":
		id, code, msg := f.active(str("target"), false)
		if code != "" {
			return nil, code, msg
		}
		text := str("text")
		f.panes[id].prompts = append(f.panes[id].prompts, text)
		f.setStatus(id, "working")
		// A "Count…" turn runs until Escape; anything else finishes at once.
		if !strings.HasPrefix(text, "Count") {
			f.setStatus(id, "idle")
		}
		return map[string]any{"type": "agent_prompted"}, "", ""
	case "agent.send_keys":
		id, code, msg := f.active(str("target"), true)
		if code != "" {
			return nil, code, msg
		}
		keys, _ := p["keys"].([]any)
		if len(keys) == 1 && keys[0] == "Escape" && f.panes[id].status == "working" {
			f.setStatus(id, "idle")
		}
		return map[string]any{"type": "ok"}, "", ""
	case "agent.read":
		id, code, msg := f.active(str("target"), true)
		if code != "" {
			return nil, code, msg
		}
		text := "line 1\nline 2\n"
		for _, pr := range f.panes[id].prompts {
			text += "❯ " + pr + "\n"
		}
		text += "❯ "
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": id, "text": text}}, "", ""
	case "pane.close":
		id := str("pane_id")
		if f.failCloses > 0 {
			f.failCloses--
			return nil, "close_failed", fmt.Sprintf("could not close %s", id)
		}
		if _, ok := f.panes[id]; !ok {
			return nil, "pane_not_found", id
		}
		f.closePane(id)
		return map[string]any{"type": "ok"}, "", ""
	}
	return nil, "unknown_method", method
}

// closePane closes the pane (and its tab) and reports pane_closed.
func (f *FakeHerdr) closePane(id string) {
	pn := f.panes[id]
	delete(f.panes, id)
	line := eventLine(map[string]any{"data": map[string]any{"pane_id": id, "type": "pane_closed", "workspace_id": pn.workspace}, "event": "pane_closed"})
	f.journal = append(f.journal, line)
	if f.holdCloses {
		f.held = append(f.held, line)
	} else {
		f.emit(line, "")
	}
}

// paneOfTab is the pane in tab ("" for a workspace's empty default tab); false if no such tab.
func (f *FakeHerdr) paneOfTab(tab string) (string, bool) {
	if _, ok := f.defaultTabs[tab]; ok {
		return "", true
	}
	for id := range f.panes {
		if tabOf(id) == tab {
			return id, true
		}
	}
	return "", false
}

// cut is s cut to what Herdr 0.7.x keeps of a metadata value (80 runes).
func cut(s string) string {
	r := []rune(s)
	return string(r[:min(len(r), 80)])
}

// Tabs are the tab labels, the default tabs ("1") first, then by pane id.
func (f *FakeHerdr) Tabs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for range f.defaultTabs {
		out = append(out, "1")
	}
	for _, id := range f.sortedPaneIDs() {
		out = append(out, f.panes[id].label)
	}
	return out
}

// TabLabel is the label of the tab of the agent named name ("" if none).
func (f *FakeHerdr) TabLabel(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, code, _ := f.find(name); code == "" {
		return f.panes[id].label
	}
	return ""
}

// Metadata is the title and tokens reported for the agent named name's pane.
func (f *FakeHerdr) Metadata(name string) (string, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, code, _ := f.find(name); code == "" {
		return f.panes[id].title, maps.Clone(f.panes[id].tokens)
	}
	return "", nil
}
