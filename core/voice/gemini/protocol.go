package gemini

// Gemini Live (BidiGenerateContent) wire format ⇄ the voice package's neutral types. Pure: no
// I/O. Port of voice-gemini/src/protocol.rs.
//
// Sources: the WebSocket API reference https://ai.google.dev/api/live, the capabilities guide
// https://ai.google.dev/gemini-api/docs/live-api/capabilities and the tools guide
// https://ai.google.dev/gemini-api/docs/live-api/tools.
//
// Wire facts relied on:
//   - Client messages carry exactly one of setup, clientContent, realtimeInput, toolResponse.
//   - setup comes first; the server answers setupComplete before anything else is sent.
//   - Input audio is raw little-endian PCM16 mono, audio/pcm;rate=16000, base64 in
//     realtimeInput.audio. Output audio is always 24 kHz PCM16 in
//     serverContent.modelTurn.parts[].inlineData.
//   - serverContent may carry several of: modelTurn, inputTranscription, outputTranscription,
//     interrupted, generationComplete, turnComplete in one message. An interrupted turn goes
//     interrupted → turnComplete with no generationComplete.
//   - toolCall.functionCalls[] = {id, name, args}; the client answers
//     toolResponse.functionResponses[] = {id, name, response} (matched by id).
//     toolCallCancellation.ids[] lists calls the server dropped (e.g. on barge-in).
//   - voiceActivity.type (ACTIVITY_START / ACTIVITY_END) may accompany a server message.
//   - goAway.timeLeft: the connection will be closed soon.
//   - clientContent interrupts any current generation; with turnComplete: true the model
//     answers the accumulated turns.
//
// Derived events (no direct Gemini equivalent). A "response" here is one span of model
// output, so the conversation state machine sees the same shape as with OpenAI Realtime:
//   - ResponseCreated is emitted before the first output (audio, output transcript or tool
//     call) after the previous response closed.
//   - ResponseDone closes it on turnComplete, interrupted, after a toolCall's ToolCall events
//     (the model pauses for the result; its continuation is a new response, like OpenAI's
//     follow-up response.create), or immediately on a client CancelResponse.
//   - SpeechStarted comes from voiceActivity ACTIVITY_START when the server sends it;
//     otherwise from the user's first inputTranscription words while no reply is open (so the
//     conversation's silence timer stops while they talk), or from interrupted (server VAD
//     heard the user talk over the reply). Words arriving within lateTranscriptGrace of a
//     reply's end are the previous turn's tail, not new speech. An interrupted caused by our
//     own clientContent (an Inject mid-reply) is not speech.
//   - SpeechStopped comes from voiceActivity ACTIVITY_END; otherwise it is emitted just before
//     the ResponseCreated that answers the user, or by codec.poll once the user has been
//     marked speaking with no reply open and nothing heard from them for userQuiet
//     (userQuietWithVAD once the server has sent voiceActivity) (a barge-in the model doesn't
//     answer), so the conversation's reply watchdog takes over. If no speech was reported at
//     all for a reply to user audio, the pair SpeechStarted, SpeechStopped is synthesised
//     before it, so every answered user turn shows started → stopped → reply, as with OpenAI.
//     Replies that follow a tool result or an injected message get no speech events, unless
//     the user spoke in between.
//   - Transcripts stream in fragments; each side's fragments are joined and emitted as one
//     Transcript when the response closes (before ToolCalls / ResponseDone), and leftover
//     user words before the next SpeechStarted.
//   - CancelResponse: Gemini has no cancel or truncate message. Server VAD already stops
//     generation on barge-in (activityHandling: START_OF_ACTIVITY_INTERRUPTS), so the adapter
//     closes the response locally and drops further output until the server's interrupted or
//     turnComplete. HeardMs cannot be sent: Gemini keeps what it streamed in its history. A
//     tool call in that dropped output is answered at once with an error response (the model
//     blocks until every call is answered), queued for codec.takeOutgoing.
//   - ToolResult with Respond false (end_conversation): the result is sent (the model blocks
//     until it gets one) and any reply to it is dropped until the user speaks again.
//   - Errors: goAway and toolCallCancellation are warnings; a close with code 1007 (invalid
//     request) or 1008 (policy, e.g. auth or quota) is fatal (see closeEvent).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	// sampleRate is the voice package's audio rate (24 kHz mono int16).
	sampleRate = 24_000
	// inputRate and inputMIME are what Gemini Live wants as input.
	inputRate = 16_000
	inputMIME = "audio/pcm;rate=16000"
	// outputRate is what Gemini Live sends (always, per the capabilities guide).
	outputRate = 24_000
	// userQuiet: with the user marked speaking, no reply open and nothing heard from them for
	// this long, the turn is over (Gemini's own end-of-speech silence is about 800 ms).
	userQuiet = 2 * time.Second
	// userQuietWithVAD is the same safety net once the server has sent voiceActivity: it then
	// ends turns itself with ACTIVITY_END, and its transcription arrives only with that end
	// (measured on gemini-3.8-live, ai-mascot #73), so a short quiet timer would split a long
	// sentence in two.
	userQuietWithVAD = 15 * time.Second
	// lateTranscriptGrace: user transcription this soon after a reply ends belongs to the
	// previous turn.
	lateTranscriptGrace = time.Second
)

