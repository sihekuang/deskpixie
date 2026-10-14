package openai

// Every test in ai-mascot crates/voice-openai/src/protocol.rs (20) and the two session.rs tests
// that had no same-named Go test, ported by name with the Rust assertions. The older Go tests in
// session_test.go stay: they cover the same cases end to end over a websocket.

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

// jeq asserts got marshals to the same JSON value as want.
func jeq(t *testing.T, got any, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func evEq(t *testing.T, got []voice.Event, want ...voice.Event) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func input(c voice.SessionConfig) map[string]any {
	b, _ := json.Marshal(sessionUpdate(c))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)
}

func TestANoisyRoomRaisesTheSpeechThresholdWithoutNoiseReduction(t *testing.T) {
	c := cfg()
	c.NoisyRoom = true
	in := input(c)
	jeq(t, in["turn_detection"], `{"type":"server_vad","threshold":0.85,"prefix_padding_ms":300,"silence_duration_ms":500,"create_response":true,"interrupt_response":true}`)
	if _, ok := in["noise_reduction"]; ok {
		t.Fatal("a noisy room alone does not ask OpenAI to clean the mic")
	}
	c.ProviderNoiseReduction = true
	jeq(t, input(c)["noise_reduction"], `{"type":"far_field"}`)
	c.ProviderNoiseReduction, c.TurnDetection = false, voice.TurnManual
	if input(c)["turn_detection"] != nil {
		t.Fatal("manual is null")
	}
}

func TestSessionUpdateUsesTheGAShape(t *testing.T) {
	c := cfg()
	c.TurnDetection = voice.TurnSemantic
	jeq(t, sessionUpdate(c), `{"type":"session.update","session":{
		"type":"realtime","instructions":"Be brief.","output_modalities":["audio"],
		"audio":{
			"input":{"format":{"type":"audio/pcm","rate":24000},
				"turn_detection":{"type":"semantic_vad","eagerness":"auto","create_response":true,"interrupt_response":true},
				"transcription":{"model":"gpt-4o-mini-transcribe"}},
			"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},
		"tools":[{"type":"function","name":"end_conversation","description":"End it.","parameters":{"type":"object","properties":{}}}],
		"tool_choice":"auto"}}`)
}

func TestOtherTurnDetectionModes(t *testing.T) {
	c := cfg()
	c.TurnDetection = voice.TurnServerVAD
	if input(c)["turn_detection"].(map[string]any)["type"] != "server_vad" {
		t.Fatal("server_vad")
	}
	c.TurnDetection = voice.TurnManual
	if input(c)["turn_detection"] != nil {
		t.Fatal("manual is null")
	}
}

func TestPcmRoundTripsThroughBase64LittleEndian(t *testing.T) {
	pcm := []int16{0, 1, -1, 32767, -32768, 258}
	d := &decoder{}
	got, ok := d.deltaToPCM(pcmToBase64(pcm))
	if !ok || !reflect.DeepEqual(got, pcm) {
		t.Fatal(got)
	}
	if pcmToBase64([]int16{258}) != "AgE=" {
		t.Fatal("258 = 0x0102 → bytes 02 01")
	}
	if _, ok := d.deltaToPCM("not base64!"); ok {
		t.Fatal("garbage decoded")
	}
}

func TestSessionUpdatedIsReadyOnce(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"session.created","session":{}}`)))
	evEq(t, d.decode([]byte(`{"type":"session.updated","session":{}}`)), voice.Event{Kind: voice.EvReady})
	evEq(t, d.decode([]byte(`{"type":"session.updated","session":{}}`)))
}

func TestSpeechEvents(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"input_audio_buffer.speech_started","audio_start_ms":120,"item_id":"item_1"}`)), voice.Event{Kind: voice.EvSpeechStarted})
	evEq(t, d.decode([]byte(`{"type":"input_audio_buffer.speech_stopped","audio_end_ms":900,"item_id":"item_1"}`)), voice.Event{Kind: voice.EvSpeechStopped})
}

