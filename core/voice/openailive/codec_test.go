package openailive

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

func asFailure(err error, fe *voice.FailureError) bool { return errors.As(err, fe) }

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

func msg(v map[string]any) []byte { b, _ := json.Marshal(v); return b }

func tone(n int, v int16) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = v
	}
	return s
}

func outDelta(pcm []int16, startMs int64) []byte {
	return msg(map[string]any{"type": "session.output_audio.delta", "delta": pcmToBase64(pcm),
		"start_ms": startMs, "end_ms": startMs + int64(len(pcm)*1000/audio.SampleRate)})
}

func inText(t string) []byte {
	return msg(map[string]any{"type": "session.input_transcript.delta", "delta": t})
}

func backend(ev map[string]any) []byte {
	return msg(map[string]any{"type": "response.event", "delegation_id": "dg_1", "event": ev})
}

func kinds(evs []voice.Event) []voice.EventKind {
	out := make([]voice.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// wantKinds skips EvUserHeard and EvAssistantSaying, the live caption's words so far (#251,
// #253): TestTheUsersWordsAreHeardAsTheyCome and TestPixiesWordsAreSaidAsTheyCome check those.
func wantKinds(t *testing.T, evs []voice.Event, want ...voice.EventKind) {
	t.Helper()
	got := slices.DeleteFunc(kinds(evs), func(k voice.EventKind) bool {
		return k == voice.EvUserHeard || k == voice.EvAssistantSaying
	})
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

var loud, quiet = tone(2400, 8000), tone(2400, 0)

func TestSessionStartShape(t *testing.T) {
	cfg := voice.SessionConfig{Model: "gpt-live-1", Voice: "voice_abc", Instructions: "be Pixie", NoisyRoom: true,
		Tools: []voice.ToolSpec{{Name: "get_time", Description: "time", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)}, {Name: "end_conversation"}}}
	var got map[string]any
	_ = json.Unmarshal(msg(sessionStart(cfg, "c_1")), &got)
	if got["type"] != "session.start" || got["event_id"] != "c_1" {
		t.Fatalf("%v", got)
	}
	s := got["session"].(map[string]any)
	for k := range s {
		switch k {
		case "model", "instructions", "audio", "delegation", "store":
		default:
			t.Fatalf("unexpected session field %q (the config is strict)", k)
		}
	}
	if v := s["audio"].(map[string]any)["output"].(map[string]any)["voice"]; v.(map[string]any)["id"] != "voice_abc" {
		t.Fatalf("custom voice %v", v)
	}
	if f := s["audio"].(map[string]any)["format"].(map[string]any); f["rate"] != float64(24000) || f["type"] != "audio/pcm" {
		t.Fatalf("format %v", f)
	}
	instr := s["instructions"].(string)
	if !strings.HasPrefix(instr, "be Pixie\n\n") || !strings.Contains(instr, "Interruption policy") || !strings.Contains(instr, "The room is noisy") {
		t.Fatalf("instructions %q", instr)
	}
	d := s["delegation"].(map[string]any)
	if d["type"] != "responses" {
		t.Fatalf("delegation %v", d)
	}
	r := d["responses"].(map[string]any)
	if r["model"] != DefaultBackendModel || !strings.HasSuffix(r["instructions"].(string), "be Pixie") || r["tool_choice"] != "auto" {
		t.Fatalf("responses %v", r)
	}
	tools := r["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "get_time" || tools[1].(map[string]any)["parameters"] == nil {
		t.Fatalf("tools %v", tools)
	}
	// Defaults and a built-in voice name.
	_ = json.Unmarshal(msg(sessionStart(voice.SessionConfig{Voice: "cedar", BackendModel: "gpt-6-sol", BackendInstructions: "tools only"}, "c_1")), &got)
	s = got["session"].(map[string]any)
	if s["model"] != DefaultModel || s["audio"].(map[string]any)["output"].(map[string]any)["voice"] != "cedar" {
		t.Fatalf("%v", s)
	}
	r = s["delegation"].(map[string]any)["responses"].(map[string]any)
	if r["model"] != "gpt-6-sol" || !strings.HasSuffix(r["instructions"].(string), "tools only") {
		t.Fatalf("%v", r)
	}
	if strings.Contains(s["instructions"].(string), "noisy") {
		t.Fatal("noisy room line without NoisyRoom")
	}
}

func TestReadyOnce(t *testing.T) {
	c := newCodec()
	wantKinds(t, c.decode(msg(map[string]any{"type": "session.started"}), at(0)), voice.EvReady)
	wantKinds(t, c.decode(msg(map[string]any{"type": "session.started"}), at(0)))
}

// A turn: the user's words open their span; Pixie's first loud audio closes it and opens a
// reply; quiet ends the reply with its transcript.
func TestTurnFromTranscriptsAndAudio(t *testing.T) {
	c := newCodec()
	wantKinds(t, c.decode(outDelta(quiet, 0), at(0))) // idle track: nothing
	wantKinds(t, c.decode(inText("what time"), at(100)), voice.EvSpeechStarted)
	wantKinds(t, c.decode(inText(" is it"), at(200)))
	evs := c.decode(outDelta(loud, 300), at(600))
	wantKinds(t, evs, voice.EvTranscript, voice.EvSpeechStopped, voice.EvResponseCreated, voice.EvAudioDelta)
	if evs[0].Role != "user" || evs[0].Text != "what time is it" {
		t.Fatalf("user transcript %+v", evs[0])
	}
	c.decode(msg(map[string]any{"type": "session.output_transcript.delta", "delta": "It's noon."}), at(650))
	wantKinds(t, c.decode(outDelta(quiet, 400), at(700)), voice.EvAudioDelta) // pauses inside a reply play
	wantKinds(t, c.poll(at(1200)))
	evs = c.poll(at(1300))
	wantKinds(t, evs, voice.EvTranscript, voice.EvResponseDone)
	if evs[0].Role != "assistant" || evs[0].Text != "It's noon." {
		t.Fatalf("assistant transcript %+v", evs[0])
	}
	wantKinds(t, c.decode(outDelta(quiet, 500), at(1400))) // idle again
}

func TestUserTurnEndsOnQuiet(t *testing.T) {
	c := newCodec()
	c.decode(inText("hello"), at(0))
	wantKinds(t, c.poll(at(700)))
	evs := c.poll(at(800))
	wantKinds(t, evs, voice.EvTranscript, voice.EvSpeechStopped)
}

func TestBlankInputDoesNotStartATurn(t *testing.T) {
	c := newCodec()
	wantKinds(t, c.decode(inText("  "), at(0)))
}

// Omitted silence between deltas inside a reply is filled, at most assistantQuiet.
func TestGapInsideReplyIsFilled(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	evs := c.decode(outDelta(loud, 300), at(300))
	wantKinds(t, evs, voice.EvAudioDelta, voice.EvAudioDelta)
	if n := len(evs[0].Audio); n != 200*audio.SampleRate/1000 {
		t.Fatalf("gap fill %d samples", n)
	}
}

// The user talking over Pixie is GPT-Live's call: no SpeechStarted while Pixie talks. If Pixie
// stops, the user's turn starts then; if Pixie talks through it, it was a backchannel.
func TestBargeInDeferredUntilBitStops(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	wantKinds(t, c.decode(inText("wait"), at(100)))
	wantKinds(t, c.poll(at(400)))
	evs := c.poll(at(700)) // Pixie quiet since 0 ms + 700
	wantKinds(t, evs, voice.EvResponseDone, voice.EvSpeechStarted)
	wantKinds(t, c.poll(at(900)), voice.EvTranscript, voice.EvSpeechStopped)
}

func TestBackchannelIsNotATurn(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	c.decode(inText("yeah"), at(100))
	for ms := 100; ms <= 1000; ms += 100 {
		c.decode(outDelta(loud, int64(ms)), at(ms))
	}
	evs := c.poll(at(1000)) // user quiet 900 ms while Pixie still talks
	wantKinds(t, evs, voice.EvTranscript)
	if evs[0].Role != "user" {
		t.Fatalf("%+v", evs[0])
	}
	wantKinds(t, c.poll(at(1700)), voice.EvResponseDone) // no SpeechStarted
}

// Cancel ends the reply locally (no wire message) and drops its tail until quiet.
func TestCancelClosesAndMutes(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	msgs, evs := c.encode(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 50}, at(100))
	if len(msgs) != 0 {
		t.Fatalf("cancel sent %v", msgs)
	}
	wantKinds(t, evs, voice.EvResponseDone)
	wantKinds(t, c.decode(outDelta(loud, 100), at(200))) // tail dropped
	wantKinds(t, c.poll(at(800)))
	c.poll(at(900)) // 700 ms without loud output: unmuted
	wantKinds(t, c.decode(outDelta(loud, 900), at(950)), voice.EvResponseCreated, voice.EvAudioDelta)
}

func TestCancelMuteEndsWhenTheUserTalks(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	c.encode(voice.Command{Kind: voice.CmdCancelResponse}, at(100))
	c.decode(inText("no, I meant"), at(150))
	evs := c.decode(outDelta(loud, 200), at(200))
	wantKinds(t, evs, voice.EvTranscript, voice.EvSpeechStopped, voice.EvResponseCreated, voice.EvAudioDelta)
}

func TestCancelWhenQuietIsANoOp(t *testing.T) {
	c := newCodec()
	msgs, evs := c.encode(voice.Command{Kind: voice.CmdCancelResponse}, at(0))
	if len(msgs)+len(evs) != 0 {
		t.Fatal("cancel while quiet did something")
	}
	wantKinds(t, c.decode(outDelta(loud, 0), at(10)), voice.EvResponseCreated, voice.EvAudioDelta)
}

// Backend work holds the reply open so its tool calls come before ResponseDone; unanswered
// calls don't (the runtime answers after ResponseDone, like Realtime).
func TestDelegationHoldsReplyOpen(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	c.decode(msg(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dg_1", "target": "responses"}}), at(100))
	wantKinds(t, c.poll(at(1000))) // quiet, but the backend hasn't started
	c.decode(backend(map[string]any{"type": "response.created"}), at(1100))
	evs := c.decode(backend(map[string]any{"type": "response.output_item.done",
		"item": map[string]any{"type": "function_call", "name": "get_time", "call_id": "call_1", "arguments": `{"city":"Paris"}`}}), at(1200))
	wantKinds(t, evs, voice.EvToolCall)
	if evs[0].CallID != "call_1" || evs[0].ToolName != "get_time" || string(evs[0].Args) != `{"city":"Paris"}` {
		t.Fatalf("%+v", evs[0])
	}
	wantKinds(t, c.poll(at(1300)))
	c.decode(backend(map[string]any{"type": "response.completed"}), at(1400))
	wantKinds(t, c.poll(at(1450)), voice.EvResponseDone)
}

func TestStaleDelegationStopsHolding(t *testing.T) {
	c := newCodec()
	c.decode(outDelta(loud, 0), at(0))
	c.decode(msg(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"target": "responses"}}), at(0))
	wantKinds(t, c.poll(at(29_000)))
	wantKinds(t, c.poll(at(30_000)), voice.EvResponseDone)
}

func TestToolCallBadArgsAreWrapped(t *testing.T) {
	c := newCodec()
	evs := c.decode(backend(map[string]any{"type": "response.output_item.done",
		"item": map[string]any{"type": "function_call", "name": "x", "call_id": "c", "arguments": "not json"}}), at(0))
	if string(evs[0].Args) != `"not json"` {
		t.Fatalf("%s", evs[0].Args)
	}
	wantKinds(t, c.decode(backend(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message"}}), at(0)))
}

// Tool results: one response.create once every call of the batch is answered; none for a
// result that ends the conversation.
func TestToolResultsContinueTheBackendOnce(t *testing.T) {
	c := newCodec()
	for _, id := range []string{"a", "b"} {
		c.decode(backend(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "name": "x", "call_id": id}}), at(0))
	}
	msgs, _ := c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "a", Output: json.RawMessage(`{"ok":1}`), Respond: true}, at(0))
	if len(msgs) != 1 || msgs[0]["type"] != "response.item.create" {
		t.Fatalf("%v", msgs)
	}
	item := msgs[0]["item"].(obj)
	if item["type"] != "function_call_output" || item["call_id"] != "a" || item["output"] != `{"ok":1}` {
		t.Fatalf("%v", item)
	}
	msgs, _ = c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "b", Output: json.RawMessage(`{}`), Respond: true}, at(0))
	if len(msgs) != 2 || msgs[1]["type"] != "response.create" {
		t.Fatalf("%v", msgs)
	}
	c.decode(backend(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "name": "end_conversation", "call_id": "e"}}), at(0))
	msgs, _ = c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: "e", Output: json.RawMessage(`{}`), Respond: false}, at(0))
	if len(msgs) != 1 {
		t.Fatalf("end_conversation continued the backend: %v", msgs)
	}
}