type obj = map[string]any

// parseJSON decodes text keeping numbers exact (json.Number).
func parseJSON(text string) (any, error) {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("trailing data")
	}
	return v, nil
}

// marshal encodes without HTML escaping and without a trailing newline.
func marshal(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// traceLine renders a server message for the GEMINI_TRACE log: the JSON with every base64
// data string replaced by its length, so audio never floods the log.
func traceLine(text string) string {
	v, err := parseJSON(text)
	if err != nil {
		return fmt.Sprintf("<%d bytes, not JSON>", len(text))
	}
	var elide func(v any)
	elide = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if s, ok := child.(string); ok && k == "data" {
					t[k] = fmt.Sprintf("<%d bytes>", len(s))
				} else {
					elide(child)
				}
			}
		case []any:
			for _, c := range t {
				elide(c)
			}
		}
	}
	elide(v)
	return marshal(v)
}

// functionDeclarations is tools[].functionDeclarations[] for the session's tools.
// behavior: BLOCKING ("the system will wait to receive the function response before
// continuing") is the voice loop's contract. The API reference treats it as the default, but
// the Live capabilities table (checked 2026-10-12) lists gemini-3.8-live as asynchronous by
// default with "backwards-compatible blocking mode with behavior: BLOCKING", so it is sent
// explicitly. The Extended Thinking variant supports only NON_BLOCKING.
func functionDeclarations(tools []voice.ToolSpec) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		decl := obj{"name": t.Name, "description": t.Description, "behavior": "BLOCKING"}
		if p, ok := Parameters(t.Parameters); ok {
			decl["parameters"] = p
		}
		out = append(out, decl)
	}
	return out
}

// setupMessage is the first client message. cfg.Model and cfg.Voice must already be Gemini
// values (see Resolve).
func setupMessage(cfg voice.SessionConfig) obj {
	model := cfg.Model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	var detection obj
	switch {
	case cfg.TurnDetection == voice.TurnManual:
		detection = obj{"disabled": true}
	case cfg.NoisyRoom:
		// Low sensitivity: a TV or people nearby no longer cut Pixie off.
		detection = obj{"disabled": false, "startOfSpeechSensitivity": "START_SENSITIVITY_LOW"}
	default:
		detection = obj{"disabled": false}
	}
	setup := obj{
		"model": model,
		"generationConfig": obj{
			"responseModalities": []any{"AUDIO"},
			"speechConfig":       obj{"voiceConfig": obj{"prebuiltVoiceConfig": obj{"voiceName": cfg.Voice}}},
		},
		"realtimeInputConfig": obj{
			"automaticActivityDetection": detection,
			"activityHandling":           "START_OF_ACTIVITY_INTERRUPTS",
		},
		"inputAudioTranscription":  obj{},
		"outputAudioTranscription": obj{},
	}
	if strings.TrimSpace(cfg.Instructions) != "" {
		setup["systemInstruction"] = obj{"parts": []any{obj{"text": cfg.Instructions}}}
	}
	if len(cfg.Tools) > 0 {
		setup["tools"] = []any{obj{"functionDeclarations": functionDeclarations(cfg.Tools)}}
	}
	return obj{"setup": setup}
}

func pcmToBase64(pcm []int16) string { return base64.StdEncoding.EncodeToString(audio.EncodeLE(pcm)) }

