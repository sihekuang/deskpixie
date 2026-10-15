package openailive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Turn timing (spec §3.1). GPT-Live sends no turn events, so the codec derives them.
const (
	// assistantQuiet: output quiet this long (and no backend work outstanding) ends Pixie's reply.
	assistantQuiet = 700 * time.Millisecond
	// userQuiet: no input transcript for this long ends the user's turn (transcripts lag audio).
	userQuiet = 800 * time.Millisecond
	// outputSpeechRMS: output louder than this is speech; quieter frames are GPT-Live idling.
	outputSpeechRMS = 0.01
	// delegationStale: backend work with no news for this long stops holding a reply open.
	delegationStale = 30 * time.Second
	// maxContent: session.*.append content is capped at 500 tokens; ~3 characters a token.
	maxContent = 1500
)

// codec turns GPT-Live's duplex stream into the per-turn events voice.Conversation expects,
// and commands into client events. Pure apart from logging: time comes in as now. Not safe
// for concurrent use; the session guards it.
type codec struct {
	ready bool
	seq   int // client event ids: c_1, c_2, …

	pendingByte []byte // odd trailing byte of the previous output delta

	// Pixie's reply (the assistant span).
	aOpen     bool
	aLastLoud time.Time
	aEndMs    int64 // end_ms of the last forwarded delta; -1 = none
	aText     strings.Builder
	// muted: a cancelled reply's tail is dropped until output has had no speech for
	// assistantQuiet, or the user talks (what GPT-Live says next answers them).
	muted     bool
	mutedLoud time.Time

	// The user's turn (the user span).
	uOpen     bool
	uDeferred bool // opened while Pixie talked: SpeechStarted waits for Pixie to stop
	uLast     time.Time
	uText     strings.Builder

	// Backend (Responses delegation) work in flight; it holds Pixie's reply open so tool calls
	// come before their ResponseDone.
	awaiting    int // delegations created whose backend response hasn't started
	backendOpen int // backend responses started and not finished
	// pendingCalls: tool calls not yet answered; response.create waits for all of them.
	pendingCalls map[string]bool
	backendNews  time.Time

	usageSeconds float64

	// Client delegation (#70): delegations handed to the app, by id, until it answers them;
	// the goodbye calls the codec made up (their results go nowhere); the minute of the last
	// time context sent.
	client      bool
	clientCalls map[string]bool
	byeCalls    map[string]bool
	byeSeq      int
	timeSent    string
}

func newCodec() *codec {
	return &codec{aEndMs: -1, pendingCalls: map[string]bool{}, clientCalls: map[string]bool{}, byeCalls: map[string]bool{}}
}

func (c *codec) nextID() string {
	c.seq++
	return fmt.Sprintf("c_%d", c.seq)
}

func (c *codec) outstanding(now time.Time) bool {
	busy := c.awaiting > 0 || c.backendOpen > 0
	if busy && now.Sub(c.backendNews) >= delegationStale {
		c.awaiting, c.backendOpen = 0, 0
		return false
	}
	return busy
}

func (c *codec) deltaToPCM(b64 string) ([]int16, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	b := append(append([]byte{}, c.pendingByte...), raw...)
	pcm, rest := audio.DecodeLE(b)
	c.pendingByte = append([]byte{}, rest...)
	return pcm, true
}

// closeUser ends the user's turn: their words, then SpeechStopped.
func (c *codec) closeUser(out []voice.Event) []voice.Event {
	c.uOpen, c.uDeferred = false, false
	t := strings.TrimSpace(c.uText.String())
	if t != "" {
		out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "user", Text: t})
	}
	c.uText.Reset()
	out = append(out, voice.Event{Kind: voice.EvSpeechStopped})
	if c.client && voice.IsGoodbye(t) {
		// No typed end_conversation with client delegation: a goodbye ends the conversation
		// once Pixie's reply to it has played (#70).
		c.byeSeq++
		id := fmt.Sprintf("bye_%d", c.byeSeq)
		c.byeCalls[id] = true
		out = append(out, voice.Event{Kind: voice.EvToolCall, CallID: id, ToolName: voice.EndConversationTool, Args: json.RawMessage(`{}`)})
	}
	return out
}