func TestInjectKinds(t *testing.T) {
	c := newCodec()
	for _, tc := range []struct {
		text    string
		respond bool
		kind    string
	}{
		{"Coordinator reply: done.", true, "session.commentary.append"},
		{"Coordinator reply: done.", false, "session.thinking.append"},
		{"Coordinator question: delete it? Say confirm hawk.", true, "session.instructions.append"},
	} {
		msgs, _ := c.encode(voice.Command{Kind: voice.CmdInject, Text: tc.text, Respond: tc.respond}, at(0))
		if len(msgs) != 1 || msgs[0]["type"] != tc.kind || msgs[0]["delegation_id"] != nil {
			t.Fatalf("%q: %v", tc.text, msgs)
		}
		if !strings.Contains(msgs[0]["content"].(string), tc.text) {
			t.Fatalf("content %v", msgs[0]["content"])
		}
	}
}

func TestLongInjectIsSplit(t *testing.T) {
	c := newCodec()
	long := strings.Repeat("This is one sentence of an agent reply. ", 100)
	msgs, _ := c.encode(voice.Command{Kind: voice.CmdInject, Text: long, Respond: true}, at(0))
	if len(msgs) < 3 || msgs[0]["type"] != "session.commentary.append" || msgs[1]["type"] != "session.thinking.append" {
		t.Fatalf("%d msgs", len(msgs))
	}
	total := 0
	for _, m := range msgs {
		s := m["content"].(string)
		if len(s) > maxContent || !strings.HasSuffix(s, ".") {
			t.Fatalf("part %d bytes: %q…", len(s), s[:40])
		}
		total += len(s)
	}
	if total < len(strings.TrimSpace(long))-len(msgs) {
		t.Fatalf("lost text: %d of %d", total, len(long))
	}
}

