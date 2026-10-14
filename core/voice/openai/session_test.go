package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/voice"
)

func cfg() voice.SessionConfig {
	return voice.SessionConfig{Model: "gpt-realtime", Voice: "marin", Instructions: "Be brief.",
		Tools: []voice.ToolSpec{{Name: "end_conversation", Description: "End it.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}}
}

type srvConn struct {
	c   *websocket.Conn
	ctx context.Context
	t   *testing.T
}

func (s *srvConn) read() map[string]any {
	s.t.Helper()
	rctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	_, b, err := s.c.Read(rctx)
	if err != nil {
		s.t.Fatalf("server read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		s.t.Fatal(err)
	}
	return m
}
func (s *srvConn) write(v string) {
	s.t.Helper()
	if err := s.c.Write(s.ctx, websocket.MessageText, []byte(v)); err != nil {
		s.t.Fatalf("server write: %v", err)
	}
}

// start runs script on each accepted connection (after reading session.update and replying session.updated).
func start(t *testing.T, handshake bool, script func(*srvConn), hdr *http.Header, path *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hdr != nil {
			*hdr = r.Header.Clone()
		}
		if path != nil {
			*path = r.URL.RequestURI()
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		s := &srvConn{c: c, ctx: r.Context(), t: t}
		if handshake {
			s.read()
			s.write(`{"type":"session.updated","session":{}}`)
		}
		script(s)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/v1/realtime"
}

func connect(t *testing.T, srv *httptest.Server) voice.Session {
	t.Helper()
	s, err := New(wsURL(srv)).Connect(context.Background(), cfg(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func next(t *testing.T, s voice.Session) voice.Event {
	t.Helper()
	select {
	case ev, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
	}
	return voice.Event{}
}

func TestConnectSendsSessionUpdateAndWaitsForUpdated(t *testing.T) {
	var hdr http.Header
	var path string
	var first map[string]any
	got := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr, path = r.Header.Clone(), r.URL.RequestURI()
		c, _ := websocket.Accept(w, r, nil)
		defer c.CloseNow()
		s := &srvConn{c: c, ctx: r.Context(), t: t}
		first = s.read()
		close(got)
		s.write(`{"type":"session.updated","session":{}}`)
		<-r.Context().Done()
	}))
	defer srv.Close()
	s := connect(t, srv)
	<-got
	if ev := next(t, s); ev.Kind != voice.EvReady {
		t.Fatalf("first event %v", ev)
	}
	if hdr.Get("Authorization") != "Bearer test-key" || hdr.Get("OpenAI-Beta") != "" {
		t.Fatalf("headers %v", hdr)
	}
	if path != "/v1/realtime?model=gpt-realtime" {
		t.Fatal(path)
	}
	b, _ := json.Marshal(first)
	for _, want := range []string{`"type":"session.update"`, `"rate":24000`, `"voice":"marin"`, `"semantic_vad"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

func TestConnectRejectsBadModel(t *testing.T) {
	for _, bad := range []string{"", "gpt realtime", "a&b=c", "x/../y", "m?x"} {
		c := cfg()
		c.Model = bad
		if _, err := New("ws://127.0.0.1:1/v1/realtime").Connect(context.Background(), c, "k"); err == nil || !strings.Contains(err.Error(), "invalid model") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestConnectFailureDoesNotLeakKey(t *testing.T) {
	_, err := New("ws://127.0.0.1:1/v1/realtime").Connect(context.Background(), cfg(), "sk-super-secret")
	if err == nil || strings.Contains(err.Error(), "sk-super-secret") {
		t.Fatal(err)
	}
}

func TestRejectedSessionUpdateFailsConnect(t *testing.T) {
	srv := start(t, false, func(s *srvConn) {
		s.read()
		s.write(`{"type":"error","error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'session.voice'."}}`)
		<-s.ctx.Done()
	}, nil, nil)
	_, err := New(wsURL(srv)).Connect(context.Background(), cfg(), "k")
	if err == nil || !strings.Contains(err.Error(), "unknown_parameter") {
		t.Fatal(err)
	}
}

func TestAudioAppendIsBase64LE(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := start(t, true, func(s *srvConn) { got <- s.read(); <-s.ctx.Done() }, nil, nil)
	s := connect(t, srv)
	if err := s.Send(voice.Command{Kind: voice.CmdAudio, Audio: []int16{1, -1}}); err != nil {
		t.Fatal(err)
	}
	m := <-got
	if m["type"] != "input_audio_buffer.append" || m["audio"] != base64.StdEncoding.EncodeToString([]byte{1, 0, 0xff, 0xff}) {
		t.Fatalf("%v", m)
	}
}

func TestAudioDeltaCarriesOddByte(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`)
		s.write(`{"type":"response.output_audio.delta","item_id":"it1","delta":"` + base64.StdEncoding.EncodeToString([]byte{1, 0, 2}) + `"}`)
		s.write(`{"type":"response.output_audio.delta","item_id":"it1","delta":"` + base64.StdEncoding.EncodeToString([]byte{0}) + `"}`)
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	next(t, s) // ready
	next(t, s) // response.created
	a, b := next(t, s), next(t, s)
	if len(a.Audio) != 1 || len(b.Audio) != 1 || a.Audio[0] != 1 || b.Audio[0] != 2 {
		t.Fatalf("%v %v", a.Audio, b.Audio)
	}
}

func TestCancelSendsCancelThenTruncate(t *testing.T) {
	got := make(chan map[string]any, 2)
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`)
		s.write(`{"type":"response.output_audio.delta","item_id":"it1","delta":"AAA="}`)
		got <- s.read()
		got <- s.read()
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	for i := 0; i < 3; i++ {
		next(t, s)
	}
	if err := s.Send(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 1234}); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m["type"] != "response.cancel" {
		t.Fatalf("%v", m)
	}
	m := <-got
	if m["type"] != "conversation.item.truncate" || m["item_id"] != "it1" || m["audio_end_ms"] != float64(1234) || m["content_index"] != float64(0) {
		t.Fatalf("%v", m)
	}
}

func TestToolResultAndInject(t *testing.T) {
	got := make(chan map[string]any, 3)
	srv := start(t, true, func(s *srvConn) {
		for i := 0; i < 3; i++ {
			got <- s.read()
		}
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	s.Send(voice.Command{Kind: voice.CmdToolResult, CallID: "c1", Output: json.RawMessage(`{"ok":true}`), Respond: true})
	s.Send(voice.Command{Kind: voice.CmdInject, Text: "hi", Respond: false})
	m := <-got
	item := m["item"].(map[string]any)
	if item["type"] != "function_call_output" || item["call_id"] != "c1" || item["output"] != `{"ok":true}` {
		t.Fatalf("%v", m)
	}
	if m := <-got; m["type"] != "response.create" {
		t.Fatalf("%v", m)
	}
	m = <-got
	item = m["item"].(map[string]any)
	c := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || c["type"] != "input_text" || c["text"] != "hi" {
		t.Fatalf("%v", m)
	}
}

func TestResponseDoneEmitsToolCalls(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"response.done","response":{"id":"r","status":"completed","output":[
		 {"type":"message","role":"assistant"},
		 {"type":"function_call","status":"completed","name":"end_conversation","call_id":"call_7","arguments":"{}"},
		 {"type":"function_call","status":"completed","name":"odd","call_id":"call_8","arguments":"not json"}]}}`)
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	next(t, s)
	a, b, c := next(t, s), next(t, s), next(t, s)
	if a.Kind != voice.EvToolCall || a.CallID != "call_7" || a.ToolName != "end_conversation" || string(a.Args) != "{}" {
		t.Fatalf("%+v", a)
	}
	if b.Kind != voice.EvToolCall || string(b.Args) != `"not json"` {
		t.Fatalf("%+v", b)
	}
	if c.Kind != voice.EvResponseDone {
		t.Fatalf("%+v", c)
	}
}

func TestCancelNotActiveErrorIgnored(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"error","error":{"type":"invalid_request_error","code":"response_cancel_not_active","message":"x"}}`)
		s.write(`{"type":"input_audio_buffer.speech_started"}`)
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	next(t, s)
	if ev := next(t, s); ev.Kind != voice.EvSpeechStarted {
		t.Fatalf("%+v", ev)
	}
}

func TestOtherErrorIsNonFatal(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"error","error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'session.voice'."}}`)
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	next(t, s)
	ev := next(t, s)
	if ev.Kind != voice.EvError || ev.Fatal || ev.Message != "unknown_parameter: Unknown parameter: 'session.voice'." {
		t.Fatalf("%+v", ev)
	}
}

func TestFailedResponseIsFatalAfterDone(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		s.write(`{"type":"response.done","response":{"status":"failed","status_details":{"error":{"type":"server_error","code":"insufficient_quota"}},"output":[]}}`)
		<-s.ctx.Done()
	}, nil, nil)
	s := connect(t, srv)
	next(t, s)
	if ev := next(t, s); ev.Kind != voice.EvResponseDone {
		t.Fatalf("%+v", ev)
	}
	if ev := next(t, s); ev.Kind != voice.EvError || !ev.Fatal || ev.Message != "insufficient_quota" {
		t.Fatalf("%+v", ev)
	}
}