// onClientDelegation: GPT-Live handed this turn to the app (no task text). Its decision means
// the user's turn is over, so their words go out first; then the call, which the conversation
// fills in from the transcript.
func (c *codec) onClientDelegation(id string) []voice.Event {
	var out []voice.Event
	if c.uOpen {
		if c.uDeferred {
			// Said over Pixie: the words count, but no turn starts.
			c.uOpen, c.uDeferred = false, false
			if t := strings.TrimSpace(c.uText.String()); t != "" {
				out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "user", Text: t})
			}
			c.uText.Reset()
		} else {
			out = c.closeUser(out)
		}
	}
	if id == "" {
		return out
	}
	c.clientCalls[id] = true
	return append(out, voice.Event{Kind: voice.EvToolCall, CallID: id, ToolName: voice.ClientDelegationTool, Args: json.RawMessage(`{}`)})
}

// closeAssistant ends Pixie's reply: its words, then ResponseDone; a user turn that was waiting
// for Pixie to stop starts now.
func (c *codec) closeAssistant(out []voice.Event) []voice.Event {
	c.aOpen = false
	c.aEndMs = -1
	if t := strings.TrimSpace(c.aText.String()); t != "" {
		out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "assistant", Text: t})
	}
	c.aText.Reset()
	out = append(out, voice.Event{Kind: voice.EvResponseDone})
	if c.uDeferred {
		c.uDeferred = false
		out = append(out, voice.Event{Kind: voice.EvSpeechStarted})
	}
	return out
}

// decode handles one server message received at now.
func (c *codec) decode(data []byte, now time.Time) []voice.Event {
	var m serverMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return []voice.Event{{Kind: voice.EvError, Message: "unparseable server message"}}
	}
	switch m.Type {
	case "session.started":
		if c.ready {
			return nil
		}
		c.ready = true
		return []voice.Event{{Kind: voice.EvReady}}
	case "session.output_audio.delta":
		return c.onOutputAudio(m, now)
	case "session.output_transcript.delta":
		c.aText.WriteString(m.Delta)
	case "session.input_transcript.delta":
		if strings.TrimSpace(m.Delta) == "" && !c.uOpen {
			return nil
		}
		c.uText.WriteString(m.Delta)
		c.uLast = now
		c.muted = false
		if c.uOpen {
			return nil
		}
		c.uOpen = true
		if c.aOpen {
			c.uDeferred = true // GPT-Live decides whether this interrupts it
			return nil
		}
		return []voice.Event{{Kind: voice.EvSpeechStarted}}
	case "session.delegation.created":
		switch {
		case m.Delegation.Target == "responses":
			c.awaiting++
			c.backendNews = now
		case m.Delegation.Target == "client" && c.client:
			return c.onClientDelegation(m.Delegation.ID)
		}
	case "response.event":
		return c.onBackend(m, now)
	case "session.usage.updated":
		c.usageSeconds = m.Usage.Seconds
	case "session.closed":
		if m.Usage.Seconds > 0 {
			c.usageSeconds = m.Usage.Seconds
		}
		log.Printf("[voice] gpt-live session closed: %s, %.0f s billed", m.Reason, c.usageSeconds)
		if f, bad := closedFailure(m.Reason); bad {
			return []voice.Event{{Kind: voice.EvError, Fatal: true, Message: "gpt-live: session closed: " + m.Reason, Failure: &f}}
		}
	case "error":
		if m.Error.Code == "responses_validation_failed" {
			f := voice.Failure{Kind: voice.FailModelError, Msg: "GPT-Live backend settings rejected"}
			return []voice.Event{{Kind: voice.EvError, Fatal: true, Message: m.Error.String(), Failure: &f}}
		}
		return []voice.Event{{Kind: voice.EvError, Message: m.Error.String()}}
	}
	return nil
}