func TestEventIDsAreUnique(t *testing.T) {
	c := newCodec()
	a, _ := c.encode(voice.Command{Kind: voice.CmdAudio, Audio: []int16{1, 2}}, at(0))
	b, _ := c.encode(voice.Command{Kind: voice.CmdAudio, Audio: []int16{1, 2}}, at(0))
	if a[0]["event_id"] == b[0]["event_id"] || a[0]["type"] != "session.input_audio.append" {
		t.Fatalf("%v %v", a, b)
	}
}

func TestOddByteCarriedAcrossDeltas(t *testing.T) {
	c := newCodec()
	raw := audio.EncodeLE([]int16{1000, 2000, 3000})
	enc := func(b []byte) []byte {
		return msg(map[string]any{"type": "session.output_audio.delta", "delta": b64(b)})
	}
	big := tone(2400, 8000)
	c.decode(outDelta(big, 0), at(0)) // open a reply
	e1 := c.decode(enc(raw[:3]), at(10))
	e2 := c.decode(enc(raw[3:]), at(20))
	if len(e1[0].Audio) != 1 || len(e2[0].Audio) != 2 || e2[0].Audio[0] != 2000 {
		t.Fatalf("%v %v", e1, e2)
	}
}

func TestServerErrorsAndClose(t *testing.T) {
	c := newCodec()
	evs := c.decode(msg(map[string]any{"type": "error", "error": map[string]any{"code": "moderation_blocked", "message": "cut"}}), at(0))
	if len(evs) != 1 || evs[0].Fatal {
		t.Fatalf("moderation should warn: %+v", evs)
	}
	evs = c.decode(msg(map[string]any{"type": "error", "error": map[string]any{"code": "responses_validation_failed", "message": "bad model"}}), at(0))
	if !evs[0].Fatal || evs[0].Failure.Status() != "Voice: GPT-Live backend settings rejected" {
		t.Fatalf("%+v", evs)
	}
	for reason, status := range map[string]string{
		"expired": "Voice: GPT-Live session expired", "content": "Voice: ended by the safety filter",
		"connection_lost": "Voice: connection lost",
	} {
		evs = c.decode(msg(map[string]any{"type": "session.closed", "reason": reason}), at(0))
		if len(evs) != 1 || !evs[0].Fatal || evs[0].Failure.Status() != status {
			t.Fatalf("%s: %+v", reason, evs)
		}
	}
	wantKinds(t, c.decode(msg(map[string]any{"type": "session.closed", "reason": "close_requested"}), at(0)))
	wantKinds(t, c.decode(msg(map[string]any{"type": "transport.something"}), at(0)))
}

