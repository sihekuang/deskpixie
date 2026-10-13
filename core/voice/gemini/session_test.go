package gemini

// Port of ai-mascot crates/voice-gemini/src/session_tests.rs (15/15; the conformance case is in conformance_test.go).
// Session tests against local mock servers that speak Gemini Live's message shapes.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

func cfg() voice.SessionConfig {
	return voice.SessionConfig{Model: "gemini-3.8-live", Voice: "Puck", Instructions: "Be brief.", TurnDetection: voice.TurnSemantic}
}

// seen records what a mock server received.
type seen struct {
	mu       sync.Mutex
	messages []map[string]any // every client text message, parsed
	uri      string           // request path and query
}

func (s *seen) add(t *testing.T, b []byte) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Errorf("client sent non-JSON: %s", b)
		return
	}
	s.mu.Lock()
	s.messages = append(s.messages, m)
	s.mu.Unlock()
}

func (s *seen) snapshot() ([]map[string]any, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.messages...), s.uri
}

// serve runs handler for each websocket connection; stop closes when the test ends.
func serve(t *testing.T, path string, handler func(c *websocket.Conn, ctx context.Context)) (string, *seen) {
	t.Helper()
	sn := &seen{}
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sn.mu.Lock()
		sn.uri = r.URL.RequestURI()
		sn.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		handler(c, ctx)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) }) // runs first: lets handlers return before srv.Close waits
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path, sn
}

// bin sends a binary frame, as Gemini sends its JSON.
func bin(ctx context.Context, c *websocket.Conn, v string) error {
	return c.Write(ctx, websocket.MessageBinary, []byte(v))
}

// mock answers setup with setupComplete, records everything, and closes after closeAfter
// client messages.
func mock(t *testing.T, closeAfter int) (string, *seen) {
	var sn *seen
	url, sn := serve(t, "/ws/BidiGenerateContent", func(c *websocket.Conn, ctx context.Context) {
		n := 0
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			sn.add(t, b)
			n++
			if n == 1 {
				_ = bin(ctx, c, setupComplete)
			}
			if n >= closeAfter {
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	})
	return url, sn
}

// silentMock reads the setup, sends reply (a message, or a close when code != 0), then goes
// silent without reading.
func silentMock(t *testing.T, reply string, code websocket.StatusCode) string {
	url, _ := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx)
		if code != 0 {
			_ = c.Close(code, reply)
		} else {
			_ = bin(ctx, c, reply)
		}
		<-ctx.Done()
	})
	return url
}

func connectTo(t *testing.T, m voice.Model, c voice.SessionConfig, key string) voice.Session {
	t.Helper()
	s, err := m.Connect(context.Background(), c, key)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// recv returns the next event, or ok=false when the stream ended. Fails after 5 s.
func recv(t *testing.T, s voice.Session) (voice.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-s.Events():
		return ev, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
		return voice.Event{}, false
	}
}

func expectReady(t *testing.T, s voice.Session) {
	t.Helper()
	if ev, ok := recv(t, s); !ok || ev.Kind != voice.EvReady {
		t.Fatalf("want Ready, got %+v ok=%v", ev, ok)
	}
}

func expectEnd(t *testing.T, s voice.Session) {
	t.Helper()
	if ev, ok := recv(t, s); ok {
		t.Fatalf("want end of stream, got %v", show([]voice.Event{ev}))
	}
}