// rateOf is the rate= of a PCM mime type such as audio/pcm;rate=24000.
func rateOf(mime string) (int, bool) {
	for _, p := range strings.Split(mime, ";") {
		if r, ok := strings.CutPrefix(strings.TrimSpace(p), "rate="); ok {
			if n, err := strconv.ParseUint(strings.TrimSpace(r), 10, 32); err == nil {
				return int(n), true
			}
		}
	}
	return 0, false
}

// closeEvent turns a WebSocket close from the server into an event: fatal for request and
// policy errors (1007, 1008: bad setup, auth, quota), ok=false for everything else (the
// stream just ends, which the conversation reports as a lost connection).
func closeEvent(code int, reason string) (voice.Event, bool) {
	if code != 1007 && code != 1008 {
		return voice.Event{}, false
	}
	return voice.Event{Kind: voice.EvError, Fatal: true, Message: fmt.Sprintf("closed (%d): %s", code, reason)}, true
}

// encoded is the client messages for a command, plus events the adapter reports locally.
type encoded struct {
	messages []obj
	events   []voice.Event
}

type userState int

const (
	userIdle     userState = iota // no user speech reported since the last response opened
	userSpeaking                  // SpeechStarted sent, SpeechStopped not yet
	userStopped                   // SpeechStopped sent; the next response answers it
)

type muteState int

const (
	muteOff          muteState = iota
	muteUntilTurnEnd           // after a local cancel: until the server's interrupted or turnComplete
	muteUntilSpeaks            // after Respond false: until the user speaks again
)

// codec is the stateful mapper for one session: decodes server messages and encodes
// commands. Not safe for concurrent use; the session guards it with a mutex.
type codec struct {
	ready, open bool
	user        userState
	mute        muteState
	// followup: the next response continues the model's turn (tool result, injected text):
	// no speech events.
	followup bool
	// ownInterrupt: we sent clientContent mid-response; the interrupted it causes is not the user.
	ownInterrupt  bool
	userText      strings.Builder
	assistantText strings.Builder
	calls         map[string]string // open tool calls: id → function name
	cancelled     map[string]bool
	input         *Resampler
	output        *Resampler
	pendingByte   []byte    // odd trailing byte of the previous audio chunk
	now           time.Time // the time of the message being decoded (or of the last poll)
	userHeardAt   time.Time // last sign of the user talking (speech start or transcribed words)
	vadSeen       bool      // the server has sent voiceActivity, so its ACTIVITY_END ends turns
	closedAt      time.Time // when the last response closed (zero: never)
	outgoing      []obj     // client messages the decoder needs sent
}

func newCodec() *codec {
	now := time.Now()
	return &codec{
		calls:       map[string]string{},
		cancelled:   map[string]bool{},
		input:       NewResampler(sampleRate, inputRate),
		now:         now,
		userHeardAt: now,
	}
}

// takeOutgoing returns the client messages to send now, produced while decoding.
func (c *codec) takeOutgoing() []obj {
	out := c.outgoing
	c.outgoing = nil
	return out
}

// poll emits time-based events: it ends a user turn nothing else will end (see userQuiet
// and userQuietWithVAD).
func (c *codec) poll(now time.Time) []voice.Event {
	c.now = now
	var out []voice.Event
	quiet := userQuiet
	if c.vadSeen {
		quiet = userQuietWithVAD
	}
	if c.user == userSpeaking && !c.open && now.Sub(c.userHeardAt) >= quiet {
		out = c.userStopped(out)
	}
	return out
}

func (c *codec) responseOpen() bool { return c.open }

// ---------- decode ----------

func (c *codec) decode(text string) []voice.Event { return c.decodeAt(text, time.Now()) }

// get walks nested objects.
func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

