//go:build live

package openailive_test

// Access probe (spec §10): does this OPENAI_API_KEY have GPT-Live? Run with:
//
//	go test -tags live -run TestLiveGPTLiveAccess -v ./voice/openailive/
//
// It passes in every case; the log line is the answer. No audio is sent, the session is closed
// at once (< $0.01), and the key is never printed.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveGPTLiveAccess(t *testing.T) {
	cfg, _ := os.UserConfigDir()
	key, ok := voice.NewEnvSecrets(voice.DotenvPaths(filepath.Join(cfg, "com.sihekuang.deskpixie"))).APIKey("openai-realtime")
	if !ok {
		t.Skip("no OPENAI_API_KEY")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "wss://api.openai.com/v1/live/sessions", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + key.Expose()}},
	})
	if err != nil {
		if resp != nil {
			var body struct {
				Error struct{ Code, Type, Message string } `json:"error"`
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = json.Unmarshal(raw, &body)
			t.Logf("NO ACCESS: http %d code=%q type=%q message=%q", resp.StatusCode, body.Error.Code, body.Error.Type, body.Error.Message)
			return
		}
		t.Logf("OTHER: dial: %v", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)
	start := `{"type":"session.start","event_id":"c_1","session":{"model":"gpt-live-1","delegation":{"type":"client"}}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(start)); err != nil {
		t.Logf("OTHER: write: %v", err)
		return
	}
	read := func() (map[string]any, error) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var ev map[string]any
		return ev, json.Unmarshal(data, &ev)
	}
	for {
		ev, err := read()
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				t.Logf("NO ACCESS: closed %d %q", ce.Code, ce.Reason)
			} else {
				t.Logf("OTHER: read: %v", err)
			}
			return
		}
		switch ev["type"] {
		case "session.started":
			s, _ := ev["session"].(map[string]any)
			id, _ := s["id"].(string)
			if len(id) > 8 {
				id = id[:8]
			}
			t.Logf("ACCESS OK: session %s… expires_at=%v model=%v", id, s["expires_at"], s["model"])
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.close","event_id":"c_2"}`))
			for {
				ev, err := read()
				if err != nil {
					t.Logf("closed without session.closed: %v", err)
					return
				}
				if ev["type"] == "session.closed" {
					t.Logf("session.closed reason=%v usage=%v", ev["reason"], ev["usage"])
					return
				}
			}
		case "error":
			b, _ := json.Marshal(ev["error"])
			t.Logf("NO ACCESS: error %s", b)
			return
		default:
			t.Logf("event %v", ev["type"])
		}
	}
}
