package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVoiceLogWritesTimestampedFlatLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "voice.log")
	l := NewVoiceLog(path, nil)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 11, 6, 10, 0, time.UTC) }
	l.Printf("user: %s", "how many agents\nare running")
	b, _ := os.ReadFile(path)
	if string(b) != "2026-10-15T11:06:10.000+00:00 user: how many agents are running\n" {
		t.Fatalf("%q", b)
	}
	var none *VoiceLog
	none.Printf("ignored") // a nil log is off
	if NewVoiceLog("", nil) != nil || VoiceLogPath("") != "" {
		t.Fatal("empty path")
	}
}

// #225: the voice log keeps to the Log size setting (the file and one older one, half each).
func TestVoiceLogRotatesAtTheLogSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	NewVoiceLog(path, func() int64 { return 1000 }).Printf("new")
	old, _ := os.Stat(path + ".1")
	cur, _ := os.ReadFile(path)
	if old == nil || old.Size() != 500 || !strings.HasSuffix(string(cur), " new\n") || len(cur) > 100 {
		t.Fatalf("old %v, current %q", old, cur)
	}
}

func TestClipLogNeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("a", voiceLogClip-1) + "é" + "tail"
	if got := clipLog(s); !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, strings.Repeat("a", voiceLogClip-1)) || strings.Contains(got, "�") {
		t.Fatalf("%q", got[len(got)-8:])
	}
}

// A conversation through the runtime leaves its story in the log: opened, what was said, the
// app's hand-off and its result, the notice, and why it closed.
func TestTheRuntimeLogsAConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.log")
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	reg := DefaultTools()
	reg.Register(&fakeDelegate{})
	cc := DefaultConversationConfig()
	cc.HandOff = true
	r := NewRuntime(m, nil, RuntimeConfig{
		Session: testSessionCfg, APIKey: "k", MouthLevels: 4, Tools: reg, Conversation: cc, Log: NewVoiceLog(path, nil),
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)
	for _, ev := range []Event{
		evSpeechStarted, evSpeechStopped, said("user", "What's the price of bitcoin right now?"),
		evResponseCreated, said("assistant", "I don't know."), evResponseDone,
	} {
		m.sess.events <- ev
	}
	waitLong(t, "the hand-off notice", func() bool { return len(m.sess.commands(CmdInject)) == 1 })
	r.Hotkey()
	want := []string{
		"connecting to ", "conversation opened", "phase Connecting -> Listening",
		"user: What's the price of bitcoin right now?", "pixie: I don't know.",
		"app hand-off (unanswered request): What's the price of bitcoin right now?",
		"hand-off result: handed it to the coordinator as job_0000000000aa",
		"notice to Pixie: Coordinator: handed off: What's the price of bitcoin right now?",
		"conversation closed: hotkey",
	}
	waitLong(t, "the closed line", func() bool {
		b, _ := os.ReadFile(path)
		return strings.Contains(string(b), want[len(want)-1])
	})
	b, _ := os.ReadFile(path)
	log := string(b)
	at := 0
	for _, w := range want {
		i := strings.Index(log[at:], w)
		if i < 0 {
			t.Fatalf("missing (in order) %q in:\n%s", w, log)
		}
		at += i
	}
}
