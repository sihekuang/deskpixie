// Package gemini adapts the Google Gemini Live API (BidiGenerateContent over WebSocket) to
// voice.Model, registered as provider "gemini-live". Port of ai-mascot's voice-gemini crate.
//
//   - protocol.go: pure wire format ⇄ neutral types, including the events Gemini has no direct
//     equivalent for (documented there).
//   - schema.go: tool JSON Schema → Gemini's OpenAPI-subset Schema.
//   - models.go: default model (gemini-3.8-live) and voice (Puck); OpenAI ids are replaced.
//   - resample.go: 24 kHz (voice core) ⇄ 16 kHz (Gemini input) conversion.
//
// Official sources: https://ai.google.dev/api/live,
// https://ai.google.dev/gemini-api/docs/live-api/get-started-websocket (endpoint, ?key=),
// .../live-api/capabilities, .../live-api/tools, .../live-api/session-management.
//
// Session limits: one connection lasts about 10 minutes and an audio session 15 minutes
// without context compression; the server sends goAway before closing. Conversations here
// are short (they end after seconds of silence), so session resumption is not used: goAway
// is reported as a warning and the close ends the conversation like any lost connection.
//
// The key is only ever in the request URL; every error string and close reason is scrubbed
// of it.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	Name       = "gemini-live"
	DefaultURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

	connectTimeout = 10 * time.Second // handshake plus readiness (setupComplete), overall
	writeTimeout   = 5 * time.Second
	pingEvery      = 10 * time.Second
	idleDeadline   = 30 * time.Second
	pollEvery      = 200 * time.Millisecond // how often time-based events (codec.poll) are checked
)

type model struct {
	baseURL      string
	pingEvery    time.Duration
	idleDeadline time.Duration
	// Each settings substitution is logged once per adapter, not once per conversation.
	warnedModel, warnedVoice atomic.Bool
}

// New returns the Gemini Live model. An empty baseURL means DefaultURL (tests and proxies
// pass their own).
func New(baseURL string) voice.Model { return newModel(baseURL, pingEvery, idleDeadline) }

func newModel(baseURL string, ping, idle time.Duration) *model {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &model{baseURL: baseURL, pingEvery: ping, idleDeadline: idle}
}

func (m *model) Name() string { return Name }

// resolve returns the Gemini model and voice for cfg, logging each replacement the first
// time it happens.
func (m *model) resolve(cfg voice.SessionConfig) voice.SessionConfig {
	resolved, subs := Resolve(cfg)
	for _, s := range subs {
		flag := &m.warnedModel
		if s.Kind == SubVoice {
			flag = &m.warnedVoice
		}
		if !flag.Swap(true) {
			log.Printf("voice-gemini: %s", s.Describe())
		}
	}
	return resolved
}

// validModel: only [A-Za-z0-9._-], so the model id is a clean resource name.
func validModel(model string) bool {
	if model == "" {
		return false
	}
	for _, c := range model {
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// queryEscape percent-encodes everything but RFC 3986 unreserved characters.
func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// scrub blanks the key (raw or URL-encoded) out of text.
func scrub(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, key, "***"), queryEscape(key), "***")
}

// closeInfo is the code and reason of a close frame in err, if any.
func closeInfo(err error) (int, string, bool) {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return int(ce.Code), ce.Reason, true
	}
	return 0, "", false
}

func (m *model) Connect(ctx context.Context, cfg voice.SessionConfig, apiKey string) (voice.Session, error) {
	cfg = m.resolve(cfg)
	if !validModel(cfg.Model) {
		return nil, errors.New("gemini: connect: invalid model name")
	}
	fail := func(msg string) error { return errors.New("gemini: connect: " + scrub(msg, apiKey)) }
	setupCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	timedOut := func(err error) error {
		if errors.Is(setupCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return fail("timed out")
		}
		return fail(err.Error())
	}

	conn, _, err := websocket.Dial(setupCtx, m.baseURL+"?key="+queryEscape(apiKey), nil)
	if err != nil {
		return nil, timedOut(err)
	}
	conn.SetReadLimit(16 << 20)
	abort := func(e error) (voice.Session, error) {
		conn.CloseNow()
		return nil, e
	}
	if err := conn.Write(setupCtx, websocket.MessageText, []byte(marshal(setupMessage(cfg)))); err != nil {
		return abort(timedOut(err))
	}
	// Wait for setupComplete; anything else seen first goes through the codec.
	c := newCodec()
	var early []voice.Event
	for ready := false; !ready; {
		_, data, err := conn.Read(setupCtx)
		if err != nil {
			if code, reason, ok := closeInfo(err); ok {
				return abort(fail(fmt.Sprintf("connection closed before the session was ready: %d %s", code, reason)))
			}
			return abort(timedOut(err))
		}
		for _, ev := range c.decode(string(data)) {
			switch {
			case ev.Kind == voice.EvReady:
				ready = true
			case ev.Kind == voice.EvError && ev.Fatal:
				return abort(fail(ev.Message))
			default:
				early = append(early, ev)
			}
		}
	}

	sctx, scancel := context.WithCancel(context.Background())
	s := &session{
		conn: conn, codec: c, key: apiKey,
		ctx: sctx, cancel: scancel,
		events:   make(chan voice.Event),
		wake:     make(chan struct{}, 1),
		abandon:  make(chan struct{}),
		readDone: make(chan struct{}),
		pumpDone: make(chan struct{}),
		trace:    traceEnabled(),
		opened:   time.Now(),
	}
	s.lastRx.Store(time.Now().UnixNano())
	// Ready is always the first event the caller sees.
	s.queue = append([]voice.Event{{Kind: voice.EvReady}}, early...)
	go s.pump()
	go s.readLoop()
	go s.timers(m.pingEvery, m.idleDeadline)
	return s, nil
}

