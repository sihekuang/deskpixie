package agentapp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Ports of src-tauri/src/agent/{notify.rs (1), echo.rs (4), store.rs (1)} tests.

// notify.rs `chime_and_notification_commands`. Adapted: the chime is still afplay with
// PATH=/usr/bin:/bin, but the notification is the app's `notify` event (Swift posts it with
// UNUserNotificationCenter) instead of `osascript display notification`, so the second half
// checks the event's title and body (no sound: the chime is separate) rather than the script.
func TestChimeAndNotificationCommands(t *testing.T) {
	r := &agenttest.FakeRunner{}
	var got [][2]string
	n := NewNotifier(r, func(title, body string) { got = append(got, [2]string{title, body}) })
	n.Chime()
	n.Notify("Bit", `Run "git push" in focus? Say confirm hawk.`)
	spawned := r.Spawned()
	if len(spawned) != 1 || !reflect.DeepEqual(spawned[0].Argv, []string{"/usr/bin/afplay", Chime}) {
		t.Fatalf("%+v", spawned)
	}
	if !reflect.DeepEqual(spawned[0].Env, []agent.EnvVar{{Key: "PATH", Value: "/usr/bin:/bin"}}) {
		t.Fatal(spawned[0].Env)
	}
	if Chime != "/System/Library/Sounds/Glass.aiff" {
		t.Fatal(Chime)
	}
	if !reflect.DeepEqual(got, [][2]string{{"Bit", `Run "git push" in focus? Say confirm hawk.`}}) {
		t.Fatal(got)
	}
}

func said() voice.VoiceSignal {
	return voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"}
}

func TestUserWordsPassWhenBitIsQuiet(t *testing.T) {
	var gate EchoGate
	if !gate.Admits(said(), time.Now()) {
		t.Fatal("dropped")
	}
}

func TestUserWordsAreDroppedWhileBitSpeaksAndForTheTail(t *testing.T) {
	t0 := time.Now()
	var gate EchoGate
	gate.OnMouth(3, t0)
	if gate.Admits(said(), t0.Add(10*time.Second)) {
		t.Fatal("still speaking")
	}
	gate.OnMouth(1, t0.Add(time.Second))
	gate.OnMouth(0, t0.Add(2*time.Second))
	ended := t0.Add(2 * time.Second)
	if gate.Admits(said(), ended) || gate.Admits(said(), ended.Add(EchoTail-time.Millisecond)) {
		t.Fatal("inside the tail")
	}
	if !gate.Admits(said(), ended.Add(EchoTail)) {
		t.Fatal("after the tail")
	}
	if EchoTail != 1500*time.Millisecond {
		t.Fatal(EchoTail)
	}
}

func TestANewUtteranceRestartsTheGate(t *testing.T) {
	t0 := time.Now()
	var gate EchoGate
	gate.OnMouth(2, t0)
	gate.OnMouth(0, t0)
	gate.OnMouth(0, t0.Add(5*time.Second)) // a repeated 0 doesn't move the end
	if !gate.Admits(said(), t0.Add(2*time.Second)) {
		t.Fatal("tail moved")
	}
	gate.OnMouth(2, t0.Add(3*time.Second))
	if gate.Admits(said(), t0.Add(3*time.Second)) {
		t.Fatal("speaking again")
	}
}

func TestOpenedAndClosedAlwaysPass(t *testing.T) {
	t0 := time.Now()
	var gate EchoGate
	gate.OnMouth(4, t0)
	if !gate.Admits(voice.VoiceSignal{Kind: voice.SigOpened}, t0) || !gate.Admits(voice.VoiceSignal{Kind: voice.SigClosed}, t0) {
		t.Fatal("dropped")
	}
}

func TestAppendLoadAndRewrite(t *testing.T) {
	dir := t.TempDir()
	s := NewJsonlStore(dir)
	if len(s.Load("jobs.jsonl")) != 0 {
		t.Fatal("not empty")
	}
	s.Append("jobs.jsonl", `{"a":1}`)
	s.Append("jobs.jsonl", `{"a":2}`)
	if got := s.Load("jobs.jsonl"); !reflect.DeepEqual(got, []string{`{"a":1}`, `{"a":2}`}) {
		t.Fatal(got)
	}
	s.Rewrite("jobs.jsonl", []string{`{"a":2}`})
	if got := s.Load("jobs.jsonl"); !reflect.DeepEqual(got, []string{`{"a":2}`}) {
		t.Fatal(got)
	}
	info, err := os.Stat(filepath.Join(dir, "jobs.jsonl"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("rewritten privately", info.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("a temp file was left", entries)
	}
}
