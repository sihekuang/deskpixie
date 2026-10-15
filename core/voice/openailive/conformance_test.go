package openailive

// Runs the voice.Model conformance suite (voicetest.Run) against this adapter talking to a
// scripted fake of GPT-Live. The fake behaves like GPT-Live: a continuous output track (silence
// while idle), input transcript fragments for loud input, a reply after quiet input, barge-in
// when the user talks over a reply, tool calls through Responses delegation events, and never
// a speech or response event. Real time, 20 ms frames.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
	vt "github.com/sihekuang/deskpixie/core/voice/voicetest"
)

const confFrameMs = 20

type confReply struct {
	frames int
	tool   string
}

type fakeLive struct {
	mu      sync.Mutex
	script  []confReply
	start   map[string]any // the session.start received
	clients []string       // client event types received, in order (audio excluded)
}

type fakeConn struct {
	c   *websocket.Conn
	ctx context.Context
	srv *fakeLive

	mu        sync.Mutex // guards the reply and writes
	frames    int        // reply frames left
	sent      int        // reply frames sent
	tool      string
	callSeq   int
	toolsSent bool
}

func (f *fakeConn) write(v map[string]any) {
	b, _ := json.Marshal(v)
	_ = f.c.Write(f.ctx, websocket.MessageText, b)
}

func (f *fakeConn) startReply(r confReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames, f.sent, f.tool, f.toolsSent = r.frames, 0, r.tool, false
}

func (f *fakeConn) nextReply() confReply {
	f.srv.mu.Lock()
	defer f.srv.mu.Unlock()
	r := confReply{frames: 3}
	if len(f.srv.script) > 0 {
		r, f.srv.script = f.srv.script[0], f.srv.script[1:]
	}
	return r
}

// speak streams the output track: the reply's tone, otherwise silence, every frame.
func (f *fakeConn) speak() {
	tone := base64.StdEncoding.EncodeToString(audio.EncodeLE(vt.Tone(confFrameMs, 0.3)))
	quiet := base64.StdEncoding.EncodeToString(audio.EncodeLE(vt.Silence(confFrameMs)))
	t := time.NewTicker(confFrameMs * time.Millisecond)
	defer t.Stop()
	var ms int64
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-t.C:
		}
		f.mu.Lock()
		delta := quiet
		if f.frames > 0 {
			delta = tone
			f.frames--
			f.sent++
		}
		f.write(map[string]any{"type": "session.output_audio.delta", "delta": delta, "start_ms": ms, "end_ms": ms + confFrameMs})
		ms += confFrameMs
		if f.sent == 1 && f.tool != "" && !f.toolsSent {
			f.toolsSent = true
			f.callSeq++
			id := "call_" + string(rune('0'+f.callSeq))
			f.write(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dg_1", "type": "responses", "target": "responses"}, "response_id": "resp_1"})
			f.write(map[string]any{"type": "response.event", "delegation_id": "dg_1", "event": map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}}})
			f.write(map[string]any{"type": "response.event", "delegation_id": "dg_1", "event": map[string]any{"type": "response.output_item.done",
				"item": map[string]any{"type": "function_call", "name": f.tool, "call_id": id, "arguments": "{}"}}})
			f.write(map[string]any{"type": "response.event", "delegation_id": "dg_1", "event": map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}}})
		}
		if f.sent > 0 && f.frames == 0 {
			f.write(map[string]any{"type": "session.output_transcript.delta", "delta": "ok", "start_ms": ms, "end_ms": ms})
			f.sent = 0
		}
		f.mu.Unlock()
	}
}

