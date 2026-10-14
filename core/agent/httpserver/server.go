// Package httpserver is the loopback hook and MCP server (Rust: ai-mascot crates/agent-http;
// spec §5.5, §5.6). Claude Code posts its Stop, PermissionRequest and PostToolUse hooks here
// (`type: "http"`) with `Authorization: Bearer $PIXIE_AGENT_TOKEN`, and the coordinator and
// workers call the pixie MCP tools at `POST /mcp` with the same bearer. The bearer says which
// agent is calling; it is checked before the body is read.
//
// Routes: `POST /hooks/{stop,permission,post-tool,state}` and `POST /mcp` (other methods on /mcp and
// /hooks/* answer 405 with `Allow: POST`; any other path is 404). Served on
// 127.0.0.1:<agent.port> (Listen, Serve).
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Runtime is what the server calls on the agent runtime (Rust: the
// `mpsc::UnboundedSender<HookEvent>` the runtime reads).
//
// Deliver hands ev to the runtime and must not block for long (Rust's channel is unbounded).
// It returns false when the runtime is gone (Rust: a send on a closed channel), which the
// server answers with "The app is shutting down.". For Permission and Mcp events the server
// waits on ev.Reply / ev.McpReply (buffered by 1, so a late or abandoned answer never blocks
// the runtime) for at most PermissionLimit / McpCallLimit. Closing the reply channel without
// answering is Rust's dropped oneshot: a deny / "The app did not answer." right away.
//
// ToolInput values and MCP arguments are decoded JSON (map[string]any, []any, string, bool,
// nil, and json.Number for numbers, so integers survive exactly).
type Runtime interface {
	Deliver(ev agent.HookEvent) bool
}

// DeliverFunc adapts a function to Runtime.
type DeliverFunc func(ev agent.HookEvent) bool

func (f DeliverFunc) Deliver(ev agent.HookEvent) bool { return f(ev) }

// ErrNotLoopback: Serve only listens on loopback.
var ErrNotLoopback = errors.New("the hook server only listens on loopback")

// Server routes the hook and MCP endpoints. Make it with NewServer; it is an http.Handler.
type Server struct {
	tokens *Tokens
	rt     Runtime
	// The waits below; tests shorten them (Rust tests pause the tokio clock instead).
	permissionLimit time.Duration
	mcpCallLimit    time.Duration
}

// NewServer is Rust's `router(tokens, events)`.
func NewServer(tokens *Tokens, rt Runtime) *Server {
	return &Server{tokens: tokens, rt: rt, permissionLimit: PermissionLimit, mcpCallLimit: McpCallLimit}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/mcp":
		if r.Method != http.MethodPost {
			// GET (no server-initiated stream) and DELETE (no sessions).
			notAllowed(w)
			return
		}
		s.mcp(w, r)
	case strings.HasPrefix(path, "/hooks/") && len(path) > len("/hooks/") && !strings.Contains(path[len("/hooks/"):], "/"):
		if r.Method != http.MethodPost {
			notAllowed(w)
			return
		}
		s.hook(w, r, path[len("/hooks/"):])
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func notAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "POST")
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// Listen binds 127.0.0.1:port (port 0 picks a free one).
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Serve serves until the listener fails or ctx ends (then it closes the server, cutting any
// waiting hooks, and returns ctx's error). The listener must be bound to a loopback address
// (127.0.0.1:<agent.port>); anything else is refused with ErrNotLoopback.
func Serve(ctx context.Context, ln net.Listener, tokens *Tokens, rt Runtime) error {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return ErrNotLoopback
	}
	srv := &http.Server{
		Handler:           NewServer(tokens, rt),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	err := srv.Serve(ln)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// bearer is the request's resolved agent, or false. A request with an Origin header (a
// browser page) is reported separately by the callers, before this.
func (s *Server) bearer(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(v, "Bearer ")
	if !ok {
		tok = ""
	}
	return s.tokens.Resolve(tok)
}

func hasOrigin(r *http.Request) bool {
	_, ok := r.Header["Origin"]
	return ok
}

// readBody reads at most limit bytes. tooLarge tells the size limit apart from an I/O error.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) (body []byte, tooLarge bool, err error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		return nil, errors.As(err, &mbe), err
	}
	return b, false, nil
}

// decodeJSON is serde_json::from_slice::<Value>: one JSON value, nothing after it. Numbers
// stay json.Number.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

// encodeJSON is serde_json's Value::to_string: compact, keys sorted (Go maps), no HTML escaping.
func encodeJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func json200(w http.ResponseWriter, v any) {
	text, err := encodeJSON(v)
	if err != nil {
		// Only our own values reach here; they always encode.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}