func TestConnectsWithTheKeyInTheQueryAndSendsSetupThen16kAudio(t *testing.T) {
	url, sn := mock(t, 2)
	s := connectTo(t, New(url), cfg(), "AIza-test_key")
	expectReady(t, s)
	frame := make([]int16, 2400)
	for i := range frame {
		frame[i] = 258
	}
	if err := s.Send(voice.Command{Kind: voice.CmdAudio, Audio: frame}); err != nil {
		t.Fatal(err)
	}
	expectEnd(t, s) // server closed after 2 messages → stream ends

	msgs, uri := sn.snapshot()
	if uri != "/ws/BidiGenerateContent?key=AIza-test_key" {
		t.Fatal(uri)
	}
	setup := msgs[0]["setup"]
	if at(setup, "model") != "models/gemini-3.8-live" {
		t.Fatal(setup)
	}
	if at(setup, "generationConfig", "speechConfig", "voiceConfig", "prebuiltVoiceConfig", "voiceName") != "Puck" {
		t.Fatal(setup)
	}
	a := at(msgs[1], "realtimeInput", "audio")
	if at(a, "mimeType") != "audio/pcm;rate=16000" {
		t.Fatal(a)
	}
	if b, _ := base64.StdEncoding.DecodeString(at(a, "data").(string)); len(b) != 3200 {
		t.Fatal(len(b))
	}
}

func TestOpenaiSettingsAreReplacedBeforeAnythingIsSent(t *testing.T) {
	url, sn := mock(t, 1)
	c := cfg()
	c.Model, c.Voice = "gpt-realtime", "marin"
	s := connectTo(t, New(url), c, "k")
	expectReady(t, s)
	recv(t, s)
	msgs, _ := sn.snapshot()
	b, _ := json.Marshal(msgs[0])
	if strings.Contains(string(b), "gpt-realtime") || strings.Contains(string(b), "marin") {
		t.Fatal(string(b))
	}
	if at(msgs[0], "setup", "model") != "models/gemini-3.8-live" {
		t.Fatal(string(b))
	}
}

func TestKeysAreUrlEncoded(t *testing.T) {
	url, sn := mock(t, 1)
	s := connectTo(t, New(url), cfg(), "a+b/c=")
	recv(t, s)
	recv(t, s)
	if _, uri := sn.snapshot(); !strings.HasSuffix(uri, "?key=a%2Bb%2Fc%3D") {
		t.Fatal(uri)
	}
}

func TestCloseCommandClosesTheSocket(t *testing.T) {
	url, _ := mock(t, math.MaxInt)
	s := connectTo(t, New(url), cfg(), "k")
	select {
	case ev := <-s.Events():
		if ev.Kind != voice.EvReady {
			t.Fatal(show([]voice.Event{ev}))
		}
	case <-time.After(time.Second):
		t.Fatal("Ready is queued first")
	}
	if err := s.Send(voice.Command{Kind: voice.CmdClose}); err != nil {
		t.Fatal(err)
	}
	expectEnd(t, s) // stream ends promptly
}

func TestConnectFailureNeverLeaksTheKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().String()
	srv.Close() // nothing listens here now
	_, err := New("ws://"+addr+"/ws").Connect(context.Background(), cfg(), "AIza-super-secret")
	if err == nil {
		t.Fatal("connect should fail")
	}
	if strings.Contains(err.Error(), "AIza-super-secret") {
		t.Fatal(err)
	}
}

func TestScrubBlanksRawAndEncodedKeys(t *testing.T) {
	if got := scrub("url ?key=a%2Bb and a+b", "a+b"); got != "url ?key=*** and ***" {
		t.Fatal(got)
	}
	if got := scrub("nothing", ""); got != "nothing" {
		t.Fatal(got)
	}
}

func TestNameAndDefaultUrl(t *testing.T) {
	m := New("").(*model)
	if m.Name() != "gemini-live" {
		t.Fatal(m.Name())
	}
	if m.baseURL != "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent" {
		t.Fatal(m.baseURL)
	}
}

func TestRejectedSetupFailsConnectWithTheReason(t *testing.T) {
	url := silentMock(t, "Request contains an invalid argument.", websocket.StatusInvalidFramePayloadData)
	_, err := New(url).Connect(context.Background(), cfg(), "k")
	if err == nil || !strings.Contains(err.Error(), "1007 Request contains an invalid argument.") {
		t.Fatal(err)
	}
}

