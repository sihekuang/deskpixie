package gemini

// Port of ai-mascot crates/voice-gemini/src/protocol.rs tests (29/29) and trace_tests (1/1).

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

func protoCfg() voice.SessionConfig {
	return voice.SessionConfig{
		Model:        "gemini-3.8-live",
		Voice:        "Puck",
		Instructions: "You are Bit.",
		Tools: []voice.ToolSpec{
			{Name: "end_conversation", Description: "End it.",
				Parameters: json.RawMessage(`{ "type": "object", "properties": {}, "additionalProperties": false }`)},
			{Name: "delegate", Description: "Hand work to the agent.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": { "task": { "type": "string", "description": "What to do." } },
					"required": ["task"],
					"additionalProperties": false
				}`)},
		},
		TurnDetection: voice.TurnSemantic,
	}
}

func audioMsg(pcm []int16) string {
	return fmt.Sprintf(`{"serverContent":{"modelTurn":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":%q}}]}}}`, pcmToBase64(pcm))
}

const (
	setupComplete = `{"setupComplete":{}}`
	turnComplete  = `{"serverContent":{"turnComplete":true}}`
	interrupted   = `{"serverContent":{"interrupted":true}}`
	activityStart = `{"voiceActivity":{"type":"ACTIVITY_START"}}`
	activityEnd   = `{"voiceActivity":{"type":"ACTIVITY_END"}}`
)

// Event shorthands for comparisons.
const (
	eReady   = "Ready"
	eStarted = "SpeechStarted"
	eStopped = "SpeechStopped"
	eCreated = "ResponseCreated"
	eDone    = "ResponseDone"
)

func eAudio(s ...int16) string           { return fmt.Sprint("Audio", s) }
func eUser(text string) string           { return "Transcript user: " + text }
func eAssistant(text string) string      { return "Transcript assistant: " + text }
func eTool(id, name, args string) string { return "ToolCall " + id + " " + name + " " + compact(args) }
func eWarn(msg string) string            { return "Error: " + msg }
func eFatal(msg string) string           { return "Error fatal: " + msg }

func compact(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "<bad json " + s + ">"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func show(evs []voice.Event) []string {
	out := []string{}
	for _, e := range evs {
		switch e.Kind {
		case voice.EvReady:
			out = append(out, eReady)
		case voice.EvSpeechStarted:
			out = append(out, eStarted)
		case voice.EvSpeechStopped:
			out = append(out, eStopped)
		case voice.EvResponseCreated:
			out = append(out, eCreated)
		case voice.EvResponseDone:
			out = append(out, eDone)
		case voice.EvAudioDelta:
			out = append(out, eAudio(e.Audio...))
		case voice.EvTranscript:
			out = append(out, "Transcript "+e.Role+": "+e.Text)
		case voice.EvToolCall:
			out = append(out, eTool(e.CallID, e.ToolName, string(e.Args)))
		case voice.EvError:
			if e.Fatal {
				out = append(out, eFatal(e.Message))
			} else {
				out = append(out, eWarn(e.Message))
			}
		default:
			out = append(out, fmt.Sprintf("kind %d", e.Kind))
		}
	}
	return out
}

func want(t *testing.T, got []voice.Event, w ...string) {
	t.Helper()
	if w == nil {
		w = []string{}
	}
	if s := show(got); !reflect.DeepEqual(s, w) {
		t.Fatalf("\n got: %q\nwant: %q", s, w)
	}
}

func ready(t *testing.T) *codec {
	t.Helper()
	c := newCodec()
	want(t, c.decode(setupComplete), eReady)
	return c
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// at walks nested objects (string keys) and arrays (int indexes).
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func assertEmpty(t *testing.T, e encoded) {
	t.Helper()
	if len(e.messages) != 0 || len(e.events) != 0 {
		t.Fatalf("want nothing, got %+v", e)
	}
}

func TestANoisyRoomLowersTheStartOfSpeechSensitivity(t *testing.T) {
	c := protoCfg()
	c.NoisyRoom = true
	jsonEq(t, at(asMap(t, setupMessage(c)), "setup", "realtimeInputConfig", "automaticActivityDetection"),
		`{ "disabled": false, "startOfSpeechSensitivity": "START_SENSITIVITY_LOW" }`)
	c.TurnDetection = voice.TurnManual
	jsonEq(t, at(asMap(t, setupMessage(c)), "setup", "realtimeInputConfig", "automaticActivityDetection"), `{ "disabled": true }`)
}

func TestSetupMessageHasTheDocumentedShape(t *testing.T) {
	v := asMap(t, setupMessage(protoCfg()))
	s := v["setup"]
	if at(s, "model") != "models/gemini-3.8-live" {
		t.Fatal(at(s, "model"))
	}
	jsonEq(t, at(s, "generationConfig", "responseModalities"), `["AUDIO"]`)
	if got := at(s, "generationConfig", "speechConfig", "voiceConfig", "prebuiltVoiceConfig", "voiceName"); got != "Puck" {
		t.Fatal(got)
	}
	jsonEq(t, at(s, "systemInstruction"), `{ "parts": [{ "text": "You are Bit." }] }`)
	jsonEq(t, at(s, "realtimeInputConfig"), `{ "automaticActivityDetection": { "disabled": false }, "activityHandling": "START_OF_ACTIVITY_INTERRUPTS" }`)
	jsonEq(t, at(s, "inputAudioTranscription"), `{}`)
	jsonEq(t, at(s, "outputAudioTranscription"), `{}`)
	jsonEq(t, at(s, "tools"), `[{ "functionDeclarations": [
		{ "name": "end_conversation", "description": "End it.", "behavior": "BLOCKING" },
		{ "name": "delegate", "description": "Hand work to the agent.", "behavior": "BLOCKING",
		  "parameters": { "type": "OBJECT", "properties": { "task": { "type": "STRING", "description": "What to do." } }, "required": ["task"] } }
	] }]`)
	if len(v) != 1 {
		t.Fatalf("exactly one top-level field: %v", v)
	}
	// #57: no toolConfig, so function calling stays on AUTO and nothing holds delegate back.
	if _, ok := s.(map[string]any)["toolConfig"]; ok {
		t.Fatal("toolConfig present")
	}
	b, _ := json.Marshal(setupMessage(protoCfg()))
	if strings.Contains(string(b), "additionalProperties") {
		t.Fatal(string(b))
	}
}

func TestSetupVariants(t *testing.T) {
	c := protoCfg()
	c.Tools = nil
	c.Instructions = "  "
	c.Model = "models/gemini-3.1-flash-live-preview"
	c.TurnDetection = voice.TurnManual
	s := asMap(t, setupMessage(c))["setup"].(map[string]any)
	if _, ok := s["tools"]; ok {
		t.Fatal("tools present")
	}
	if _, ok := s["systemInstruction"]; ok {
		t.Fatal("systemInstruction present")
	}
	if s["model"] != "models/gemini-3.1-flash-live-preview" {
		t.Fatal(s["model"])
	}
	if at(s, "realtimeInputConfig", "automaticActivityDetection", "disabled") != true {
		t.Fatal("manual should disable detection")
	}
	c.TurnDetection = voice.TurnServerVAD
	if at(asMap(t, setupMessage(c)), "setup", "realtimeInputConfig", "automaticActivityDetection", "disabled") != false {
		t.Fatal("server VAD should enable detection")
	}
}

func TestSetupCompleteIsReadyOnce(t *testing.T) {
	c := ready(t)
	want(t, c.decode(setupComplete))
	want(t, c.decode(`{"usageMetadata":{"totalTokenCount":5}}`))
	want(t, c.decode(`{"sessionResumptionUpdate":{"newHandle":"h","resumable":true}}`))
}

func TestAudioGoesOutAt16kWithTheDocumentedMime(t *testing.T) {
	c := ready(t)
	frame := make([]int16, 2400)
	for i := range frame {
		frame[i] = 100
	}
	enc := c.encode(voice.Command{Kind: voice.CmdAudio, Audio: frame})
	if len(enc.events) != 0 || len(enc.messages) != 1 {
		t.Fatalf("%+v", enc)
	}
	a := at(asMap(t, enc.messages[0]), "realtimeInput", "audio")
	if at(a, "mimeType") != "audio/pcm;rate=16000" {
		t.Fatal(a)
	}
	bytes, err := base64.StdEncoding.DecodeString(at(a, "data").(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) != 1600*2 {
		t.Fatalf("100 ms at 16 kHz, 16-bit: %d", len(bytes))
	}
	if int16(binary.LittleEndian.Uint16(bytes)) != 100 {
		t.Fatal(bytes[:2])
	}
	if enc := c.encode(voice.Command{Kind: voice.CmdAudio}); len(enc.messages) != 0 {
		t.Fatal(enc)
	}
	assertEmpty(t, c.encode(voice.Command{Kind: voice.CmdClose}))
}

func TestAPlainTurnWithoutVoiceActivityDerivesSpeechEvents(t *testing.T) {
	c := ready(t)
	// The user's first words mark them speaking (stops the silence timer).
	want(t, c.decode(`{"serverContent":{"inputTranscription":{"text":"What's two"}}}`), eStarted)
	want(t, c.decode(`{"serverContent":{"inputTranscription":{"text":" plus two?"}}}`))
	want(t, c.decode(audioMsg([]int16{5, -5})), eStopped, eCreated, eAudio(5, -5))
	if !c.responseOpen() {
		t.Fatal("response should be open")
	}
	want(t, c.decode(audioMsg([]int16{7})), eAudio(7))
	want(t, c.decode(`{"serverContent":{"outputTranscription":{"text":"Four"}}}`))
	want(t, c.decode(`{"serverContent":{"outputTranscription":{"text":"!"}}}`))
	want(t, c.decode(`{"serverContent":{"generationComplete":true}}`))
	want(t, c.decode(turnComplete), eUser("What's two plus two?"), eAssistant("Four!"), eDone)
	if c.responseOpen() {
		t.Fatal("response should be closed")
	}
	want(t, c.decode(turnComplete)) // a second turnComplete closes nothing
}

func TestTranscriptionRightAfterAReplyIsLateNotNewSpeech(t *testing.T) {
	t0 := time.Now()
	c := ready(t)
	c.decodeAt(audioMsg([]int16{1}), t0)
	c.decodeAt(turnComplete, t0)
	words := `{"serverContent":{"inputTranscription":{"text":"tail"}}}`
	want(t, c.decodeAt(words, t0.Add(lateTranscriptGrace/2))) // within the grace period
	// Later words are a new turn; the late fragment is flushed first.
	want(t, c.decodeAt(words, t0.Add(lateTranscriptGrace)), eUser("tail"), eStarted)
	// Words while the reply is still open never count as speech.
	c.decodeAt(audioMsg([]int16{1}), t0.Add(5*time.Second))
	want(t, c.decodeAt(words, t0.Add(9*time.Second)))
}

func TestAUserTurnWithNoReplyIsStoppedAfterAQuietPeriod(t *testing.T) {
	t0 := time.Now()
	c := ready(t)
	c.decodeAt(audioMsg([]int16{1}), t0)
	want(t, c.decodeAt(interrupted, t0), eStarted, eDone)
	want(t, c.decodeAt(turnComplete, t0)) // the user is still talking after a barge-in
	want(t, c.poll(t0.Add(userQuiet-time.Millisecond)))
	// More words push the stop out.
	words := `{"serverContent":{"inputTranscription":{"text":"wait"}}}`
	want(t, c.decodeAt(words, t0.Add(1500*time.Millisecond)))
	want(t, c.poll(t0.Add(userQuiet)))
	want(t, c.poll(t0.Add(1500*time.Millisecond+userQuiet)), eStopped)
	want(t, c.poll(t0.Add(60*time.Second))) // once
	// The model may still answer: no second stop, no synthesised pair.
	want(t, c.decodeAt(audioMsg([]int16{2}), t0.Add(61*time.Second)), eCreated, eAudio(2))
}

func TestWithServerVoiceActivityALongSentenceIsNotSplit(t *testing.T) {
	// As measured on gemini-3.8-live (#73): ACTIVITY_START at once, then nothing until the
	// whole transcript and ACTIVITY_END arrive together after the user stops.
	t0 := time.Now()
	c := ready(t)
	want(t, c.decodeAt(activityStart, t0), eStarted)
	want(t, c.poll(t0.Add(5*time.Second))) // still talking: no quiet stop
	said := `{"serverContent":{"inputTranscription":{"text":"Can you tell me a long story?"}}}`
	want(t, c.decodeAt(said, t0.Add(5600*time.Millisecond))) // no second SpeechStarted
	want(t, c.decodeAt(activityEnd, t0.Add(5600*time.Millisecond)), eStopped)
}

func TestWithServerVoiceActivityATranscriptAfterTheEndIsNotANewTurn(t *testing.T) {
	c := ready(t)
	c.decode(activityStart)
	want(t, c.decode(activityEnd), eStopped)
	want(t, c.decode(`{"serverContent":{"inputTranscription":{"text":"What's two plus two?"}}}`))
	want(t, c.decode(audioMsg([]int16{1})), eCreated, eAudio(1))
}

func TestWithServerVoiceActivityAMissingEndStillTimesOut(t *testing.T) {
	t0 := time.Now()
	c := ready(t)
	c.decodeAt(activityStart, t0)
	want(t, c.poll(t0.Add(userQuietWithVAD-time.Millisecond)))
	want(t, c.poll(t0.Add(userQuietWithVAD)), eStopped)
}

func TestQuietPeriodDoesNotStopSpeechWhileAReplyIsOpen(t *testing.T) {
	t0 := time.Now()
	c := ready(t)
	c.decodeAt(audioMsg([]int16{1}), t0)
	c.decodeAt(activityStart, t0)
	if !c.responseOpen() {
		t.Fatal("response should be open")
	}
	want(t, c.poll(t0.Add(userQuiet*10)))
}

func TestOwnInterruptExpiresAtTurnComplete(t *testing.T) {
	c := ready(t)
	c.decode(audioMsg([]int16{1}))
	c.encode(voice.Command{Kind: voice.CmdInject, Text: "x"})
	want(t, c.decode(turnComplete), eDone) // the reply ended on its own
	c.decode(audioMsg([]int16{2}))
	want(t, c.decode(interrupted), eStarted, eDone) // a later barge-in is the user
}

func TestAToolCallInADroppedTurnIsAnsweredAsCancelled(t *testing.T) {
	c := ready(t)
	c.decode(audioMsg([]int16{1}))
	c.encode(voice.Command{Kind: voice.CmdCancelResponse})
	want(t, c.decode(`{"toolCall":{"functionCalls":[{"id":"x","name":"delegate","args":{"task":"t"}}]}}`))
	jsonEq(t, c.takeOutgoing(), `[{ "toolResponse": { "functionResponses": [
		{ "id": "x", "name": "delegate", "response": { "error": "cancelled: the user interrupted this reply" } }
	] } }]`)
	if out := c.takeOutgoing(); len(out) != 0 {
		t.Fatal(out)
	}
}

func TestUserSpeechAfterAToolResultIsANewTurn(t *testing.T) {
	t0 := time.Now()
	c := ready(t)
	c.decodeAt(`{"toolCall":{"functionCalls":[{"id":"a","name":"delegate","args":{}}]}}`, t0)
	c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "a", Output: json.RawMessage(`{}`), Respond: true})
	later := t0.Add(lateTranscriptGrace)
	want(t, c.decodeAt(`{"serverContent":{"inputTranscription":{"text":"actually"}}}`, later), eStarted)
	want(t, c.decodeAt(audioMsg([]int16{1}), later), eStopped, eCreated, eAudio(1))
}

func TestVoiceActivityDrivesSpeechEventsWhenPresent(t *testing.T) {
	c := ready(t)
	want(t, c.decode(activityStart), eStarted)
	want(t, c.decode(activityStart)) // no duplicate start
	want(t, c.decode(`{"voiceActivity":{"type":"ACTIVITY_END","audioOffset":"1.2s"}}`), eStopped)
	want(t, c.decode(audioMsg([]int16{1})), eCreated, eAudio(1)) // nothing synthesised
	// Started but the end never reported: the reply implies it.
	c.decode(turnComplete)
	c.decode(activityStart)
	want(t, c.decode(audioMsg([]int16{1})), eStopped, eCreated, eAudio(1))
}

func TestSeveralFieldsInOneMessageAreHandledInOrder(t *testing.T) {
	c := ready(t)
	msg := fmt.Sprintf(`{ "serverContent": {
		"modelTurn": { "parts": [{ "inlineData": { "mimeType": "audio/pcm;rate=24000", "data": %q } }] },
		"outputTranscription": { "text": "Hi" },
		"turnComplete": true
	}}`, pcmToBase64([]int16{3}))
	want(t, c.decode(msg), eStarted, eStopped, eCreated, eAudio(3), eAssistant("Hi"), eDone)
}

func TestBargeInFromTheServerIsSpeechStartedThenDone(t *testing.T) {
	c := ready(t)
	c.decode(audioMsg([]int16{1}))
	want(t, c.decode(interrupted), eStarted, eDone)
	want(t, c.decode(turnComplete)) // interrupted → turnComplete closes once
	// The user's new question is answered: only the stop is implied now.
	want(t, c.decode(audioMsg([]int16{2})), eStopped, eCreated, eAudio(2))
	// The conversation's Interrupt → CancelResponse after a server interrupt is a no-op.
	c.decode(interrupted)
	assertEmpty(t, c.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 9}))
}

func TestLocalCancelClosesNowAndDropsTheRestOfTheTurn(t *testing.T) {
	c := ready(t)
	c.decode(audioMsg([]int16{1}))
	enc := c.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 200})
	if len(enc.messages) != 0 {
		t.Fatal("Gemini has no cancel message")
	}
	want(t, enc.events, eDone)
	want(t, c.decode(audioMsg([]int16{2}))) // late audio is dropped
	want(t, c.decode(`{"serverContent":{"outputTranscription":{"text":"late"}}}`))
	want(t, c.decode(`{"toolCall":{"functionCalls":[{"id":"x","name":"end_conversation","args":{}}]}}`))
	want(t, c.decode(turnComplete))
	// The next turn plays again.
	want(t, c.decode(audioMsg([]int16{3})), eStarted, eStopped, eCreated, eAudio(3))
	// Cancel with no open response does nothing and mutes nothing.
	c.decode(turnComplete)
	assertEmpty(t, c.encode(voice.Command{Kind: voice.CmdCancelResponse}))
	if n := len(c.decode(audioMsg([]int16{4}))); n != 4 {
		t.Fatal(n)
	}
}

func TestInterruptedAlsoEndsALocalMute(t *testing.T) {
	c := ready(t)
	c.decode(audioMsg([]int16{1}))
	c.encode(voice.Command{Kind: voice.CmdCancelResponse})
	want(t, c.decode(interrupted), eStarted)
	want(t, c.decode(audioMsg([]int16{2})), eStopped, eCreated, eAudio(2))
}

func TestToolCallsComeBeforeResponseDoneAndResultsGoBack(t *testing.T) {
	c := ready(t)
	c.decode(`{"serverContent":{"inputTranscription":{"text":"bye"}}}`)
	c.decode(audioMsg([]int16{1}))
	c.decode(`{"serverContent":{"outputTranscription":{"text":"Bye!"}}}`)
	call := `{"toolCall":{"functionCalls":[
		{"id":"fc_1","name":"end_conversation","args":{}},
		{"id":"fc_2","name":"delegate","args":{"task":"x"}},
		{"id":"fc_3","name":"odd"}
	]}}`
	want(t, c.decode(call),
		eUser("bye"),
		eTool("fc_1", "end_conversation", `{}`),
		eTool("fc_2", "delegate", `{ "task": "x" }`),
		eTool("fc_3", "odd", `{}`),
		eAssistant("Bye!"),
		eDone)
	enc := c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "fc_2", Output: json.RawMessage(`{ "ok": true }`), Respond: true})
	jsonEq(t, enc.messages, `[{ "toolResponse": { "functionResponses": [
		{ "id": "fc_2", "name": "delegate", "response": { "output": { "ok": true } } }
	] } }]`)
	if len(enc.events) != 0 {
		t.Fatal(enc.events)
	}
	// The continuation is a new response with no speech events.
	want(t, c.decode(audioMsg([]int16{9})), eCreated, eAudio(9))
	want(t, c.decode(turnComplete), eDone)
}

