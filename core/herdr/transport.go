package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// RPCError is a Herdr error, or a transport failure with one of our own codes
// (socket_unavailable, socket_error, client_timeout, bad_response, socket_closed).
type RPCError struct {
	Code    string
	Message string
}

func (e RPCError) Error() string { return e.Code + ": " + e.Message }

// RPCCode is err's RPCError code, or "" for any other error.
func RPCCode(err error) string {
	var e RPCError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Transport sends one request; it returns the "result" object, or an RPCError.
type Transport interface {
	Request(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// MaxReply: a reply longer than this is a bad_response (a misbehaving peer must not exhaust
// memory). Verbatim from Rust (8 MiB).
const MaxReply = 8 * 1024 * 1024

// RequestTimeout is how long UnixTransport waits for a reply by default. Verbatim from Rust.
const RequestTimeout = 30 * time.Second

// UnixTransport is one JSON request per Unix-socket connection: Herdr answers one line and
// closes.
type UnixTransport struct {
	socket  string
	timeout time.Duration
	seq     atomic.Uint64
}

func NewUnixTransport(socket string) *UnixTransport {
	return &UnixTransport{socket: socket, timeout: RequestTimeout}
}

// WithTimeout sets how long a request may take (connect, write and reply).
func (t *UnixTransport) WithTimeout(d time.Duration) *UnixTransport {
	t.timeout = d
	return t
}

// ParseResponse: {"id":…,"result":…} → result; {"id":…,"error":{code,message}} → RPCError.
func ParseResponse(line string) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return nil, RPCError{"bad_response", "unparseable response"}
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &msg) != nil {
		return nil, RPCError{"bad_response", "no result"}
	}
	if raw, ok := msg["error"]; ok {
		var e map[string]any
		_ = json.Unmarshal(raw, &e)
		s := func(k string) string { v, _ := e[k].(string); return v }
		return nil, RPCError{Code: s("code"), Message: s("message")}
	}
	result, ok := msg["result"]
	if !ok {
		return nil, RPCError{"bad_response", "no result"}
	}
	return result, nil
}

func (t *UnixTransport) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("am_%d", t.seq.Add(1))
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	timedOut := func(err error) bool {
		var ne net.Error
		return ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout())
	}
	timeoutErr := func() error {
		if errors.Is(ctx.Err(), context.Canceled) {
			return RPCError{"cancelled", method + ": cancelled"}
		}
		return RPCError{"client_timeout", method + " timed out"}
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", t.socket)
	if err != nil {
		if timedOut(err) {
			return nil, timeoutErr()
		}
		code := "socket_error"
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			code = "socket_unavailable"
		}
		return nil, RPCError{code, fmt.Sprintf("%s: %v", method, err)}
	}
	defer conn.Close()
	// The deadline covers the write and the reply; a cancelled ctx unblocks them too.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	line, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, RPCError{"socket_error", err.Error()}
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		if timedOut(err) {
			return nil, timeoutErr()
		}
		return nil, RPCError{"socket_error", err.Error()}
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, MaxReply)).ReadString('\n')
	if err != nil && err != io.EOF {
		if timedOut(err) {
			return nil, timeoutErr()
		}
		return nil, RPCError{"socket_error", err.Error()}
	}
	if reply == "" {
		return nil, RPCError{"socket_closed", method + ": closed before a response"}
	}
	if !strings.HasSuffix(reply, "\n") && len(reply) >= MaxReply {
		return nil, RPCError{"bad_response", fmt.Sprintf("%s: reply over %d bytes", method, MaxReply)}
	}
	return ParseResponse(strings.TrimRight(reply, " \t\r\n"))
}