// decodeAt decodes one server message received at now.
func (c *codec) decodeAt(text string, now time.Time) []voice.Event {
	c.now = now
	v, err := parseJSON(text)
	if err != nil {
		return []voice.Event{{Kind: voice.EvError, Message: "unparseable server message"}}
	}
	msg, _ := v.(map[string]any) // valid JSON that is not an object carries nothing we know
	var out []voice.Event
	if _, has := msg["setupComplete"]; has && !c.ready {
		c.ready = true
		out = append(out, voice.Event{Kind: voice.EvReady})
	}
	switch t, _ := str(get(msg, "voiceActivity", "type")); t {
	case "ACTIVITY_START":
		c.vadSeen = true
		out = c.userStarted(out)
	case "ACTIVITY_END":
		c.vadSeen = true
		out = c.userStopped(out)
	}
	if sc, has := msg["serverContent"]; has {
		out = c.serverContent(sc, out)
	}
	if calls, ok := get(msg, "toolCall", "functionCalls").([]any); ok {
		out = c.toolCalls(calls, out)
	}
	if ids, ok := get(msg, "toolCallCancellation", "ids").([]any); ok {
		var names []string
		for _, id := range ids {
			if s, ok := id.(string); ok {
				names = append(names, s)
				delete(c.calls, s)
				c.cancelled[s] = true
			}
		}
		out = append(out, voice.Event{Kind: voice.EvError, Message: "tool call cancelled: " + strings.Join(names, ", ")})
	}
	if goAway, has := msg["goAway"]; has {
		left, ok := str(get(goAway, "timeLeft"))
		if !ok {
			left = "soon"
		}
		out = append(out, voice.Event{Kind: voice.EvError, Message: fmt.Sprintf("server will close the connection (time left: %s)", left)})
	}
	return out
}

func (c *codec) serverContent(sc any, out []voice.Event) []voice.Event {
	if get(sc, "interrupted") == true {
		if c.ownInterrupt {
			c.ownInterrupt = false // our clientContent cut the reply; not the user
		} else {
			out = c.userStarted(out)
		}
		out = c.closeResponse(out)
		c.mute = muteOff
	}
	if t, ok := str(get(sc, "inputTranscription", "text")); ok {
		late := !c.closedAt.IsZero() && c.now.Sub(c.closedAt) < lateTranscriptGrace
		// With server voice activity, ACTIVITY_START marks speech; a transcript arriving
		// after ACTIVITY_END is that turn's text, not a new turn.
		if strings.TrimSpace(t) != "" && !c.open && !late && !c.vadSeen {
			out = c.userStarted(out)
		}
		if c.user == userSpeaking {
			c.userHeardAt = c.now
		}
		c.userText.WriteString(t)
		if strings.TrimSpace(t) != "" {
			out = append(out, voice.Event{Kind: voice.EvUserHeard, Text: strings.TrimSpace(c.userText.String())})
		}
	}
	if parts, ok := get(sc, "modelTurn", "parts").([]any); ok {
		for _, part := range parts {
			if get(part, "thought") == true {
				continue
			}
			data, ok := str(get(part, "inlineData", "data"))
			if !ok {
				continue
			}
			rate := outputRate
			if mime, ok := str(get(part, "inlineData", "mimeType")); ok {
				if r, ok := rateOf(mime); ok {
					rate = r
				}
			}
			if c.mute != muteOff {
				continue
			}
			out = c.openResponse(out)
			pcm, ok := c.audio(data, rate)
			switch {
			case !ok:
				out = append(out, voice.Event{Kind: voice.EvError, Message: "bad audio chunk"})
			case len(pcm) > 0:
				out = append(out, voice.Event{Kind: voice.EvAudioDelta, Audio: pcm})
			}
		}
	}
	if t, ok := str(get(sc, "outputTranscription", "text")); ok && c.mute == muteOff {
		out = c.openResponse(out)
		c.assistantText.WriteString(t)
	}
	if get(sc, "turnComplete") == true {
		// An Inject that came too late to interrupt anything: the next interrupt is the user's.
		c.ownInterrupt = false
		out = c.closeResponse(out)
		out = c.flushUser(out)
		if c.mute == muteUntilTurnEnd {
			c.mute = muteOff
		}
	}
	return out
}

func (c *codec) toolCalls(calls []any, out []voice.Event) []voice.Event {
	if c.mute != muteOff {
		// Part of output we dropped. The model blocks until every call is answered, so
		// answer now instead of leaving it hanging.
		responses := make([]any, 0, len(calls))
		for _, call := range calls {
			responses = append(responses, obj{
				"id": get(call, "id"), "name": get(call, "name"),
				"response": obj{"error": "cancelled: the user interrupted this reply"},
			})
		}
		c.outgoing = append(c.outgoing, obj{"toolResponse": obj{"functionResponses": responses}})
		return out
	}
	out = c.openResponse(out)
	out = c.flushUser(out)
	for _, call := range calls {
		id, _ := str(get(call, "id"))
		name, _ := str(get(call, "name"))
		args := json.RawMessage("{}")
		if a := get(call, "args"); a != nil {
			args = json.RawMessage(marshal(a))
		}
		c.calls[id] = name
		out = append(out, voice.Event{Kind: voice.EvToolCall, CallID: id, ToolName: name, Args: args})
	}
	out = c.closeResponse(out)
	// Anything the model says next continues its turn.
	c.followup = true
	return out
}