func TestAudioDeltaDecodesPcmAndTracksTheItem(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`)), voice.Event{Kind: voice.EvResponseCreated})
	if !d.responseActive {
		t.Fatal("not active")
	}
	msg, _ := json.Marshal(map[string]any{"type": "response.output_audio.delta", "response_id": "resp_1", "item_id": "item_9",
		"output_index": 0, "content_index": 0, "delta": pcmToBase64([]int16{5, -5})})
	evEq(t, d.decode(msg), voice.Event{Kind: voice.EvAudioDelta, Audio: []int16{5, -5}})
	if d.currentItem != "item_9" {
		t.Fatal(d.currentItem)
	}
}

func TestTranscriptsForBothSides(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"response.output_audio_transcript.done","item_id":"i","transcript":"Four!"}`)),
		voice.Event{Kind: voice.EvTranscript, Role: "assistant", Text: "Four!"})
	evEq(t, d.decode([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"i","content_index":0,"transcript":"two plus two"}`)),
		voice.Event{Kind: voice.EvTranscript, Role: "user", Text: "two plus two"})
}

func TestResponseDoneEmitsToolCallsFirst(t *testing.T) {
	d := &decoder{}
	d.decode([]byte(`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`))
	done := `{"type":"response.done","response":{"id":"r","status":"completed","output":[
		{"type":"message","role":"assistant","content":[{"type":"output_audio","transcript":"Bye!"}]},
		{"id":"item_2","type":"function_call","status":"completed","name":"end_conversation","call_id":"call_7","arguments":"{}"},
		{"id":"item_3","type":"function_call","status":"completed","name":"odd","call_id":"call_8","arguments":"not json"}]}}`
	evEq(t, d.decode([]byte(done)),
		voice.Event{Kind: voice.EvToolCall, CallID: "call_7", ToolName: "end_conversation", Args: json.RawMessage(`{}`)},
		voice.Event{Kind: voice.EvToolCall, CallID: "call_8", ToolName: "odd", Args: json.RawMessage(`"not json"`)},
		voice.Event{Kind: voice.EvResponseDone})
	if d.responseActive {
		t.Fatal("still active")
	}
}

func TestFailedResponseIsAFatalErrorAfterDone(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"failed","status_details":{"type":"failed","error":{"type":"server_error","code":"insufficient_quota"}},"output":[]}}`)),
		voice.Event{Kind: voice.EvResponseDone}, voice.Event{Kind: voice.EvError, Fatal: true, Message: "insufficient_quota"})
	evEq(t, d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"failed","output":[]}}`)),
		voice.Event{Kind: voice.EvResponseDone}, voice.Event{Kind: voice.EvError, Fatal: true, Message: "response failed"})
}

func TestCancelledResponseIsStillDone(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"cancelled","status_details":{"type":"cancelled","reason":"turn_detected"},"output":[]}}`)),
		voice.Event{Kind: voice.EvResponseDone})
}

func TestErrorsAreReportedExceptBenignCancel(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"response_cancel_not_active","message":"x"}}`)))
	evEq(t, d.decode([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'session.voice'."}}`)),
		voice.Event{Kind: voice.EvError, Message: "unknown_parameter: Unknown parameter: 'session.voice'."})
}

func TestUnknownAndGarbageMessages(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"rate_limits.updated","rate_limits":[]}`)))
	evEq(t, d.decode([]byte(`{oops`)), voice.Event{Kind: voice.EvError, Message: "unparseable server message"})
}

func TestEncodeAudioAndClose(t *testing.T) {
	d := &decoder{}
	jeq(t, d.encode(voice.Command{Kind: voice.CmdAudio, Audio: []int16{258}}), `[{"type":"input_audio_buffer.append","audio":"AgE="}]`)
	if m := d.encode(voice.Command{Kind: voice.CmdClose}); len(m) != 0 {
		t.Fatal(m)
	}
}

func TestEncodeCancelTruncatesWhatWasHeard(t *testing.T) {
	d := &decoder{}
	d.decode([]byte(`{"type":"response.created","response":{"id":"r"}}`))
	d.decode([]byte(`{"type":"response.output_audio.delta","item_id":"item_9","delta":"AAA="}`))
	jeq(t, d.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 1234}),
		`[{"type":"response.cancel"},{"type":"conversation.item.truncate","item_id":"item_9","content_index":0,"audio_end_ms":1234}]`)
	d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"cancelled","output":[]}}`))
	// server already cancelled (interrupt_response): only truncate
	jeq(t, d.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 5}),
		`[{"type":"conversation.item.truncate","item_id":"item_9","content_index":0,"audio_end_ms":5}]`)
}

