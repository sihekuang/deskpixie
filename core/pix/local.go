package pix

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// LocalHandler is the parent pix as its own hooks and pix mcp see it.
type LocalHandler interface {
	// Hook handles one hook call; the reply is what the hook prints ("" for nothing).
	Hook(event string, payload map[string]any) string
	SetDescription(text string) error
}

// localDeadline: how long one request to the parent pix may take.
var localDeadline = 10 * time.Second

// ServeLocal listens on path (0600), one request per connection, until the returned stop.
func ServeLocal(path string, h LocalHandler) (func(), error) {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveLocalConn(c, h)
		}
	}()
	return func() { l.Close(); os.Remove(path) }, nil
}

func serveLocalConn(c net.Conn, h LocalHandler) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(localDeadline))
	m, err := wire.NewDecoder(c).Next()
	if err != nil {
		return
	}
	reply := wire.Msg{Kind: wire.KindResult}
	switch m.Event {
	case "hook":
		var payload map[string]any
		json.Unmarshal([]byte(m.Text), &payload)
		if payload == nil {
			payload = map[string]any{}
		}
		reply.Text = h.Hook(m.Status, payload)
	case "description":
		if err := h.SetDescription(m.Description); err != nil {
			reply.Err = err.Error()
		}
	default:
		reply.Err = "unknown request"
	}
	wire.Encode(c, reply)
}

// askLocal sends one request to the parent pix and returns its reply.
func askLocal(sock string, m wire.Msg, timeout time.Duration) (wire.Msg, error) {
	if sock == "" {
		return wire.Msg{}, errors.New("not in a pix session")
	}
	c, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return wire.Msg{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	if err := wire.Encode(c, m); err != nil {
		return wire.Msg{}, err
	}
	return wire.NewDecoder(c).Next()
}
