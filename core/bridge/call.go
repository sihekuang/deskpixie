package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
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

// Register adds (or replaces) a method on this engine. Configure registers the app core's
// methods (settings.*, app.*, mascot.*, canvas.*, audio.*) this way.
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

// Call: see Engine.Call. Before Init only package-level methods are served.
func Call(method string, args []byte) []byte {
	if e := global.Load(); e != nil {
		return e.Call(method, args)
	}
	fn, ok := pkgHandlers.get(method)
	return invoke(method, fn, ok, args)
}