func (c *codec) onOutputAudio(m serverMsg, now time.Time) []voice.Event {
	pcm, ok := c.deltaToPCM(m.Delta)
	if !ok {
		return []voice.Event{{Kind: voice.EvError, Message: "bad audio delta"}}
	}
	loud := audio.RMS(pcm) >= outputSpeechRMS
	if c.muted {
		if loud {
			c.mutedLoud = now
		}
		return nil
	}
	var out []voice.Event
	if !c.aOpen {
		if !loud {
			return nil // GPT-Live idling: not Pixie talking
		}
		if c.uOpen && !c.uDeferred {
			out = c.closeUser(out)
		}
		c.aOpen = true
		out = append(out, voice.Event{Kind: voice.EvResponseCreated})
	} else if m.StartMs != nil && c.aEndMs >= 0 && *m.StartMs > c.aEndMs {
		// Omitted silence inside the reply keeps its length (bounded).
		gap := min(time.Duration(*m.StartMs-c.aEndMs)*time.Millisecond, assistantQuiet)
		out = append(out, voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, int(gap.Milliseconds())*audio.SampleRate/1000)})
	}
	if loud {
		c.aLastLoud = now
	}
	if m.EndMs != nil {
		c.aEndMs = *m.EndMs
	}
	if len(pcm) > 0 {
		out = append(out, voice.Event{Kind: voice.EvAudioDelta, Audio: pcm})
	}
	return out
}

func (c *codec) onBackend(m serverMsg, now time.Time) []voice.Event {
	c.backendNews = now
	switch e := m.Event; e.Type {
	case "response.created":
		if c.awaiting > 0 {
			c.awaiting--
		}
		c.backendOpen++
	case "response.output_item.done":
		if e.Item.Type != "function_call" || e.Item.CallID == "" {
			return nil
		}
		c.pendingCalls[e.Item.CallID] = true
		args := json.RawMessage(e.Item.Arguments)
		if !json.Valid(args) {
			args, _ = json.Marshal(e.Item.Arguments)
		}
		return []voice.Event{{Kind: voice.EvToolCall, CallID: e.Item.CallID, ToolName: e.Item.Name, Args: args}}
	case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
		if c.backendOpen > 0 {
			c.backendOpen--
		}
		if e.Type == "response.failed" {
			msg := "backend response failed"
			if e.Response.Error != nil {
				msg += ": " + e.Response.Error.Code + " " + e.Response.Error.Message
			}
			return []voice.Event{{Kind: voice.EvError, Message: msg}}
		}
	}
	return nil
}

// poll closes the spans whose quiet time has passed.
func (c *codec) poll(now time.Time) []voice.Event {
	var out []voice.Event
	if c.muted && now.Sub(c.mutedLoud) >= assistantQuiet {
		c.muted = false
	}
	if c.uOpen && now.Sub(c.uLast) >= userQuiet {
		if c.uDeferred {
			// Pixie talked through it (a backchannel or noise): no turn, but keep the words.
			c.uOpen, c.uDeferred = false, false
			if t := strings.TrimSpace(c.uText.String()); t != "" {
				out = append(out, voice.Event{Kind: voice.EvTranscript, Role: "user", Text: t})
			}
			c.uText.Reset()
		} else {
			out = c.closeUser(out)
		}
	}
	if c.aOpen && now.Sub(c.aLastLoud) >= assistantQuiet && !c.outstanding(now) {
		out = c.closeAssistant(out)
	}
	return out
}

// encode maps a command to client events, plus any events it causes locally.
func (c *codec) encode(cmd voice.Command, now time.Time) ([]obj, []voice.Event) {
	switch cmd.Kind {
	case voice.CmdAudio:
		return []obj{{"type": "session.input_audio.append", "event_id": c.nextID(), "audio": pcmToBase64(cmd.Audio)}}, nil
	case voice.CmdCancelResponse:
		// No truncate on GPT-Live: end the reply locally and drop its tail until it goes quiet.
		if !c.aOpen {
			return nil, nil // already quiet: nothing to drop
		}
		c.muted, c.mutedLoud = true, now
		return nil, c.closeAssistant(nil)
	case voice.CmdToolResult:
		if c.byeCalls[cmd.CallID] {
			delete(c.byeCalls, cmd.CallID)
			return nil, nil // made up here: nobody waits for it
		}
		if c.clientCalls[cmd.CallID] {
			delete(c.clientCalls, cmd.CallID)
			return c.clientReply(cmd.CallID, cmd.Output), nil
		}
		delete(c.pendingCalls, cmd.CallID)
		c.backendNews = now
		out := []obj{{"type": "response.item.create", "event_id": c.nextID(),
			"item": obj{"type": "function_call_output", "call_id": cmd.CallID, "output": string(cmd.Output)}}}
		if cmd.Respond && len(c.pendingCalls) == 0 {
			out = append(out, obj{"type": "response.create", "event_id": c.nextID()})
		}
		return out, nil
	case voice.CmdInject:
		return c.inject(cmd.Text, cmd.Respond), nil
	}
	return nil, nil
}

