package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sihekuang/deskpixie/core/packs"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Handler serves one dp_call method. args is never empty (absent args arrive as `{}`).
// A nil result encodes as `{}`.
type Handler func(args json.RawMessage) (any, error)

// handlers is a method registry. Later registrations replace earlier ones.
type handlers struct {
	mu sync.RWMutex
	m  map[string]Handler
}

func (h *handlers) set(method string, fn Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]Handler{}
	}
	if fn == nil {
		delete(h.m, method)
		return
	}
	h.m[method] = fn
}

func (h *handlers) get(method string) (Handler, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn, ok := h.m[method]
	return fn, ok
}

// pkgHandlers are methods registered with the package-level Register; every engine serves
// them unless it has its own handler of the same name.
var pkgHandlers handlers

// Register adds a method every engine serves (package-level; engine registrations win).
// Packages call it while the engine is built.
func Register(method string, fn Handler) { pkgHandlers.set(method, fn) }

func unregisterPackage(method string) { pkgHandlers.set(method, nil) }

// Register adds (or replaces) a method on this engine. The engine pre-registers
// temporary `app.state` and `app.action` handlers; P14 replaces them this way.
func (e *Engine) Register(method string, fn Handler) { e.calls.set(method, fn) }

// Call runs method with args (JSON; empty = `{}`) and returns the envelope
// `{"ok":<result>}` or `{"error":"<msg>"}`. Unknown methods yield
// `{"error":"unknown method <method>"}`. A panicking handler is reported as an error.
func (e *Engine) Call(method string, args []byte) []byte {
	fn, ok := e.calls.get(method)
	if !ok {
		fn, ok = pkgHandlers.get(method)
	}
	return invoke(method, fn, ok, args)
}

func invoke(method string, fn Handler, ok bool, args []byte) (out []byte) {
	if !ok {
		return errEnvelope("unknown method " + method)
	}
	args = bytes.TrimSpace(args)
	if len(args) == 0 {
		args = []byte("{}")
	}
	if !json.Valid(args) {
		return errEnvelope("invalid args JSON for " + method)
	}
	defer func() {
		if r := recover(); r != nil {
			out = errEnvelope(fmt.Sprintf("%s: panic: %v", method, r))
		}
	}()
	res, err := fn(json.RawMessage(args))
	if err != nil {
		return errEnvelope(err.Error())
	}
	if res == nil {
		return []byte(`{"ok":{}}`)
	}
	b, err := json.Marshal(struct {
		OK any `json:"ok"`
	}{res})
	if err != nil {
		return errEnvelope(fmt.Sprintf("%s: encode result: %v", method, err))
	}
	return b
}

func errEnvelope(msg string) []byte {
	b, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{msg})
	return b
}

// --- temporary app.* handlers (replaced by P14's app core) ---

// DefaultScale is the mascot scale app.state reports until settings own it (Rust default).
const DefaultScale = 8

// AppState is the tray model returned by `app.state` (plan "Shared contract").
type AppState struct {
	State        string       `json:"state"`
	Packs        []PackRef    `json:"packs"`
	Pack         string       `json:"pack"`
	Scale        int          `json:"scale"`
	Fade         bool         `json:"fade"`
	Talking      bool         `json:"talking"`
	CanvasRecent []CanvasPage `json:"canvasRecent"`
	Agent        AgentState   `json:"agent"`
	StatusLines  []string     `json:"statusLines"`
	Hotkey       string       `json:"hotkey"`
}

// PackRef is one Tray ▸ Mascot row.
type PackRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CanvasPage is one Tray ▸ Canvas ▸ Recent row.
type CanvasPage struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// AgentState drives Tray ▸ Agent (shown only when Enabled).
type AgentState struct {
	Enabled bool     `json:"enabled"`
	Jobs    []JobRef `json:"jobs"`
}

// JobRef is one agent job row; Label is preformatted ("<task> — <status>, <age>").
type JobRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// trayModel is what the bridge itself knows about the tray: the mascot state, the voice
// status line and the discovered packs.
type trayModel struct {
	mu     sync.Mutex
	state  string
	status string // "" = no voice status line
	packs  []PackRef
	pack   string
}

func (e *Engine) registerTempHandlers() {
	e.Register("app.state", func(json.RawMessage) (any, error) { return e.appState(), nil })
	e.Register("app.action", e.tempAction)
}

func (e *Engine) appState() AppState {
	e.tray.mu.Lock()
	defer e.tray.mu.Unlock()
	s := AppState{
		State:        e.tray.state,
		Packs:        append([]PackRef{}, e.tray.packs...),
		Pack:         e.tray.pack,
		Scale:        DefaultScale,
		CanvasRecent: []CanvasPage{},
		Agent:        AgentState{Jobs: []JobRef{}},
		StatusLines:  []string{},
	}
	if e.tray.status != "" {
		s.StatusLines = append(s.StatusLines, e.tray.status)
	}
	return s
}

// tempAction handles only `state:<name>` (the Phase 3 debug state switcher); every other
// menu id fails until P14 lands.
func (e *Engine) tempAction(args json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if name, ok := strings.CutPrefix(in.ID, "state:"); ok {
		for _, s := range packs.StateNames {
			if s == name {
				e.emit(voice.UIEvent{Kind: "state", State: name})
				return nil, nil
			}
		}
		return nil, fmt.Errorf("unknown state %q", name)
	}
	return nil, fmt.Errorf("app.action %q not available yet", in.ID)
}

// observe updates the tray model from an outgoing UI event and reports whether the tray
// must be refreshed.
func (e *Engine) observe(ev voice.UIEvent) bool {
	e.tray.mu.Lock()
	defer e.tray.mu.Unlock()
	switch ev.Kind {
	case "state":
		if ev.State == e.tray.state {
			return false
		}
		e.tray.state = ev.State
	case "status", "error":
		if ev.Text == e.tray.status {
			return false
		}
		e.tray.status = ev.Text
	default:
		return false
	}
	return true
}

// setPacks records the discovered packs (sorted by id) and the active one, then refreshes
// the tray.
func (e *Engine) setPacks(found map[string]packs.Pack, active string) {
	refs := make([]PackRef, 0, len(found))
	for id, p := range found {
		refs = append(refs, PackRef{ID: id, Name: p.Name})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	e.tray.mu.Lock()
	e.tray.packs, e.tray.pack = refs, active
	e.tray.mu.Unlock()
	e.pushTray()
}

func (e *Engine) pushTray() { e.q.push("tray", []byte(`{"kind":"tray"}`)) }

// Call: see Engine.Call. Before Init only package-level methods are served.
func Call(method string, args []byte) []byte {
	if e := global.Load(); e != nil {
		return e.Call(method, args)
	}
	fn, ok := pkgHandlers.get(method)
	return invoke(method, fn, ok, args)
}