func (c *codec) userStarted(out []voice.Event) []voice.Event {
	if c.mute == muteUntilSpeaks {
		c.mute = muteOff
	}
	// The next reply answers the user, whatever was pending before.
	c.followup = false
	c.userHeardAt = c.now
	if c.user != userSpeaking {
		out = c.flushUser(out)
		c.user = userSpeaking
		out = append(out, voice.Event{Kind: voice.EvSpeechStarted})
	}
	return out
}

func (c *codec) userStopped(out []voice.Event) []voice.Event {
	if c.user == userSpeaking {
		c.user = userStopped
		out = append(out, voice.Event{Kind: voice.EvSpeechStopped})
	}
	return out
}

func (c *codec) openResponse(out []voice.Event) []voice.Event {
	if c.open {
		return out
	}
	if c.followup {
		c.followup = false
	} else {
		switch c.user {
		case userSpeaking:
			out = append(out, voice.Event{Kind: voice.EvSpeechStopped})
		case userIdle:
			out = append(out, voice.Event{Kind: voice.EvSpeechStarted}, voice.Event{Kind: voice.EvSpeechStopped})
		}
	}
	c.user = userIdle
	c.open = true
	c.pendingByte = nil
	if c.output != nil {
		c.output.Reset()
	}
	return append(out, voice.Event{Kind: voice.EvResponseCreated})
}

func (c *codec) closeResponse(out []voice.Event) []voice.Event {
	if !c.open {
		return out
	}
	c.open = false
	c.closedAt = c.now
	out = c.flushUser(out)
	said := strings.TrimSpace(c.assistantText.String())
	c.assistantText.Reset()
	if said != "" {
		out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "assistant", Text: said})
	}
	return append(out, voice.Event{Kind: voice.EvResponseDone})
}

func (c *codec) flushUser(out []voice.Event) []voice.Event {
	said := strings.TrimSpace(c.userText.String())
	c.userText.Reset()
	if said != "" {
		out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "user", Text: said})
	}
	return out
}

// audio decodes one base64 PCM16 chunk at rate into 24 kHz samples.
func (c *codec) audio(b64 string, rate int) ([]int16, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	b := append(c.pendingByte, raw...)
	pcm, rest := audio.DecodeLE(b)
	c.pendingByte = append([]byte(nil), rest...)
	if rate == sampleRate {
		return pcm, true
	}
	if c.output == nil || c.output.FromRate() != rate {
		c.output = NewResampler(rate, sampleRate)
	}
	return c.output.Process(pcm), true
}

// ---------- encode ----------

func (c *codec) encode(cmd voice.Command) encoded {
	var enc encoded
	switch cmd.Kind {
	case voice.CmdAudio:
		if pcm := c.input.Process(cmd.Audio); len(pcm) > 0 {
			enc.messages = append(enc.messages, obj{
				"realtimeInput": obj{"audio": obj{"data": pcmToBase64(pcm), "mimeType": inputMIME}},
			})
		}
	case voice.CmdCancelResponse:
		if c.open {
			enc.events = c.closeResponse(enc.events)
			c.mute = muteUntilTurnEnd
		}
	case voice.CmdToolResult:
		if c.cancelled[cmd.CallID] {
			delete(c.cancelled, cmd.CallID)
			return enc // the server already dropped this call
		}
		name := c.calls[cmd.CallID]
		delete(c.calls, cmd.CallID)
		output := cmd.Output
		if len(output) == 0 {
			output = json.RawMessage("null")
		}
		enc.messages = append(enc.messages, obj{"toolResponse": obj{"functionResponses": []any{
			obj{"id": cmd.CallID, "name": name, "response": obj{"output": output}},
		}}})
		if cmd.Respond {
			c.followup = true
		} else {
			c.mute = muteUntilSpeaks
		}
	case voice.CmdInject:
		if c.open {
			c.ownInterrupt = true
		}
		if cmd.Respond {
			c.followup = true
		}
		enc.messages = append(enc.messages, obj{"clientContent": obj{
			"turns":        []any{obj{"role": "user", "parts": []any{obj{"text": cmd.Text}}}},
			"turnComplete": cmd.Respond,
		}})
	case voice.CmdClose:
	}
	return enc
}