// inject: a notice for Pixie to say (commentary, paraphrased) or just know (thinking). A confirm
// prompt must be read word for word, so it goes in as an instruction.
func (c *codec) inject(text string, respond bool) []obj {
	head, rest := splitContent(text, maxContent)
	kind := "session.thinking.append"
	switch {
	case strings.Contains(head, "Say confirm"):
		kind = "session.instructions.append"
		head = "Ask the user this now, reading any confirm phrase exactly as written: " + head
	case respond:
		kind = "session.commentary.append"
	}
	out := []obj{{"type": kind, "event_id": c.nextID(), "delegation_id": nil, "content": head}}
	for rest != "" {
		var part string
		part, rest = splitContent(rest, maxContent)
		out = append(out, obj{"type": "session.thinking.append", "event_id": c.nextID(), "delegation_id": nil, "content": part})
	}
	return out
}

// splitContent cuts text to at most n bytes, at the last sentence end (or space) before n,
// never inside a UTF-8 character.
func splitContent(text string, n int) (head, rest string) {
	text = strings.TrimSpace(text)
	if len(text) <= n {
		return text, ""
	}
	cut := strings.LastIndexAny(text[:n], ".!?")
	if cut < n/2 {
		cut = strings.LastIndex(text[:n], " ")
	}
	if cut <= 0 {
		cut = n - 1
		for cut > 0 && !utf8.RuneStart(text[cut+1]) {
			cut--
		}
	}
	return strings.TrimSpace(text[:cut+1]), strings.TrimSpace(text[cut+1:])
}

// clientReply answers a client delegation with what became of it (voice.ClientDelegationResult):
// commentary when Pixie should say something now, thinking when it is only context.
func (c *codec) clientReply(id string, output []byte) []obj {
	var r voice.ClientDelegationResult
	_ = json.Unmarshal(output, &r)
	speak, text := true, ""
	switch {
	case r.Routed == "delegate" && r.Check:
		speak = false
		text = "The app gave this question to the coordinator (" + r.JobID + "). Its answer arrives later as a \"Coordinator reply:\" line; until then don't answer it yourself. If you haven't yet, say only that you're checking."
	case r.Routed == "delegate":
		text = "The app handed this to the coordinator as " + r.JobID + ". Tell the user in a few words that you've passed it on and will let them know; don't answer it yourself."
	case r.Routed == "answer_job":
		text = "The app passed the user's answer to the coordinator (" + r.JobID + "). Say in a few words that you've passed it on."
	case r.Routed == "none":
		speak = false
		text = "Nothing was sent to the coordinator: " + r.Note + ". Say nothing about it."
	case strings.HasPrefix(r.Error, "Delegation is off ("):
		text = "This can't be handed off: " + strings.TrimSuffix(r.Error, ".") + ". Tell the user in one short sentence that you can't hand it off and why."
	case r.Error != "":
		text = "Handing this off failed: " + r.Error + ". Tell the user in one short sentence."
	default:
		speak = false
		text = "The app has this in hand; don't hand it off again: " + string(output)
	}
	kind := "session.thinking.append"
	if speak {
		kind = "session.commentary.append"
	}
	head, _ := splitContent(text, maxContent)
	return []obj{{"type": kind, "event_id": c.nextID(), "delegation_id": id, "content": head}}
}

// timeContext is the current local time as quiet context, once a minute (client delegation
// only: without get_time, GPT-Live answers the time from it; #70).
func (c *codec) timeContext(now time.Time) []obj {
	if !c.client {
		return nil
	}
	key := now.Format("2006-01-02 15:04")
	if key == c.timeSent {
		return nil
	}
	c.timeSent = key
	zone, _ := now.Zone()
	text := fmt.Sprintf("Local time now: %s (%s, UTC%s).", now.Format("Monday, 2 January 2006, 3:04 PM"),
		zone, now.Format("-07:00"))
	return []obj{{"type": "session.thinking.append", "event_id": c.nextID(), "delegation_id": nil, "content": text}}
}