func TestResponseCreatedClearsTheStaleItem(t *testing.T) {
	d := &decoder{}
	d.decode([]byte(`{"type":"response.created","response":{"id":"r1"}}`))
	d.decode([]byte(`{"type":"response.output_audio.delta","item_id":"old","delta":"AAA="}`))
	d.decode([]byte(`{"type":"response.done","response":{"id":"r1","status":"completed","output":[]}}`))
	if d.currentItem != "old" {
		t.Fatal("kept after done for the server-cancelled path")
	}
	d.decode([]byte(`{"type":"response.created","response":{"id":"r2"}}`))
	if d.currentItem != "" {
		t.Fatal(d.currentItem)
	}
	jeq(t, d.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 9}), `[{"type":"response.cancel"}]`)
}

func TestCancelledResponseEmitsNoToolCalls(t *testing.T) {
	d := &decoder{}
	evEq(t, d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"cancelled","output":[
		{"id":"i","type":"function_call","status":"completed","name":"end_conversation","call_id":"c","arguments":"{}"}]}}`)),
		voice.Event{Kind: voice.EvResponseDone})
	evEq(t, d.decode([]byte(`{"type":"response.done","response":{"id":"r","status":"completed","output":[
		{"id":"i","type":"function_call","status":"in_progress","name":"x","call_id":"c","arguments":"{}"}]}}`)),
		voice.Event{Kind: voice.EvResponseDone})
}

func TestSamplesSplitAcrossDeltasReassemble(t *testing.T) {
	d := &decoder{}
	b := audio.EncodeLE([]int16{258, -2, 7})
	m := func(x []byte) []byte {
		out, _ := json.Marshal(map[string]any{"type": "response.output_audio.delta", "item_id": "i", "delta": base64.StdEncoding.EncodeToString(x)})
		return out
	}
	evEq(t, d.decode(m(b[:3])), voice.Event{Kind: voice.EvAudioDelta, Audio: []int16{258}})
	evEq(t, d.decode(m(b[3:])), voice.Event{Kind: voice.EvAudioDelta, Audio: []int16{-2, 7}})
}

func TestEncodeToolResult(t *testing.T) {
	d := &decoder{}
	out := json.RawMessage(`{"ok":true}`)
	jeq(t, d.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "call_7", Output: out, Respond: true}),
		`[{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call_7","output":"{\"ok\":true}"}},{"type":"response.create"}]`)
	if n := len(d.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "c", Output: out})); n != 1 {
		t.Fatal("respond: false → no response.create")
	}
}

func TestEncodeInjectAddsAUserTextItemAndAsksForAReply(t *testing.T) {
	d := &decoder{}
	jeq(t, d.encode(voice.Command{Kind: voice.CmdInject, Text: "Agent reply: done.", Respond: true}),
		`[{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"Agent reply: done."}]}},{"type":"response.create"}]`)
	if n := len(d.encode(voice.Command{Kind: voice.CmdInject, Text: "x"})); n != 1 {
		t.Fatal("respond: false → no response.create")
	}
}

// session.rs connects_configures_and_streams: the server closes after two messages
// (session.update, then the audio append) and the stream ends.
func TestConnectsConfiguresAndStreams(t *testing.T) {
	second := make(chan map[string]any, 1)
	srv := start(t, true, func(s *srvConn) { second <- s.read() }, nil, nil)
	s := connect(t, srv)
	if ev := next(t, s); ev.Kind != voice.EvReady {
		t.Fatalf("%+v", ev)
	}
	if err := s.Send(voice.Command{Kind: voice.CmdAudio, Audio: []int16{258}}); err != nil {
		t.Fatal(err)
	}
	jeq(t, <-second, `{"type":"input_audio_buffer.append","audio":"AgE="}`)
	for ev := range s.Events() { // the hang-up ends the stream (with a fatal error in Go)
		if ev.Kind != voice.EvError {
			t.Fatalf("%+v", ev)
		}
	}
}

// session.rs ready_is_first_event: Connect has already waited for session.updated, so Ready is
// queued before anything else.
func TestReadyIsFirstEvent(t *testing.T) {
	srv := start(t, true, func(s *srvConn) { <-s.ctx.Done() }, nil, nil)
	s := connect(t, srv)
	select {
	case ev := <-s.Events():
		if ev.Kind != voice.EvReady {
			t.Fatalf("%+v", ev)
		}
	default:
		t.Fatal("Ready not queued when Connect returned")
	}
}