func TestAToolCallAloneStillOpensAndClosesAResponse(t *testing.T) {
	c := ready(t)
	want(t, c.decode(`{"toolCall":{"functionCalls":[{"id":"a","name":"end_conversation","args":{}}]}}`),
		eStarted, eStopped, eCreated, eTool("a", "end_conversation", `{}`), eDone)
	// The model goes on talking in the same turn (async tools): a continuation, not a new user turn.
	want(t, c.decode(audioMsg([]int16{1})), eCreated, eAudio(1))
}

func TestRespondFalseSendsTheResultAndDropsAnyReplyUntilTheUserSpeaks(t *testing.T) {
	c := ready(t)
	c.decode(`{"toolCall":{"functionCalls":[{"id":"a","name":"end_conversation","args":{}}]}}`)
	enc := c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "a", Output: json.RawMessage(`{ "ok": true }`)})
	if got := at(asMap(t, enc.messages[0]), "toolResponse", "functionResponses", 0, "name"); got != "end_conversation" {
		t.Fatal(got)
	}
	want(t, c.decode(audioMsg([]int16{1})))
	want(t, c.decode(turnComplete))
	want(t, c.decode(audioMsg([]int16{1}))) // still muted after turnComplete
	want(t, c.decode(activityStart), eStarted)
	want(t, c.decode(audioMsg([]int16{2})), eStopped, eCreated, eAudio(2))
}