func TestIdleServerEndsTheStream(t *testing.T) {
	url := silentMock(t, setupComplete, 0)
	m := newModel(url, 50*time.Millisecond, 250*time.Millisecond)
	s := connectTo(t, m, cfg(), "k")
	expectReady(t, s)
	expectEnd(t, s) // the idle deadline ends the stream
}

func TestAPolicyCloseMidSessionIsAFatalError(t *testing.T) {
	url, _ := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx)
		_ = bin(ctx, c, setupComplete)
		_, _, _ = c.Read(ctx) // one audio frame
		_ = c.Close(websocket.StatusPolicyViolation, "quota exceeded")
	})
	s := connectTo(t, New(url), cfg(), "k")
	expectReady(t, s)
	if err := s.Send(voice.Command{Kind: voice.CmdAudio, Audio: make([]int16, 2400)}); err != nil {
		t.Fatal(err)
	}
	ev, ok := recv(t, s)
	if !ok {
		t.Fatal("stream ended without the fatal error")
	}
	want(t, []voice.Event{ev}, eFatal("closed (1008): quota exceeded"))
	expectEnd(t, s)
}

func TestInvalidModelNameIsRejected(t *testing.T) {
	for _, bad := range []string{"gemini-a b", "gemini-a&b=c", "gemini-x/../y"} {
		c := cfg()
		c.Model = bad
		_, err := New("ws://127.0.0.1:1/ws").Connect(context.Background(), c, "k")
		if err == nil || err.Error() != "gemini: connect: invalid model name" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// ---------- the voice loop against a scripted Gemini ----------

// frameMs: audio is exchanged in 100 ms frames (as voice-core's fakes::FRAME_MS).
const frameMs = 100

// tone is ms of a 440 Hz sine at amplitude amp (0..1), 24 kHz.
func tone(ms int, amp float64) []int16 {
	out := make([]int16, 24_000*ms/1000)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*440*float64(i)/24_000) * amp * 32767)
	}
	return out
}

// reply is what the fake Gemini says after each user turn.
type reply struct {
	chunks int
	tool   string // "" = no tool call
}

// fakeGemini is a fake Gemini Live server: server VAD on the 16 kHz input (loud then three
// quiet frames = end of turn), replies streamed as 24 kHz modelTurn chunks every 30 ms,
// barge-in → interrupted. It never sends voiceActivity, so the adapter's derived speech
// events are what is tested. With transcribeLive, every loud frame also gets an
// inputTranscription fragment at once (as Gemini streams the user's words while they talk).
func fakeGemini(t *testing.T, script []reply, transcribeLive bool) (string, *seen) {
	var sn *seen
	url, sn := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		var mu sync.Mutex
		var outgoing []string
		send := func(v string) bool { return bin(ctx, c, v) == nil }
		go func() {
			tick := time.NewTicker(30 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
				mu.Lock()
				var next string
				if len(outgoing) > 0 {
					next, outgoing = outgoing[0], outgoing[1:]
				}
				mu.Unlock()
				if next != "" && !send(next) {
					return
				}
			}
		}()
		speaking, quiet := false, 0
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			sn.add(t, b)
			var v map[string]any
			_ = json.Unmarshal(b, &v)
			if _, ok := v["setup"]; ok {
				send(setupComplete)
				continue
			}
			data, ok := at(v, "realtimeInput", "audio", "data").(string)
			if !ok {
				continue
			}
			if at(v, "realtimeInput", "audio", "mimeType") != "audio/pcm;rate=16000" {
				t.Errorf("mime %v", at(v, "realtimeInput", "audio", "mimeType"))
			}
			raw, _ := base64.StdEncoding.DecodeString(data)
			pcm, _ := audio.DecodeLE(raw)
			loud := audio.RMS(pcm) > 0.05
			mu.Lock()
			switch {
			case loud:
				if transcribeLive {
					send(`{"serverContent":{"inputTranscription":{"text":"la "}}}`)
				}
				if !speaking && len(outgoing) > 0 {
					outgoing = nil
					send(interrupted)
					send(turnComplete)
				}
				speaking, quiet = true, 0
			case speaking:
				quiet++
				if quiet >= 3 {
					speaking = false
					if len(script) == 0 {
						t.Error("script ran out")
						mu.Unlock()
						return
					}
					r := script[0]
					script = script[1:]
					outgoing = append(outgoing, `{"serverContent":{"inputTranscription":{"text":"hello"}}}`)
					for range r.chunks {
						outgoing = append(outgoing, audioMsg(tone(frameMs, 0.2)))
					}
					outgoing = append(outgoing, `{"serverContent":{"outputTranscription":{"text":"Four."}}}`)
					if r.tool != "" {
						outgoing = append(outgoing, fmt.Sprintf(`{"toolCall":{"functionCalls":[{"id":"fc_1","name":%q,"args":{}}]}}`, r.tool))
					} else {
						outgoing = append(outgoing, `{"serverContent":{"generationComplete":true}}`, turnComplete)
					}
				}
			}
			mu.Unlock()
		}
	})
	return url, sn
}

