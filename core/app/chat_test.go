package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
)

// #221: the text box's calls, events and the text-mode hotkey.

func (f *fakeAgent) ChatSend(text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatErr != nil {
		return "", f.chatErr
	}
	f.sent = append(f.sent, text)
	return "job_000000000001", nil
}
func (f *fakeAgent) ChatStop() { f.mu.Lock(); f.stops++; f.mu.Unlock() }
func (f *fakeAgent) SetTextMode(on bool) {
	f.mu.Lock()
	f.textModes = append(f.textModes, on)
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
	if _, err := h.callErr("chat.send", map[string]string{"text": "again"}); err == nil || err.Error() != "Delegation is off (Coordinator: herdr not found)." {
		t.Fatal(err)
	}
	if !slices.Equal(f.sent, []string{"hello"}) {
		t.Fatal(f.sent)
	}
}

func TestChatSendWithoutAnAgentSaysTheCoordinatorIsOff(t *testing.T) {
	h := newHarness(t)
	if _, err := h.callErr("chat.send", map[string]string{"text": "hello"}); err == nil || err.Error() != "The coordinator is off." {
		t.Fatal(err)
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
	if out := h.call("chat.mode", nil); string(out) != `{"mode":"text"}` {
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
	if out := h.call("chat.mode", nil); string(out) != `{"mode":"text"}` {
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