func TestCancelledToolCallsAreWarnedAndTheirResultsDropped(t *testing.T) {
	c := ready(t)
	c.decode(`{"toolCall":{"functionCalls":[{"id":"a","name":"delegate","args":{"task":"t"}}]}}`)
	want(t, c.decode(`{"toolCallCancellation":{"ids":["a"]}}`), eWarn("tool call cancelled: a"))
	assertEmpty(t, c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "a", Output: json.RawMessage(`1`), Respond: true}))
	// Only once: a reused id later is answered normally.
	if n := len(c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "a", Output: json.RawMessage(`1`), Respond: true}).messages); n != 1 {
		t.Fatal(n)
	}
}

func TestInjectIsClientContentAndItsOwnInterruptIsNotSpeech(t *testing.T) {
	c := ready(t)
	enc := c.encode(voice.Command{Kind: voice.CmdInject, Text: "Agent reply: done.", Respond: true})
	jsonEq(t, enc.messages, `[{ "clientContent": { "turns": [{ "role": "user", "parts": [{ "text": "Agent reply: done." }] }], "turnComplete": true } }]`)
	want(t, c.decode(audioMsg([]int16{1})), eCreated, eAudio(1)) // no speech events
	// Injected mid-reply: the interrupt that follows is ours.
	c.encode(voice.Command{Kind: voice.CmdInject, Text: "more", Respond: true})
	want(t, c.decode(interrupted), eDone)
	want(t, c.decode(audioMsg([]int16{2})), eCreated, eAudio(2))
	quiet := c.encode(voice.Command{Kind: voice.CmdInject, Text: "fyi"})
	if got := at(asMap(t, quiet.messages[0]), "clientContent", "turnComplete"); got != false {
		t.Fatal(got)
	}
}