func TestSessionServerCloseEmitsFatal(t *testing.T) {
	srv := start(t, true, func(s *srvConn) { s.c.Close(websocket.StatusNormalClosure, "bye") }, nil, nil)
	s := connect(t, srv)
	next(t, s)
	if ev := next(t, s); ev.Kind != voice.EvError || !ev.Fatal {
		t.Fatalf("%+v", ev)
	}
	select {
	case _, ok := <-s.Events():
		if ok {
			t.Fatal("expected closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events not closed")
	}
}

func TestCloseCommandEndsStreamWithoutError(t *testing.T) {
	srv := start(t, true, func(s *srvConn) {
		rctx, c := context.WithTimeout(s.ctx, 5*time.Second)
		defer c()
		s.c.Read(rctx) // consumes the close frame
	}, nil, nil)
	s := connect(t, srv)
	next(t, s)
	if err := s.Send(voice.Command{Kind: voice.CmdClose}); err != nil {
		t.Fatal(err)
	}
	if ev, ok := <-s.Events(); ok {
		t.Fatalf("unexpected %+v", ev)
	}
}

func TestIdleServerEndsWithFatal(t *testing.T) {
	srv := start(t, true, func(s *srvConn) { <-s.ctx.Done() }, nil, nil) // never reads: pings go unanswered
	m := New(wsURL(srv)).(*model)
	m.pingEvery, m.idleDeadline = 50*time.Millisecond, 250*time.Millisecond
	s, err := m.Connect(context.Background(), cfg(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	next(t, s)
	if ev := next(t, s); ev.Kind != voice.EvError || !ev.Fatal {
		t.Fatalf("%+v", ev)
	}
}

func TestNameAndDefaultURL(t *testing.T) {
	m := New("").(*model)
	if m.Name() != "openai-realtime" || m.baseURL != "wss://api.openai.com/v1/realtime" {
		t.Fatal(m.Name(), m.baseURL)
	}
}

// ---- protocol (ported from protocol.rs tests) ----

func jsonOf(t *testing.T, v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestSessionUpdateGAShape(t *testing.T) {
	s := jsonOf(t, sessionUpdate(cfg()))["session"].(map[string]any)
	in := s["audio"].(map[string]any)["input"].(map[string]any)
	if s["type"] != "realtime" || s["tool_choice"] != "auto" || s["voice"] != nil || s["modalities"] != nil {
		t.Fatalf("%v", s)
	}
	if in["transcription"].(map[string]any)["model"] != "gpt-4o-mini-transcribe" || in["noise_reduction"] != nil {
		t.Fatalf("%v", in)
	}
	td := in["turn_detection"].(map[string]any)
	if td["type"] != "semantic_vad" || td["eagerness"] != "auto" {
		t.Fatalf("%v", td)
	}
	tool := s["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "end_conversation" {
		t.Fatalf("%v", tool)
	}
}

func TestTurnDetectionModes(t *testing.T) {
	c := cfg()
	c.NoisyRoom = true
	in := func() map[string]any {
		return jsonOf(t, sessionUpdate(c))["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)
	}
	td := in()["turn_detection"].(map[string]any)
	if td["type"] != "server_vad" || td["threshold"] != 0.85 || td["prefix_padding_ms"] != 300.0 || td["silence_duration_ms"] != 500.0 {
		t.Fatalf("%v", td)
	}
	if in()["noise_reduction"] != nil {
		t.Fatal("noisy room alone must not enable noise reduction")
	}
	c.ProviderNoiseReduction = true
	if in()["noise_reduction"].(map[string]any)["type"] != "far_field" {
		t.Fatal("noise_reduction")
	}
	c.TurnDetection = voice.TurnManual
	if v, ok := in()["turn_detection"]; !ok || v != nil {
		t.Fatalf("manual must be null: %v", v)
	}
	c.NoisyRoom, c.TurnDetection = false, voice.TurnServerVAD
	if in()["turn_detection"].(map[string]any)["type"] != "server_vad" {
		t.Fatal("server_vad")
	}
}

func TestPCMBase64LittleEndian(t *testing.T) {
	if pcmToBase64([]int16{258}) != "AgE=" {
		t.Fatal(pcmToBase64([]int16{258}))
	}
}

func TestDecoderReadyOnce(t *testing.T) {
	d := &decoder{}
	if len(d.decode([]byte(`{"type":"session.created"}`))) != 0 {
		t.Fatal("created")
	}
	if ev := d.decode([]byte(`{"type":"session.updated"}`)); len(ev) != 1 || ev[0].Kind != voice.EvReady {
		t.Fatal(ev)
	}
	if len(d.decode([]byte(`{"type":"session.updated"}`))) != 0 {
		t.Fatal("second ready")
	}
}

func TestDecoderCancelledResponseStillDone(t *testing.T) {
	d := &decoder{responseActive: true}
	ev := d.decode([]byte(`{"type":"response.done","response":{"status":"cancelled","output":[]}}`))
	if len(ev) != 1 || ev[0].Kind != voice.EvResponseDone || d.responseActive {
		t.Fatal(ev)
	}
	ev = d.decode([]byte(`{"type":"response.done","response":{"status":"failed","output":[]}}`))
	if len(ev) != 2 || ev[1].Message != "response failed" {
		t.Fatal(ev)
	}
}

func TestDecoderTranscripts(t *testing.T) {
	d := &decoder{}
	a := d.decode([]byte(`{"type":"response.output_audio_transcript.done","transcript":"Four!"}`))
	u := d.decode([]byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"two plus two"}`))
	if a[0].Role != "assistant" || a[0].Text != "Four!" || u[0].Role != "user" || u[0].Text != "two plus two" {
		t.Fatal(a, u)
	}
}

func TestEncodeCancelOnlyWhenActive(t *testing.T) {
	d := &decoder{currentItem: "i"}
	if m := d.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 5}); len(m) != 1 || m[0]["type"] != "conversation.item.truncate" {
		t.Fatal(m)
	}
	d = &decoder{}
	if m := d.encode(voice.Command{Kind: voice.CmdCancelResponse}); len(m) != 0 {
		t.Fatal(m)
	}
}
