package openailive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	connectTimeout = 10 * time.Second
	writeTimeout   = 5 * time.Second
	pingEvery      = 10 * time.Second
	idleDeadline   = 30 * time.Second
	pollEvery      = 50 * time.Millisecond
	closeWait      = time.Second
	eventBuffer    = 512
)

var modelRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type model struct {
	baseURL      string
	pingEvery    time.Duration
	idleDeadline time.Duration
	now          func() time.Time
}

// New returns the GPT-Live model. An empty baseURL means DefaultURL.
func New(baseURL string) voice.Model {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &model{baseURL: baseURL, pingEvery: pingEvery, idleDeadline: idleDeadline, now: time.Now}
}

func (m *model) Name() string { return Name }

// Capabilities: GPT-Live decides barge-in itself and can't truncate.
func (m *model) Capabilities() voice.Capabilities {
	return voice.Capabilities{Truncate: false, OwnsTurnTaking: true}
}

func (m *model) Connect(ctx context.Context, cfg voice.SessionConfig, apiKey string) (voice.Session, error) {
	if cfg.Model != "" && !modelRe.MatchString(cfg.Model) {
		return nil, errors.New("gpt-live: invalid model name")
	}
	if cfg.BackendModel != "" && !modelRe.MatchString(cfg.BackendModel) {
		return nil, errors.New("gpt-live: invalid backend model name")
	}
	setupCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(setupCtx, m.baseURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + apiKey}},
	})
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			var e struct {
				Error apiError `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			msg := fmt.Sprintf("gpt-live: connect: http %d %s", resp.StatusCode, e.Error)
			if f, ok := httpFailure(resp.StatusCode); ok {
				return nil, &connectError{msg: msg, f: f}
			}
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("gpt-live: connect: %w", err)
	}
	conn.SetReadLimit(16 << 20)
	fail := func(e error) (voice.Session, error) {
		conn.CloseNow()
		return nil, e
	}
	cd := newCodec()
	cd.client = cfg.ClientDelegation
	body, _ := json.Marshal(sessionStart(cfg, cd.nextID()))
	if err := conn.Write(setupCtx, websocket.MessageText, body); err != nil {
		return fail(fmt.Errorf("gpt-live: connect: %w", err))
	}
	for !cd.ready {
		_, data, err := conn.Read(setupCtx)
		if err != nil {
			return fail(fmt.Errorf("gpt-live: connection failed before the session started: %w", err))
		}
		var m0 serverMsg
		if json.Unmarshal(data, &m0) == nil && m0.Type == "error" {
			return fail(&connectError{msg: "gpt-live: " + m0.Error.String(), f: startupFailure(m0.Error)})
		}
		cd.decode(data, m.now()) // anything before session.started is setup noise
	}

	sctx, scancel := context.WithCancel(context.Background())
	s := &session{
		conn: conn, cd: cd, now: m.now, events: make(chan voice.Event, eventBuffer),
		ctx: sctx, cancel: scancel, done: make(chan struct{}), closed: make(chan struct{}),
		notify: make(chan struct{}, 1),
	}
	s.lastRx.Store(time.Now().UnixNano())
	if err := s.write(cd.timeContext(m.now())); err != nil {
		return fail(err)
	}
	s.events <- voice.Event{Kind: voice.EvReady}
	go s.emitter()
	go s.readLoop()
	go s.clock()
	go s.keepalive(m.pingEvery, m.idleDeadline)
	return s, nil
}

type session struct {
	conn   *websocket.Conn
	now    func() time.Time
	events chan voice.Event
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the read loop has ended
	closed chan struct{} // closed when session.closed arrived (or the read loop ended)

	mu     sync.Mutex // guards cd, the outbox, and write ordering
	cd     *codec
	outbox []voice.Event
	ended  bool          // the read loop is done: the emitter closes events once the outbox drains
	notify chan struct{} // wakes the emitter

	lastRx    atomic.Int64
	closing   atomic.Bool
	closeOnce sync.Once
}

func (s *session) Events() <-chan voice.Event { return s.events }

// queue appends events in order (caller holds mu); the emitter delivers them without blocking
// the reader, the clock or Send.
func (s *session) queue(evs []voice.Event) {
	if len(evs) == 0 {
		return
	}
	s.outbox = append(s.outbox, evs...)
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *session) emitter() {
	for {
		s.mu.Lock()
		batch, ended := s.outbox, s.ended
		s.outbox = nil
		s.mu.Unlock()
		for _, ev := range batch {
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
		if ended && len(batch) == 0 {
			close(s.events)
			return
		}
		if len(batch) == 0 {
			<-s.notify
		}
	}
}

func (s *session) finish() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *session) readLoop() {
	defer close(s.done)
	defer s.finish()
	defer s.closeOnce.Do(func() { close(s.closed) })
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.cancel()
			s.conn.CloseNow()
			if !s.closing.Load() {
				f := voice.Failure{Kind: voice.FailConnectionLost}
				s.mu.Lock()
				s.queue([]voice.Event{{Kind: voice.EvError, Fatal: true, Message: "gpt-live: session ended: server closed", Failure: &f}})
				s.mu.Unlock()
			}
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &head)
		s.mu.Lock()
		evs := s.cd.decode(data, s.now())
		if !s.closing.Load() {
			s.queue(evs)
		}
		s.mu.Unlock()
		if head.Type == "session.closed" {
			s.closeOnce.Do(func() { close(s.closed) })
			if s.closing.Load() {
				return
			}
		}
	}
}

// clock polls the codec's turn timing.
func (s *session) clock() {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		if !s.closing.Load() {
			now := s.now()
			s.queue(s.cd.poll(now))
			_ = s.write(s.cd.timeContext(now)) // a failed write ends the session
		}
		s.mu.Unlock()
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
			s.cancel()
			s.conn.CloseNow()
			return
		}
		pctx, pcancel := context.WithTimeout(s.ctx, min(writeTimeout, idle))
		err := s.conn.Ping(pctx)
		pcancel()
		if err != nil {
			if s.ctx.Err() == nil {
				s.cancel()
				s.conn.CloseNow()
			}
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
	}
}

func (s *session) write(msgs []obj) error {
	for _, msg := range msgs {
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
		err = s.conn.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			s.cancel()
			s.conn.CloseNow()
			return fmt.Errorf("gpt-live: write: %w", err)
		}
	}
	return nil
}

func (s *session) Send(c voice.Command) error {
	if c.Kind == voice.CmdClose {
		return s.Close()
	}
	if s.closing.Load() || s.ctx.Err() != nil {
		return errors.New("gpt-live: session closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, evs := s.cd.encode(c, s.now())
	s.queue(evs)
	return s.write(msgs)
}

// Close sends session.close, waits up to closeWait for session.closed (final usage), then
// closes the socket.
func (s *session) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		<-s.done
		return nil
	}
	if s.ctx.Err() == nil {
		s.mu.Lock()
		_ = s.write([]obj{{"type": "session.close", "event_id": s.cd.nextID()}})
		s.mu.Unlock()
		select {
		case <-s.closed:
		case <-time.After(closeWait):
		}
		done := make(chan struct{})
		go func() { s.conn.Close(websocket.StatusNormalClosure, ""); close(done) }()
		select {
		case <-done:
		case <-time.After(closeWait):
		}
	}
	s.cancel()
	s.conn.CloseNow()
	<-s.done
	return nil
}
