package openailive

// Client delegation (#70): voice.liveDelegation "agent".

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/voice"
)

func clientCodec() *codec {
	c := newCodec()
	c.client = true
	c.ready = true
	return c
}

func delegationCreated(id string) []byte {
	return msg(map[string]any{"type": "session.delegation.created",
		"delegation": map[string]any{"id": id, "type": "delegation", "target": "client"}, "offset_ms": 3800})
}

func TestClientSessionStartHasNoBackend(t *testing.T) {
	cfg := voice.SessionConfig{Instructions: "be Bit", ClientDelegation: true, BackendModel: "gpt-6-sol",
		Tools: []voice.ToolSpec{{Name: "get_time"}}}
	var got map[string]any
	_ = json.Unmarshal(msg(sessionStart(cfg, "c_1")), &got)
	s := got["session"].(map[string]any)
	d := s["delegation"].(map[string]any)
	if len(d) != 1 || d["type"] != "client" {
		t.Fatalf("delegation %v", d)
	}
	ins := s["instructions"].(string)
	if !strings.HasPrefix(ins, "be Bit") || !strings.Contains(ins, "you have no tools of your own") ||
		strings.Contains(ins, "runs through your backend") {
		t.Fatalf("instructions %q", ins)
	}
	for k := range s {
		switch k {
		case "model", "instructions", "audio", "delegation", "store":
		default:
			t.Fatalf("unexpected session field %q", k)
		}
	}
}

// GPT-Live's delegation means the user's turn is over: their words go out at once (the
// conversation builds the task from them), then the call.
func TestClientDelegationClosesTheUserTurnThenCalls(t *testing.T) {
	c := clientCodec()
	wantKinds(t, c.decode(inText("can you check why my build"), at(0)), voice.EvSpeechStarted)
	c.decode(inText(" failed"), at(300))
	evs := c.decode(delegationCreated("item_1"), at(300))
	wantKinds(t, evs, voice.EvTranscript, voice.EvSpeechStopped, voice.EvToolCall)
	if evs[0].Text != "can you check why my build failed" {
		t.Fatalf("transcript %q", evs[0].Text)
	}
	if evs[2].CallID != "item_1" || evs[2].ToolName != voice.ClientDelegationTool {
		t.Fatalf("call %+v", evs[2])
	}
	if got := c.poll(at(2000)); len(got) != 0 {
		t.Fatalf("the user turn closed twice: %v", kinds(got))
	}
}

// Backend mode ignores a client delegation (it can't happen there).
func TestClientDelegationIgnoredInBackendMode(t *testing.T) {
	c := newCodec()
	c.ready = true
	if evs := c.decode(delegationCreated("item_1"), at(0)); len(evs) != 0 {
		t.Fatalf("%v", kinds(evs))
	}
}

func clientResult(t *testing.T, c *codec, id string, r voice.ClientDelegationResult) map[string]any {
	t.Helper()
	out, _ := json.Marshal(r)
	msgs, evs := c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: id, Output: out, Respond: true}, at(0))
	if len(msgs) != 1 || len(evs) != 0 {
		t.Fatalf("msgs %v evs %v", msgs, evs)
	}
	return msgs[0]
}

func TestClientDelegationReplies(t *testing.T) {
	cases := []struct {
		name string
		r    voice.ClientDelegationResult
		kind string
		has  string
	}{
		{"work", voice.ClientDelegationResult{Routed: "delegate", JobID: "job_1"}, "session.commentary.append", "handed this to the coordinator as job_1"},
		{"check", voice.ClientDelegationResult{Routed: "delegate", JobID: "job_2", Check: true}, "session.thinking.append", "Coordinator reply:"},
		{"answer", voice.ClientDelegationResult{Routed: "answer_job", JobID: "job_3"}, "session.commentary.append", "passed the user's answer"},
		{"answer-session", voice.ClientDelegationResult{Routed: "answer_job", Note: "the session that asked"}, "session.commentary.append", "passed the user's answer to the session that asked"},
		{"confirm", voice.ClientDelegationResult{Routed: "none", Note: "a confirm word"}, "session.thinking.append", "Say nothing"},
		{"off", voice.ClientDelegationResult{Error: "Delegation is off (Coordinator: herdr not found)."}, "session.commentary.append", "can't be handed off: Delegation is off (Coordinator: herdr not found)"},
		{"failed", voice.ClientDelegationResult{Error: "task is required"}, "session.commentary.append", "failed: task is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := clientCodec()
			c.decode(delegationCreated("item_9"), at(0))
			m := clientResult(t, c, "item_9", tc.r)
			if m["type"] != tc.kind || m["delegation_id"] != "item_9" || !strings.Contains(m["content"].(string), tc.has) {
				t.Fatalf("%v", m)
			}
			// Answered once: a second result for it is an ordinary (backend) tool result.
			if c.clientCalls["item_9"] {
				t.Fatal("still pending")
			}
		})
	}
}