func TestGoAwayAndGarbageAreWarnings(t *testing.T) {
	c := ready(t)
	want(t, c.decode(`{"goAway":{"timeLeft":"10s"}}`), eWarn("server will close the connection (time left: 10s)"))
	want(t, c.decode("{oops"), eWarn("unparseable server message"))
	want(t, c.decode(`{"somethingNew":{}}`))
}

func TestCloseCodes(t *testing.T) {
	ev, ok := closeEvent(1008, "API key not valid")
	if !ok {
		t.Fatal("1008 should be an event")
	}
	want(t, []voice.Event{ev}, eFatal("closed (1008): API key not valid"))
	if ev, ok := closeEvent(1007, "bad"); !ok || !ev.Fatal || ev.Kind != voice.EvError {
		t.Fatal(ev, ok)
	}
	if _, ok := closeEvent(1000, ""); ok {
		t.Fatal("1000")
	}
	if _, ok := closeEvent(1011, "internal"); ok {
		t.Fatal("1011")
	}
}

func TestSamplesSplitAcrossChunksReassembleAndThoughtsAreSkipped(t *testing.T) {
	c := ready(t)
	var bytes []byte
	for _, s := range []int16{258, -2, 7} {
		bytes = binary.LittleEndian.AppendUint16(bytes, uint16(s))
	}
	m := func(b []byte) string {
		return fmt.Sprintf(`{ "serverContent": { "modelTurn": { "parts": [
			{ "text": "thinking...", "thought": true },
			{ "inlineData": { "mimeType": "audio/pcm;rate=24000", "data": %q } }
		] } } }`, base64.StdEncoding.EncodeToString(b))
	}
	got := show(c.decode(m(bytes[:3])))
	if got[len(got)-1] != eAudio(258) {
		t.Fatal(got)
	}
	want(t, c.decode(m(bytes[3:])), eAudio(-2, 7))
	bad := `{ "serverContent": { "modelTurn": { "parts": [{ "inlineData": { "data": "not base64!" } }] } } }`
	want(t, c.decode(bad), eWarn("bad audio chunk"))
}