func TestStartupFailures(t *testing.T) {
	for code, want := range map[string]string{
		"invalid_api_key":    "Voice: OpenAI rejected the API key",
		"model_not_found":    "Voice: this API key has no GPT-Live access",
		"permission_denied":  "Voice: this API key has no GPT-Live access",
		"insufficient_quota": "Voice: OpenAI quota or rate limit reached",
		"something_else":     "Voice: model error",
	} {
		if got := startupFailure(apiError{Code: code}).Status(); got != want {
			t.Errorf("%s: %q", code, got)
		}
	}
	for status, want := range map[int]string{401: "Voice: OpenAI rejected the API key", 403: "Voice: this API key has no GPT-Live access", 429: "Voice: OpenAI quota or rate limit reached"} {
		if f, ok := httpFailure(status); !ok || f.Status() != want {
			t.Errorf("%d: %q", status, f.Status())
		}
	}
	if _, ok := httpFailure(500); ok {
		t.Error("500 typed")
	}
}

func TestCapabilities(t *testing.T) {
	if c := voice.CapabilitiesOf(New("")); !c.OwnsTurnTaking || c.Truncate {
		t.Fatalf("%+v", c)
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// heardTexts are the EvUserHeard texts in evs: the user's words so far (#251).
func heardTexts(evs []voice.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == voice.EvUserHeard {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestTheUsersWordsAreHeardAsTheyCome(t *testing.T) {
	c := newCodec()
	if got := heardTexts(c.decode(inText("what time"), at(100))); !slices.Equal(got, []string{"what time"}) {
		t.Fatal(got)
	}
	if got := heardTexts(c.decode(inText(" is it"), at(200))); !slices.Equal(got, []string{"what time is it"}) {
		t.Fatal(got)
	}
	if got := heardTexts(c.decode(inText(" "), at(300))); got != nil {
		t.Fatal("nothing new heard:", got)
	}
}

func TestPixiesWordsAreSaidAsTheyCome(t *testing.T) {
	c := newCodec()
	out := func(t string) []byte {
		return msg(map[string]any{"type": "session.output_transcript.delta", "delta": t})
	}
	said := func(evs []voice.Event) []string {
		var got []string
		for _, e := range evs {
			if e.Kind == voice.EvAssistantSaying {
				got = append(got, e.Text)
			}
		}
		return got
	}
	if got := said(c.decode(out("It's"), at(100))); !slices.Equal(got, []string{"It's"}) {
		t.Fatal(got)
	}
	if got := said(c.decode(out(" sunny"), at(200))); !slices.Equal(got, []string{"It's sunny"}) {
		t.Fatal(got)
	}
	if got := said(c.decode(out(" "), at(300))); got != nil {
		t.Fatal("nothing new said:", got)
	}
}