func TestGoodbyeEndsTheConversationInClientMode(t *testing.T) {
	c := clientCodec()
	c.decode(inText("thanks, that's all. Bye!"), at(0))
	evs := c.poll(at(1000))
	wantKinds(t, evs, voice.EvTranscript, voice.EvSpeechStopped, voice.EvToolCall)
	call := evs[2]
	if call.ToolName != voice.EndConversationTool {
		t.Fatalf("%+v", call)
	}
	// Its result goes nowhere: nobody on the wire waits for it.
	msgs, _ := c.encode(voice.Command{Kind: voice.CmdToolResult, CallID: call.CallID, Output: json.RawMessage(`{"ok":true}`)}, at(1100))
	if len(msgs) != 0 {
		t.Fatalf("%v", msgs)
	}
	// Not a goodbye, and backend mode never makes one up.
	c.decode(inText("bye the way, what's next?"), at(2000))
	wantKinds(t, c.poll(at(3000)), voice.EvTranscript, voice.EvSpeechStopped)
	b := newCodec()
	b.ready = true
	b.decode(inText("bye"), at(0))
	wantKinds(t, b.poll(at(1000)), voice.EvTranscript, voice.EvSpeechStopped)
}

// #93: a goodbye said while Pixie is still talking starts no turn, but still ends the
// conversation (once Pixie's reply has played).
func TestGoodbyeSaidOverPixieEndsTheConversation(t *testing.T) {
	for _, viaDelegation := range []bool{false, true} {
		c := clientCodec()
		c.decode(outDelta(tone(2400, 8000), 0), at(0))
		c.decode(inText("thanks, bye"), at(100))
		var evs []voice.Event
		if viaDelegation {
			evs = c.decode(delegationCreated(""), at(200))
		} else {
			c.decode(outDelta(tone(2400, 8000), 100), at(800))
			evs = c.poll(at(1000))
		}
		wantKinds(t, evs, voice.EvTranscript, voice.EvToolCall)
		if evs[1].ToolName != voice.EndConversationTool {
			t.Fatalf("delegation=%v: %+v", viaDelegation, evs[1])
		}
	}
	// Not a goodbye: the words are kept, nothing ends.
	c := clientCodec()
	c.decode(outDelta(tone(2400, 8000), 0), at(0))
	c.decode(inText("mm-hm"), at(100))
	c.decode(outDelta(tone(2400, 8000), 100), at(800))
	wantKinds(t, c.poll(at(1000)), voice.EvTranscript)
}

func TestTimeContextOncePerMinuteInClientMode(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	now := time.Date(2026, 10, 14, 21, 16, 5, 0, loc)
	c := clientCodec()
	first := c.timeContext(now)
	if len(first) != 1 || first[0]["type"] != "session.thinking.append" || first[0]["delegation_id"] != nil {
		t.Fatalf("%v", first)
	}
	if got := first[0]["content"]; got != "Local time now: Wednesday, 14 October 2026, 9:16 PM (PDT, UTC-07:00)." {
		t.Fatalf("%q", got)
	}
	if again := c.timeContext(now.Add(30 * time.Second)); len(again) != 0 {
		t.Fatalf("same minute sent twice: %v", again)
	}
	if next := c.timeContext(now.Add(time.Minute)); len(next) != 1 {
		t.Fatal("next minute not sent")
	}
	if b := newCodec(); len(b.timeContext(now)) != 0 {
		t.Fatal("backend mode sends time context")
	}
}

func TestSplitContentNeverCutsACharacter(t *testing.T) {
	text := strings.Repeat("é", 1000) // 2000 bytes, no spaces or sentence ends
	head, rest := splitContent(text, maxContent)
	if len(head) > maxContent || !utf8.ValidString(head) || !utf8.ValidString(rest) || head+rest != text {
		t.Fatalf("head %d bytes valid=%v rest valid=%v", len(head), utf8.ValidString(head), utf8.ValidString(rest))
	}
}
