package pix

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// Backoff bounds between reconnect attempts.
const (
	BackoffMin = time.Second
	BackoffMax = 30 * time.Second
)

// Client keeps pix registered with the daemon, reconnecting until ctx ends.
type Client struct {
	Sock         string
	Hello        wire.Msg
	OnCommand    func(wire.Msg) wire.Msg // its reply is sent back as the result
	OnRegistered func(reply wire.Msg)    // Address, and Outdated (#189)
	OnRefused    func(reason string)     // the daemon refused; pix runs standalone from then on
	Resend       func() []wire.Msg       // state to send after each registration

	mu   sync.Mutex
	conn net.Conn
}

func (c *Client) Run(ctx context.Context) {
	wait := BackoffMin
	for ctx.Err() == nil {
		registered, refused := c.connect(ctx)
		if refused {
			return
		}
		if registered {
			wait = BackoffMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > BackoffMax {
			wait = BackoffMax
		}
	}
}

// connect runs one connection until it drops.
func (c *Client) connect(ctx context.Context) (registered, refused bool) {
	conn, err := net.DialTimeout("unix", c.Sock, 2*time.Second)
	if err != nil {
		return false, false
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c.mu.Lock()
	hello := c.Hello
	c.mu.Unlock()
	if wire.Encode(conn, hello) != nil {
		return false, false
	}
	dec := wire.NewDecoder(conn)
	reply, err := dec.Next()
	if err != nil {
		return false, false
	}
	if reply.Kind == wire.KindRefused {
		if c.OnRefused != nil {
			c.OnRefused(reply.Reason)
		}
		return false, true
	}
	c.mu.Lock()
	c.conn = conn
	c.Hello.Address = reply.Address // asked for again after a reconnect
	c.mu.Unlock()
	if c.OnRegistered != nil {
		c.OnRegistered(reply)
	}
	if c.Resend != nil {
		for _, m := range c.Resend() {
			c.Send(m)
		}
	}
	for {
		m, err := dec.Next()
		if err != nil {
			break
		}
		if m.Kind == wire.KindRefused {
			// Refused after registering (another pix holds this conversation): stop for good.
			c.mu.Lock()
			c.conn = nil
			c.mu.Unlock()
			if c.OnRefused != nil {
				c.OnRefused(m.Reason)
			}
			return false, true
		}
		if m.Kind == wire.KindCommand {
			go func(m wire.Msg) {
				r := c.OnCommand(m)
				r.Kind, r.ID = wire.KindResult, m.ID
				c.Send(r)
			}(m)
		}
	}
	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()
	return true, false
}

// SetSessionID makes the next registration carry the agent's session id.
func (c *Client) SetSessionID(id string) {
	c.mu.Lock()
	c.Hello.SessionID = id
	c.mu.Unlock()
}

// Send writes m when connected; otherwise it is dropped (Resend restores the state).
func (c *Client) Send(m wire.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wire.Encode(c.conn, m)
	}
}
