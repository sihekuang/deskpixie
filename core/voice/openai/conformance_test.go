package openai

// Runs the voice.Model conformance suite (voicetest.Run, port of voice-core conformance.rs)
// against this adapter talking to a scripted fake of the Realtime server. Real time: the
// adapter does network I/O, which testing/synctest cannot fake, so clips use 20 ms frames.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// confServer behaves like Realtime with server VAD + interrupt_response: loud appends are
// speech, 5 quiet appends end a turn, speech during a reply cancels it.
type confServer struct {
	mu     sync.Mutex
	script []confReply
	calls  int
}

type confConn struct {
	c       *websocket.Conn
	ctx     context.Context
	srv     *confServer
	wmu     sync.Mutex // one reply emits at a time; nothing follows a cancel
	active  *confActive
	itemSeq int
}

type confActive struct {
	cancelled, finished bool
	stop                context.CancelFunc
}

func (c *confConn) write(v map[string]any) {
	b, _ := json.Marshal(v)
	_ = c.c.Write(c.ctx, websocket.MessageText, b)
}

func (c *confConn) cancel() {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	a := c.active
	c.active = nil
	if a == nil || a.finished || a.cancelled {
		return
	}
	a.cancelled = true
	a.stop()
	c.write(map[string]any{"type": "response.done", "response": map[string]any{"status": "cancelled", "output": []any{}}})
}

func (c *confConn) startReply() {
	c.cancel()
	c.srv.mu.Lock()
	c.srv.calls++
	r := confReply{}
	if len(c.srv.script) > 0 {
		r, c.srv.script = c.srv.script[0], c.srv.script[1:]
	}
	callID := fmt.Sprintf("call_%d", c.srv.calls)
	c.srv.mu.Unlock()
	c.itemSeq++
	item := fmt.Sprintf("item_%d", c.itemSeq)

	ctx, stop := context.WithCancel(c.ctx)
	a := &confActive{stop: stop}
	c.wmu.Lock()
	c.active = a
	c.write(map[string]any{"type": "response.created", "response": map[string]any{}})
	c.wmu.Unlock()
	emit := func(v map[string]any, final bool) bool {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		if a.cancelled {
			return false
		}
		c.write(v)
		a.finished = final
		return true
	}
	go func() {
		defer stop()
		tone := base64.StdEncoding.EncodeToString(audio.EncodeLE(vt.Tone(confFrameMs, 0.3)))
		for range r.frames {
			if !emit(map[string]any{"type": "response.output_audio.delta", "item_id": item, "delta": tone}, false) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(confFrameMs * time.Millisecond):
			}
		}
		output := []any{}
		if r.tool != "" {
			output = append(output, map[string]any{"type": "function_call", "status": "completed",
				"name": r.tool, "call_id": callID, "arguments": "{}"})
		}
		emit(map[string]any{"type": "response.output_audio_transcript.done", "transcript": "ok"}, false)
		emit(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed", "output": output}}, true)
	}()
}

func (s *confServer) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cc := &confConn{c: c, ctx: ctx, srv: s}
	talking, quiet := false, 0
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		_ = json.Unmarshal(data, &m)
		switch m.Type {
		case "session.update":
			cc.write(map[string]any{"type": "session.updated", "session": map[string]any{}})
		case "input_audio_buffer.append":
			raw, _ := base64.StdEncoding.DecodeString(m.Audio)
			pcm, _ := audio.DecodeLE(raw)
			if voice.RMS(pcm) > 0.05 {
				quiet = 0
				if !talking {
					talking = true
					cc.write(map[string]any{"type": "input_audio_buffer.speech_started"})
					cc.cancel() // interrupt_response
				}
			} else if talking {
				if quiet++; quiet >= 5 {
					talking, quiet = false, 0
					cc.write(map[string]any{"type": "input_audio_buffer.speech_stopped"})
					cc.startReply()
				}
			}
		case "response.cancel":
			cc.cancel()
		case "response.create":
			cc.startReply()
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

func TestOpenAIAdapterPassesTheConformanceSuite(t *testing.T) {
	fs := &confServer{script: []confReply{{frames: 3}, {frames: 200}, {frames: 1, tool: voice.EndConversationTool}}}
	srv := httptest.NewServer(http.HandlerFunc(fs.serve))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/realtime"
	rep, err := vt.Run(New(url), "test-key", cfg(), confClips())
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}
	if len(rep.Steps) != 5 {
		t.Fatalf("steps %v", rep.Steps)
	}
	if len(rep.Transcripts) == 0 {
		t.Fatal("no assistant transcripts")
	}
}