func TestOtherOutputRatesAreResampledTo24k(t *testing.T) {
	if r, ok := rateOf("audio/pcm;rate=16000"); !ok || r != 16_000 {
		t.Fatal(r, ok)
	}
	if r, ok := rateOf("audio/pcm; rate=24000"); !ok || r != 24_000 {
		t.Fatal(r, ok)
	}
	if _, ok := rateOf("audio/pcm"); ok {
		t.Fatal("no rate")
	}
	c := ready(t)
	msg := fmt.Sprintf(`{ "serverContent": { "modelTurn": { "parts": [
		{ "inlineData": { "mimeType": "audio/pcm;rate=16000", "data": %q } }
	] } } }`, pcmToBase64(make([]int16, 1600)))
	evs := c.decode(msg)
	last := evs[len(evs)-1]
	if last.Kind != voice.EvAudioDelta {
		t.Fatal(show(evs))
	}
	if n := len(last.Audio); n < 2397 || n > 2400 {
		t.Fatal(n)
	}
}

func TestAudioDataIsElidedAndEverythingElseKept(t *testing.T) {
	msg := `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AAAABBBB"}}]},"inputTranscription":{"text":"hi"}}}`
	if got := traceLine(msg); got != `{"serverContent":{"inputTranscription":{"text":"hi"},"modelTurn":{"parts":[{"inlineData":{"data":"<8 bytes>","mimeType":"audio/pcm;rate=24000"}}]}}}` {
		t.Fatal(got)
	}
	if got := traceLine("nope"); got != "<4 bytes, not JSON>" {
		t.Fatal(got)
	}
}