// TestPassesVoiceCoreConformanceAgainstAScriptedGemini (the 15th Rust case) is in
// conformance_test.go: it needs P2's core/voice/voicetest suite.

// waitFor polls (up to 5 s) for the first client message with a field, and returns that field.
func waitFor(t *testing.T, sn *seen, field string) any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		msgs, _ := sn.snapshot()
		for _, m := range msgs {
			if v, ok := m[field]; ok {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s message within 5 s", field)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole voice loop (voice runtime + this adapter) against the fake Gemini: the user
// talks for longer than the idle timeout before any reply, with no voiceActivity from the
// server. Their transcribed words must stop the silence timer, so the reply still plays.
func TestALongQuestionOutlastsTheIdleTimeoutWithoutVoiceActivity(t *testing.T) {
	url, _ := fakeGemini(t, []reply{{chunks: 3}}, true)
	var mu sync.Mutex
	var ui []voice.UIEvent
	r := voice.NewRuntime(New(url), audio.Passthrough{}, voice.RuntimeConfig{
		Session:      cfg(),
		APIKey:       "k",
		MouthLevels:  2,
		Conversation: voice.ConversationConfig{IdleTimeout: 800 * time.Millisecond, HoldTimeout: 10 * time.Second},
	}, func(e voice.UIEvent) { mu.Lock(); ui = append(ui, e); mu.Unlock() })
	t.Cleanup(r.Close)
	uiHas := func(f func(voice.UIEvent) bool) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range ui {
			if f(e) {
				return true
			}
		}
		return false
	}

	r.Hotkey()
	deadline := time.Now().Add(5 * time.Second)
	for !uiHas(func(e voice.UIEvent) bool { return e.Kind == "mic" && e.Text == "start" }) {
		if time.Now().After(deadline) {
			t.Fatal("mic never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	opened := len(ui)
	mu.Unlock()

	// 2 s of speech, then silence: well past the 800 ms timeout.
	frame := tone(frameMs, 0.3)
	for range 2000 / frameMs {
		r.PushMic(frame, time.Now())
		time.Sleep(frameMs * time.Millisecond)
	}
	silence := make([]int16, len(frame))
	buf := make([]int16, 4800)
	deadline = time.Now().Add(5 * time.Second)
	for played := false; !played; {
		if time.Now().After(deadline) {
			t.Fatal("the reply never played")
		}
		r.PushMic(silence, time.Now())
		time.Sleep(frameMs * time.Millisecond)
		n, _ := r.PullPlayback(buf)
		played = n > 0
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range ui[opened:] {
		if e.Kind == "mic" && e.Text == "stop" { // the conversation closed
			t.Fatalf("the conversation ended while the user was talking: %+v", ui)
		}
	}
}

func TestAToolCallInDroppedOutputIsAnsweredAsCancelled(t *testing.T) {
	var sn *seen
	url, sn := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx) // setup
		_ = bin(ctx, c, setupComplete)
		_ = bin(ctx, c, audioMsg([]int16{1, 2}))
		time.Sleep(300 * time.Millisecond) // the client cancels meanwhile
		_ = bin(ctx, c, `{"toolCall":{"functionCalls":[{"id":"late","name":"delegate","args":{}}]}}`)
		for {
			typ, b, err := c.Read(ctx)
			if err != nil || typ != websocket.MessageText {
				return
			}
			sn.add(t, b)
		}
	})
	s := connectTo(t, New(url), cfg(), "k")
	for {
		ev, ok := recv(t, s)
		if !ok {
			t.Fatal("closed")
		}
		if ev.Kind == voice.EvAudioDelta {
			break
		}
	}
	if err := s.Send(voice.Command{Kind: voice.CmdCancelResponse}); err != nil {
		t.Fatal(err)
	}
	response := waitFor(t, sn, "toolResponse")
	if at(response, "functionResponses", 0, "id") != "late" {
		t.Fatal(response)
	}
	if _, ok := at(response, "functionResponses", 0, "response", "error").(string); !ok {
		t.Fatal(response)
	}
	// No ToolCall reached the voice loop.
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == voice.EvToolCall {
				t.Fatal(show([]voice.Event{ev}))
			}
			continue
		default:
		}
		break
	}
}