func (s *fakeLive) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	f := &fakeConn{c: c, ctx: ctx, srv: s}
	talking, quiet := false, 0
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type    string         `json:"type"`
			Audio   string         `json:"audio"`
			Session map[string]any `json:"session"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Type != "session.input_audio.append" {
			s.mu.Lock()
			s.clients = append(s.clients, m.Type)
			s.mu.Unlock()
		}
		switch m.Type {
		case "session.start":
			s.mu.Lock()
			s.start = m.Session
			s.mu.Unlock()
			f.mu.Lock()
			f.write(map[string]any{"type": "session.started", "session": map[string]any{"id": "live_1", "model": "gpt-live-1"}})
			f.mu.Unlock()
			go f.speak()
		case "session.input_audio.append":
			raw, _ := base64.StdEncoding.DecodeString(m.Audio)
			pcm, _ := audio.DecodeLE(raw)
			if voice.RMS(pcm) > 0.05 {
				quiet = 0
				talking = true
				f.mu.Lock()
				f.frames = 0 // barge-in: GPT-Live yields
				f.write(map[string]any{"type": "session.input_transcript.delta", "delta": "word "})
				f.mu.Unlock()
			} else if talking {
				if quiet++; quiet >= 5 {
					talking, quiet = false, 0
					f.startReply(f.nextReply())
				}
			}
		case "response.create":
			f.startReply(confReply{frames: 3})
		case "session.close":
			f.mu.Lock()
			f.write(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": 1}})
			f.mu.Unlock()
			return
		}
	}
}

func confClips() vt.Clips {
	var c vt.Clips
	for range 5 {
		c.Question = append(c.Question, vt.Tone(confFrameMs, 0.3))
		c.Goodbye = append(c.Goodbye, vt.Tone(confFrameMs, 0.3))
	}
	for range 8 {
		c.Silence = append(c.Silence, vt.Silence(confFrameMs))
	}
	return c
}

func confCfg() voice.SessionConfig {
	return voice.SessionConfig{
		Model: DefaultModel, Voice: "marin", Instructions: "be Pixie",
		Tools: []voice.ToolSpec{{Name: voice.EndConversationTool, Description: "end"}},
	}
}

func TestGPTLiveAdapterPassesTheConformanceSuite(t *testing.T) {
	fs := &fakeLive{script: []confReply{{frames: 3}, {frames: 200}, {frames: 1, tool: voice.EndConversationTool}}}
	srv := httptest.NewServer(http.HandlerFunc(fs.serve))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/live/sessions"
	rep, err := vt.Run(New(url), "test-key", confCfg(), confClips())
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}
	if len(rep.Steps) != 5 {
		t.Fatalf("steps %v", rep.Steps)
	}
	if len(rep.Transcripts) == 0 {
		t.Fatal("no assistant transcripts")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.start["model"] != DefaultModel {
		t.Fatalf("session.start model: %v", fs.start["model"])
	}
	// end_conversation's result is sent but doesn't ask the backend to continue.
	var items, creates int
	for _, ty := range fs.clients {
		switch ty {
		case "response.item.create":
			items++
		case "response.create":
			creates++
		}
	}
	if items != 1 || creates != 0 {
		t.Fatalf("tool result events: %d item.create, %d response.create (%v)", items, creates, fs.clients)
	}
}

// A startup error that says the key has no access becomes FailNoAccess.
func TestConnectNoAccessIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, _, _ = c.Read(r.Context())
		b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "model_not_found", "message": "no"}})
		_ = c.Write(r.Context(), websocket.MessageText, b)
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	_, err := New("ws"+strings.TrimPrefix(srv.URL, "http")).Connect(context.Background(), confCfg(), "k")
	var fe voice.FailureError
	if !asFailure(err, &fe) || fe.Failure().Status() != "Voice: this API key has no GPT-Live access" {
		t.Fatalf("err %v", err)
	}
}

func TestConnectHTTP401IsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"bad key"}}`))
	}))
	defer srv.Close()
	_, err := New("ws"+strings.TrimPrefix(srv.URL, "http")).Connect(context.Background(), confCfg(), "k")
	var fe voice.FailureError
	if !asFailure(err, &fe) || fe.Failure().Status() != "Voice: OpenAI rejected the API key" {
		t.Fatalf("err %v", err)
	}
}