// traceEnabled: GEMINI_TRACE=1 logs every server message's shape and timing (audio elided,
// the API key scrubbed).
func traceEnabled() bool {
	v := os.Getenv("GEMINI_TRACE")
	return v != "" && v != "0"
}

// session pumps commands out and events in. Events go through an unbounded queue drained by
// one goroutine, so producers (read loop, Send, poll timer) never block while holding the
// codec lock and the caller sees them in the order the codec made them.
type session struct {
	conn   *websocket.Conn
	key    string
	ctx    context.Context
	cancel context.CancelFunc
	trace  bool
	opened time.Time

	mu    sync.Mutex // guards codec, queue, ended; serialises encode+write ordering
	codec *codec
	queue []voice.Event
	ended bool // no more events will be queued

	events   chan voice.Event
	wake     chan struct{}
	abandon  chan struct{} // closed by Close: the caller wants no further events
	readDone chan struct{}
	pumpDone chan struct{}

	lastRx    atomic.Int64
	closing   atomic.Bool
	causeOnce sync.Once
	closeOnce sync.Once
}

func (s *session) Events() <-chan voice.Event { return s.events }

// push queues events; the caller holds s.mu.
func (s *session) push(evs ...voice.Event) {
	if len(evs) == 0 || s.ended {
		return
	}
	s.queue = append(s.queue, evs...)
	s.notify()
}

func (s *session) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pump delivers queued events and closes the stream once the session ended and the queue is
// drained (or the caller called Close).
func (s *session) pump() {
	defer close(s.pumpDone)
	defer close(s.events)
	for {
		s.mu.Lock()
		q, ended := s.queue, s.ended
		s.queue = nil
		s.mu.Unlock()
		for _, ev := range q {
			select {
			case s.events <- ev:
			case <-s.abandon:
				return
			}
		}
		if ended && len(q) == 0 {
			return
		}
		if len(q) > 0 {
			continue
		}
		select {
		case <-s.wake:
		case <-s.abandon:
			return
		}
	}
}

// end records why the session ends (first cause wins), queues a final event if any, and
// tears the socket down.
func (s *session) end(cause string, final ...voice.Event) {
	s.causeOnce.Do(func() {
		if !s.closing.Load() {
			log.Printf("voice-gemini: session ended: %s", cause)
		}
		s.mu.Lock()
		s.push(final...)
		s.ended = true
		s.notify()
		s.mu.Unlock()
		s.cancel()
		s.conn.CloseNow()
	})
}

func (s *session) readLoop() {
	defer close(s.readDone)
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			if s.closing.Load() {
				s.end("closed by caller")
				return
			}
			if code, reason, ok := closeInfo(err); ok {
				reason = scrub(reason, s.key)
				var final []voice.Event
				if ev, ok := closeEvent(code, reason); ok {
					final = append(final, ev)
				}
				s.end(fmt.Sprintf("server closed (%d) %s", code, reason), final...)
				return
			}
			if s.ctx.Err() != nil {
				s.end("closed") // idle timeout or write failure already recorded the cause
				return
			}
			s.end("read error")
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
		text := string(data)
		if s.trace {
			log.Printf("voice-gemini trace +%d ms: %s", time.Since(s.opened).Milliseconds(), scrub(traceLine(text), s.key))
		}
		s.mu.Lock()
		s.push(s.codec.decodeAt(text, time.Now())...)
		replies := s.codec.takeOutgoing()
		err = s.writeLocked(replies)
		s.mu.Unlock()
		if err != nil {
			s.end("write error")
			return
		}
	}
}

// writeLocked sends client messages; the caller holds s.mu.
func (s *session) writeLocked(msgs []obj) error {
	for _, m := range msgs {
		wctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
		err := s.conn.Write(wctx, websocket.MessageText, []byte(marshal(m)))
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// timers runs codec.poll every pollEvery and the keepalive ping / idle deadline.
func (s *session) timers(every, idle time.Duration) {
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	ping := time.NewTicker(every)
	defer ping.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-poll.C:
			s.mu.Lock()
			s.push(s.codec.poll(time.Now())...)
			s.mu.Unlock()
		case <-ping.C:
			if time.Since(time.Unix(0, s.lastRx.Load())) >= idle {
				s.end("idle timeout")
				return
			}
			pctx, cancel := context.WithTimeout(s.ctx, min(writeTimeout, idle))
			err := s.conn.Ping(pctx)
			cancel()
			if err != nil {
				if s.ctx.Err() == nil {
					s.end("ping failed")
				}
				return
			}
			s.lastRx.Store(time.Now().UnixNano())
		}
	}
}

func (s *session) Send(c voice.Command) error {
	if c.Kind == voice.CmdClose {
		return s.Close()
	}
	if s.closing.Load() || s.ctx.Err() != nil {
		return errors.New("gemini: session closed")
	}
	s.mu.Lock()
	enc := s.codec.encode(c)
	s.push(enc.events...)
	err := s.writeLocked(enc.messages)
	s.mu.Unlock()
	if err != nil {
		s.end("write error")
		return fmt.Errorf("gemini: write: %s", scrub(err.Error(), s.key))
	}
	return nil
}

// Close ends the session: a polite close handshake (never waiting long), then the event
// stream closes. Safe to call more than once.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		closed := make(chan struct{})
		go func() { _ = s.conn.Close(websocket.StatusNormalClosure, ""); close(closed) }()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
		s.end("closed by caller")
		<-s.readDone
		close(s.abandon)
	})
	<-s.pumpDone
	return nil
}
