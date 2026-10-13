// Package openai adapts the OpenAI Realtime (GA) WebSocket API to voice.Model.
// Port of ai-mascot's voice-openai crate.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	Name       = "openai-realtime"
	DefaultURL = "wss://api.openai.com/v1/realtime"

	connectTimeout = 10 * time.Second
	writeTimeout   = 5 * time.Second
	pingEvery      = 10 * time.Second
	idleDeadline   = 30 * time.Second
	eventBuffer    = 512
)

var modelRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type model struct {
	baseURL      string
	pingEvery    time.Duration
	idleDeadline time.Duration
}

// New returns the OpenAI Realtime model. An empty baseURL means DefaultURL.
func New(baseURL string) voice.Model {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &model{baseURL: baseURL, pingEvery: pingEvery, idleDeadline: idleDeadline}
}

func (m *model) Name() string { return Name }

func (m *model) Connect(ctx context.Context, cfg voice.SessionConfig, apiKey string) (voice.Session, error) {
	if !modelRe.MatchString(cfg.Model) {
		return nil, errors.New("openai: invalid model name")
	}
	u, err := url.Parse(m.baseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: bad url: %w", err)
	}
	q := u.Query()
	q.Set("model", cfg.Model)
	u.RawQuery = q.Encode()

	setupCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(setupCtx, u.String(), &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Authorization": {"Bearer " + apiKey}},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: connect: %w", err)
	}
	conn.SetReadLimit(16 << 20)
	fail := func(e error) (voice.Session, error) {
		conn.CloseNow()
		return nil, e
	}
	body, _ := json.Marshal(sessionUpdate(cfg))
	if err := conn.Write(setupCtx, websocket.MessageText, body); err != nil {
		return fail(fmt.Errorf("openai: connect: %w", err))
	}
	dec := &decoder{}
	var early []voice.Event
	for ready := false; !ready; {
		_, data, err := conn.Read(setupCtx)
		if err != nil {
			return fail(fmt.Errorf("openai: connection failed before the session was ready: %w", err))
		}
		for _, ev := range dec.decode(data) {
			switch ev.Kind {
			case voice.EvReady:
				ready = true
			case voice.EvError:
				return fail(fmt.Errorf("openai: %s", ev.Message))
			default:
				early = append(early, ev)
			}
		}
	}

	sctx, scancel := context.WithCancel(context.Background())
	s := &session{
		conn: conn, dec: dec, events: make(chan voice.Event, eventBuffer),
		ctx: sctx, cancel: scancel, done: make(chan struct{}),
	}
	s.lastRx.Store(time.Now().UnixNano())
	go s.readLoop(early)
	go s.keepalive(m.pingEvery, m.idleDeadline)
	return s, nil
}

type session struct {
	conn   *websocket.Conn
	events chan voice.Event
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the read loop has ended

	mu  sync.Mutex // guards dec and serialises encode+write ordering
	dec *decoder

	lastRx    atomic.Int64
	closing   atomic.Bool
	causeOnce sync.Once
	cause     string
}

func (s *session) Events() <-chan voice.Event { return s.events }

// setCause records why the session is ending (first writer wins) and tears the socket down.
func (s *session) setCause(c string) {
	s.causeOnce.Do(func() { s.cause = c })
	s.cancel()
	s.conn.CloseNow()
}

func (s *session) emit(ev voice.Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
		// Still try to deliver the final fatal error without blocking forever.
		if ev.Kind == voice.EvError && ev.Fatal {
			select {
			case s.events <- ev:
			case <-time.After(time.Second):
			}
		}
	}
}

func (s *session) readLoop(early []voice.Event) {
	defer close(s.done)
	defer close(s.events)
	s.emit(voice.Event{Kind: voice.EvReady}) // Ready is always the first event
	for _, ev := range early {
		s.emit(ev)
	}
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			if s.closing.Load() {
				return
			}
			c := "server closed"
			s.causeOnce.Do(func() { s.cause = c })
			s.cancel()
			s.conn.CloseNow()
			s.emit(voice.Event{Kind: voice.EvError, Fatal: true, Message: "openai: session ended: " + s.cause})
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
		s.mu.Lock()
		evs := s.dec.decode(data)
		s.mu.Unlock()
		for _, ev := range evs {
			s.emit(ev)
		}
	}
}

func (s *session) keepalive(every, idle time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, s.lastRx.Load())) >= idle {
			s.setCause("idle timeout")
			return
		}
		to := writeTimeout
		if idle < to {
			to = idle
		}
		pctx, pcancel := context.WithTimeout(s.ctx, to)
		err := s.conn.Ping(pctx)
		pcancel()
		if err != nil {
			if s.ctx.Err() == nil {
				s.setCause("ping failed")
			}
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
	}
}

func (s *session) Send(c voice.Command) error {
	if c.Kind == voice.CmdClose {
		return s.Close()
	}
	if s.closing.Load() || s.ctx.Err() != nil {
		return errors.New("openai: session closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.dec.encode(c) {
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
		err = s.conn.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			s.setCause("write error")
			return fmt.Errorf("openai: write: %w", err)
		}
	}
	return nil
}

func (s *session) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		<-s.done
		return nil
	}
	// Polite close handshake, but never wait long for a peer that does not answer.
	closed := make(chan struct{})
	go func() { s.conn.Close(websocket.StatusNormalClosure, ""); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
	}
	s.cancel()
	s.conn.CloseNow()
	<-s.done
	return nil
}