func TestCloseReasonsNeverCarryTheKey(t *testing.T) {
	url, _ := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx)
		_ = bin(ctx, c, setupComplete)
		_ = c.Close(websocket.StatusPolicyViolation, "API key AIza-leaky not valid")
	})
	s := connectTo(t, New(url), cfg(), "AIza-leaky")
	expectReady(t, s)
	ev, ok := recv(t, s)
	if !ok {
		t.Fatal("stream ended without the fatal error")
	}
	want(t, []voice.Event{ev}, eFatal("closed (1008): API key *** not valid"))
}

// V-014: GEMINI_TRACE=1 logs every server message after setup with its timing, the audio data
// elided and the API key never printed (even when a server message echoes it).
func TestGeminiTraceLogsServerFramesWithTheKeyRedacted(t *testing.T) {
	t.Setenv("GEMINI_TRACE", "1")
	var buf syncBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	const key = "AIza-trace_secret/+="
	audioMsg := `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"` +
		base64.StdEncoding.EncodeToString(make([]byte, 480)) + `"}}]}}}`
	url, _ := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx)
		_ = bin(ctx, c, setupComplete)
		_ = bin(ctx, c, audioMsg)
		_ = bin(ctx, c, `{"serverContent":{"outputTranscription":{"text":"your key is `+key+` and `+queryEscape(key)+`"}}}`)
		_ = bin(ctx, c, turnComplete)
		<-ctx.Done()
	})
	s := connectTo(t, New(url), cfg(), key)
	expectReady(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), "voice-gemini trace +") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("trace lines missing:\n%s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if strings.Contains(out, key) || strings.Contains(out, queryEscape(key)) {
		t.Fatalf("key in the trace:\n%s", out)
	}
	if !strings.Contains(out, `"data":"<640 bytes>"`) || !strings.Contains(out, "turnComplete") || !strings.Contains(out, "***") {
		t.Fatalf("trace shape:\n%s", out)
	}
}

// Without GEMINI_TRACE nothing is traced.
func TestNoGeminiTraceByDefault(t *testing.T) {
	t.Setenv("GEMINI_TRACE", "")
	var buf syncBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	url, _ := serve(t, "/ws", func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx)
		_ = bin(ctx, c, setupComplete)
		_ = bin(ctx, c, turnComplete)
		<-ctx.Done()
	})
	s := connectTo(t, New(url), cfg(), "k")
	expectReady(t, s)
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(buf.String(), "trace") {
		t.Fatal(buf.String())
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
