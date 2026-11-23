package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// replyTimingRun speaks into a listening conversation, has the provider end that turn without a
// reply when silentFirst, then plays a reply, and returns the voice log (#481).
func replyTimingRun(t *testing.T, silentFirst bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "voice.log")
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "sk-test", MouthLevels: 4,
		Conversation: DefaultConversationConfig(), Log: NewVoiceLog(path, nil),
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)
	r.PushMic(pcmFrame(8000, audio.FrameSamples), time.Now())
	eventually(t, "speech sent", func() bool { return len(m.sess.commands(CmdAudio)) == 1 })
	m.sess.events <- Event{Kind: EvSpeechStarted}
	m.sess.events <- Event{Kind: EvSpeechStopped}
	if silentFirst {
		// No reply to it (noise, a backchannel): the turn ends with nothing said.
		m.sess.events <- Event{Kind: EvResponseCreated}
		m.sess.events <- Event{Kind: EvResponseDone}
		eventually(t, "back to listening", func() bool { return strings.Contains(logOf(path), "phase Thinking -> Listening") })
	}
	m.sess.events <- Event{Kind: EvResponseCreated}
	m.sess.events <- Event{Kind: EvAudioDelta, Audio: pcmFrame(5, 2400)}
	eventually(t, "reply queued", func() bool { return r.queuedSamples() == 2400 })
	r.PullPlayback(make([]int16, 2400))
	r.ReportPlayback(2400, 0, 0.2)
	m.sess.events <- Event{Kind: EvResponseDone}
	eventually(t, "reply audio line", func() bool {
		return strings.Contains(logOf(path), "reply audio:")
	})
	time.Sleep(200 * time.Millisecond) // a tick or two, for the timing line
	return logOf(path)
}

func logOf(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// A reply right after the user's speech is timed from it.
func TestReplyTimingIsLoggedAfterSpeech(t *testing.T) {
	if log := replyTimingRun(t, false); !strings.Contains(log, "reply timing: you stopped") {
		t.Fatalf("no reply timing line:\n%s", log)
	}
}

// Speech the provider didn't answer isn't the start of a later reply (an update said seconds
// later): that reply gets no timing line.
func TestReplyTimingSkipsSpeechThatGotNoReply(t *testing.T) {
	if log := replyTimingRun(t, true); strings.Contains(log, "reply timing") {
		t.Fatalf("a reply was timed from unanswered speech:\n%s", log)
	}
}
