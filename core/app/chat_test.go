package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
)

// #221: the text box's calls, events and the text-mode hotkey.

// ChatSend answers the question waiting (asking: its job; askingSession: the session whose
// question the box shows), as the real one does, else starts a new numbered job.
func (f *fakeAgent) ChatSend(text string) (ChatSent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatErr != nil {
		return ChatSent{}, f.chatErr
	}
	f.sent = append(f.sent, text)
	if f.asking != "" {
		s := ChatSent{Job: f.asking, Answered: true, Session: f.askingSession, Attached: f.takeAttached()}
		f.asking, f.askingSession = "", ""
		return s, nil
	}
	f.jobs++
	return ChatSent{Job: fmt.Sprintf("job_%012d", f.jobs), Attached: f.takeAttached()}, nil
}

// ChatDelegate always starts a new job.
func (f *fakeAgent) ChatDelegate(text string) (ChatSent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatErr != nil {
		return ChatSent{}, f.chatErr
	}
	f.sent = append(f.sent, text)
	f.delegated++
	f.jobs++
	return ChatSent{Job: fmt.Sprintf("job_%012d", f.jobs), Attached: f.takeAttached()}, nil
}

// takeAttached hands over the waiting attachments, as the real agent does when a message goes.
func (f *fakeAgent) takeAttached() []string {
	if f.attach == nil {
		return nil
	}
	return f.attach.Take()
}
func (f *fakeAgent) ChatStop() { f.mu.Lock(); f.stops++; f.mu.Unlock() }

// Interrupt records the name; interruptErr refuses it.
func (f *fakeAgent) Interrupt(name, surface string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.interruptErr != nil {
		return f.interruptErr
	}
	f.interrupted = append(f.interrupted, name+"@"+surface)
	return nil
}
func (f *fakeAgent) SetTextMode(on bool) {
	f.mu.Lock()
	f.textModes = append(f.textModes, on)
	f.mu.Unlock()
}
func (f *fakeAgent) WindowSend(text string) (ChatSent, error) {
	f.mu.Lock()
	f.fromWindow = append(f.fromWindow, text)
	f.mu.Unlock()
	return f.ChatSend(text)
}
func (f *fakeAgent) SetMarkForced(on bool) {
	f.mu.Lock()
	f.markForced = append(f.markForced, on)
	f.mu.Unlock()
}

func TestChatSendCallsTheAgent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	out := h.call("chat.send", map[string]string{"text": "hello"})
	if !strings.Contains(string(out), `"job":"job_000000000001"`) {
		t.Fatal(string(out))
	}
	f.chatErr = errors.New("Delegation is off (Coordinator: herdr not found).")
	_, err := h.callErr("chat.send", map[string]string{"text": "again"})
	if err == nil || err.Error() != "Delegation is off (Coordinator: herdr not found)." {
		t.Fatal(err)
	}
	if code := codeOf(err); code != "" {
		t.Fatalf("a failure, not a refusal: code %q", code)
	}
	if !slices.Equal(f.sent, []string{"hello"}) {
		t.Fatal(f.sent)
	}
}

// #414: a message from the chat window goes as the window's, one from the box as the box's.
func TestAWindowMessageGoesAsTheWindows(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.call("chat.send", map[string]any{"text": "from the window", "from": "window"})
	h.call("chat.send", map[string]any{"text": "from the box"})
	if !slices.Equal(f.fromWindow, []string{"from the window"}) || !slices.Equal(f.sent, []string{"from the window", "from the box"}) {
		t.Fatal(f.fromWindow, f.sent)
	}
}

func TestChatSendWithoutAnAgentSaysTheCoordinatorIsOff(t *testing.T) {
	h := newHarness(t)
	_, err := h.callErr("chat.send", map[string]any{"text": "hello", "from": "window"})
	if err == nil || err.Error() != "The coordinator is off." {
		t.Fatal(err)
	}
	if code := codeOf(err); code != "" {
		t.Fatalf("not a refusal: code %q", code)
	}
	h.call("chat.stop", nil) // no agent: a no-op
}

func TestChatStopCallsTheAgent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.call("chat.stop", nil)
	if f.stops != 1 {
		t.Fatal(f.stops)
	}
}

func TestChatModeReportsTheSettingAndTheAgentStartsInIt(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f), withSettings(func(c *settings.Config) { c.ReplyMode = settings.ReplyText }))
	if out := h.call("chat.mode", nil); string(out) != `{"alwaysShowCaption":false,"captionDismissSecs":120,"captionFadeSecs":4,"mode":"text","replyHistoryMins":5}` {
		t.Fatal(string(out))
	}
	if !slices.Equal(f.textModes, []bool{true}) {
		t.Fatal(f.textModes)
	}
}

func TestTheHotkeyFocusesTheBoxInTextMode(t *testing.T) {
	h := newHarness(t, withSettings(func(c *settings.Config) { c.ReplyMode = settings.ReplyText }))
	h.take()
	h.app.Hotkey()
	h.waitFor("chat.focus", nil, time.Second)
	time.Sleep(50 * time.Millisecond)
	for _, e := range h.take() {
		if e["kind"] == "status" || e["kind"] == "state" {
			t.Fatal("the voice loop was started:", e)
		}
	}
}

func TestTheHotkeyIsUnchangedInVoiceMode(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.app.Hotkey()
	h.waitFor("status", nil, 3*time.Second)
	if len(h.kinds(h.take(), "chat.focus")) != 0 {
		t.Fatal("focused the box in voice mode")
	}
}

func TestSwitchingReplyModeTellsTheAgentAndTheShell(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	if s := h.set("replyMode", "text"); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	h.waitFor("replyMode", func(e event) bool { return e["mode"] == "text" }, time.Second)
	if !slices.Equal(f.textModes, []bool{false, true}) {
		t.Fatal(f.textModes)
	}
	if out := h.call("chat.mode", nil); string(out) != `{"alwaysShowCaption":false,"captionDismissSecs":120,"captionFadeSecs":4,"mode":"text","replyHistoryMins":5}` {
		t.Fatal(string(out))
	}
}

func TestAChatLineHookEmitsAnEvent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	f.hooks.Chat(map[string]string{"job": "job_000000000001", "status": "done", "text": "Two are running."})
	h.waitFor("chat.line", func(e event) bool { return e["text"] == "Two are running." && e["status"] == "done" }, time.Second)
}

// #224: the always-show switch reaches the shell; it never touches the agent or the mode.
func TestAlwaysShowCaptionTellsTheShell(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	if s := h.set("alwaysShowCaption", true); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	h.waitFor("captions", func(e event) bool {
		return e["always"] == true && e["dismissSecs"] == float64(120) && e["fadeSecs"] == float64(4) && e["historyMins"] == float64(5)
	}, time.Second)
	if len(f.textModes) != 1 {
		t.Fatal("the agent was told again:", f.textModes)
	}
	if out := h.call("chat.mode", nil); string(out) != `{"alwaysShowCaption":true,"captionDismissSecs":120,"captionFadeSecs":4,"mode":"voice","replyHistoryMins":5}` {
		t.Fatal(string(out))
	}
}

// #340: turning agent.markForcedSends off (or on) tells the running agent at once.
func TestMarkForcedSendsAppliesLiveToTheAgent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	for _, on := range []bool{false, true} {
		if s := h.set("agent.markForcedSends", on); s.SetError != nil && string(s.SetError) != "null" {
			t.Fatal(string(s.SetError))
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.markForced, []bool{false, true}) {
		t.Fatal(f.markForced)
	}
}
