package agent_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/voice"
)

// speakingVoice is the voice loop's own conversation, open with Pixie in the middle of a
// reply, so notices wait for her to finish as they do in the app.
type speakingVoice struct {
	mu        sync.Mutex
	c         *voice.Conversation
	now       time.Time
	said      []string
	withdrawn int
}

func newSpeakingVoice() *speakingVoice {
	v := &speakingVoice{c: voice.NewConversation(voice.ConversationConfig{IdleTimeout: time.Hour}), now: time.Unix(1_800_000_000, 0)}
	v.step(voice.HotkeyInput())
	v.model(voice.EvReady)
	v.step(voice.MicLiveInput())
	v.model(voice.EvSpeechStarted)
	v.model(voice.EvSpeechStopped)
	v.model(voice.EvResponseCreated)
	v.step(voice.ModelInput(voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, 2400)}))
	return v
}

func (v *speakingVoice) step(in voice.Input) {
	v.now = v.now.Add(10 * time.Millisecond)
	for _, e := range v.c.Step(in, v.now) {
		if e.Kind == voice.EfInject {
			v.said = append(v.said, e.Text)
		}
	}
}

func (v *speakingVoice) model(k voice.EventKind) { v.step(voice.ModelInput(voice.Event{Kind: k})) }

func (v *speakingVoice) IsOpen() bool { return true }
func (v *speakingVoice) Open() bool   { return false }
func (v *speakingVoice) Inject(n Notice) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.step(voice.InjectInput(n))
}
func (v *speakingVoice) HoldOpen(on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.step(voice.HoldOpenInput(on))
}
func (v *speakingVoice) WithdrawHeld(session string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.withdrawn++
	v.step(voice.WithdrawHeldInput(session))
}

func (v *speakingVoice) withdrawals() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.withdrawn
}

// finish: Pixie finishes her reply, then says (and answers) every notice still waiting.
func (v *speakingVoice) finish() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	for range 10 {
		v.model(voice.EvResponseDone)
		v.step(voice.TickInput(true))
		v.model(voice.EvResponseCreated)
	}
	return v.said
}

// #339 end to end: a message held for a busy session goes in with the session's next step; the
// notice saying it still waits, queued behind Pixie's speech, is never said, nothing asks for
// it to be stopped and sent, and a stop-and-send after that sends nothing again.
func TestAHeldMessageThatWentInIsNotSaidToWait(t *testing.T) {
	p := startPixSession(t)
	v := newSpeakingVoice()
	h, _ := pixRuntime(t, p, config(false), v)
	h.Voice(VoiceSignal{Kind: voice.SigOpened})
	delegate(t, &rtRig{h: h}, "ask the bug fixing session how it's going")
	settle()

	p.step("work") // busy on a prompt of yours
	const msg = "Status check: how is it going?"
	if r := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: msg}); r["status"] != "waiting" || r["reason"] != "working" {
		t.Fatalf("message: %v", r)
	}
	p.step("tool") // its next step takes it
	if ctx := p.read("context"); !strings.Contains(ctx, msg) {
		t.Fatalf("the session's tool didn't get it: %q", ctx)
	}
	p.waitFor("the waiting notice withdrawn", func() bool { return v.withdrawals() > 0 })
	for _, s := range v.finish() {
		if strings.Contains(s, "hasn't got your message yet") || strings.Contains(s, "stopped and sent now") {
			t.Fatalf("said after it went in: %q", s)
		}
	}
	for _, u := range h.Updates() {
		if strings.Contains(u.Text, "hasn't got your message yet") {
			t.Fatalf("still in the bubble: %+v", u)
		}
	}
	// Asked to stop and send anyway: nothing waits, so nothing is stopped or typed again.
	if r := sessionCall(t, h, McpCall{Kind: CallStopAndSendSession, Name: p.addr}); r["status"] != "sent" {
		t.Fatalf("stop and send: %v", r)
	}
	time.Sleep(300 * time.Millisecond)
	if typed := p.read("typed"); strings.Contains(typed, msg) || strings.Contains(typed, "\x1b") || len(p.prompts()) != 0 {
		t.Fatalf("sent again: typed %q, prompts %q", typed, p.prompts())
	}
}

// #336 end to end: a subagent's tool doesn't take the message; stop-and-send then stops the
// turn and types it, as one prompt the session itself gets.
func TestASubagentsToolLeavesTheMessageForStopAndSend(t *testing.T) {
	p := startPixSession(t)
	h, _ := pixRuntime(t, p, config(false), nil)
	p.step("work")
	const msg = "Can you see what can be parallelized?"
	if r := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: msg}); r["status"] != "waiting" {
		t.Fatalf("message: %v", r)
	}
	p.step("subtool")
	if ctx := p.read("context"); strings.Contains(ctx, msg) {
		t.Fatalf("a subagent got it: %q", ctx)
	}
	if r := sessionCall(t, h, McpCall{Kind: CallStopAndSendSession, Name: p.addr}); r["status"] != "sent" {
		t.Fatalf("stop and send: %v", r)
	}
	p.waitFor("typed into the session", func() bool { return len(p.prompts()) == 1 })
	if got := p.prompts()[0]; !strings.HasSuffix(got, msg) || !strings.Contains(p.read("typed"), "\x1b") {
		t.Fatalf("prompt %q, typed %q", got, p.read("typed"))
	}
}
