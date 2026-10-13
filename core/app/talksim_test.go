package app

// Port of ai-mascot src-tauri/src/talk_sim.rs tests (5 of 5). The Rust simulator runs on a thread
// fed by an mpsc channel; here it is a goroutine fed by a Go channel, closed instead of dropped.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMouth is a 4-level pack. It counts levels() reads, which take the app lock in the real port.
type fakeMouth struct {
	mu    sync.Mutex
	reads int
	sent  []uint32
	// voice is shared with the test: a voice conversation is open.
	voice *atomic.Bool
	// sawVoice is what the simulator last saw from VoiceActive().
	sawVoice bool
	// sentDuringVoice are levels sent right after the simulator saw a conversation open.
	sentDuringVoice []uint32
}

func (f *fakeMouth) Levels() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return 4
}

func (f *fakeMouth) VoiceActive() bool {
	on := f.voice.Load()
	f.mu.Lock()
	f.sawVoice = on
	f.mu.Unlock()
	return on
}

func (f *fakeMouth) Mouth(level uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, level)
	if f.sawVoice {
		f.sentDuringVoice = append(f.sentDuringVoice, level)
	}
}

// spawnSim runs the simulator; the returned func waits for it and yields (reads, sent).
func spawnSim(t *testing.T, sw <-chan bool, voice *atomic.Bool) func() (int, []uint32) {
	t.Helper()
	if voice == nil {
		voice = &atomic.Bool{}
	}
	port := &fakeMouth{voice: voice}
	done := make(chan struct{})
	go func() {
		RunTalkSim(sw, port, time.Millisecond)
		close(done)
	}()
	return func() (int, []uint32) {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("simulator did not stop")
		}
		port.mu.Lock()
		defer port.mu.Unlock()
		for _, l := range port.sentDuringVoice {
			if l != 0 {
				t.Errorf("animated over the voice: %v", port.sentDuringVoice)
			}
		}
		if len(port.sentDuringVoice) > 1 {
			t.Errorf("closes once at most: %v", port.sentDuringVoice)
		}
		return port.reads, append([]uint32(nil), port.sent...)
	}
}

func onlyChanges(sent []uint32) bool {
	for i := 1; i < len(sent); i++ {
		if sent[i] == sent[i-1] {
			return false
		}
	}
	return true
}

func TestWhileOffItNeverReadsStateOrSends(t *testing.T) {
	sw := make(chan bool)
	join := spawnSim(t, sw, nil)
	time.Sleep(60 * time.Millisecond)
	close(sw)
	reads, sent := join()
	if reads != 0 || len(sent) != 0 {
		t.Fatalf("reads %d sent %v", reads, sent)
	}
}

func TestWhileOnItAnimatesAndClosesTheMouthWhenTurnedOff(t *testing.T) {
	sw := make(chan bool)
	join := spawnSim(t, sw, nil)
	sw <- true
	time.Sleep(60 * time.Millisecond)
	sw <- false
	time.Sleep(20 * time.Millisecond)
	close(sw)
	reads, sent := join()
	if reads == 0 {
		t.Error("no reads")
	}
	opened := false
	for _, l := range sent {
		if l > 0 {
			opened = true
		}
		if l >= 4 {
			t.Errorf("level %d out of range", l)
		}
	}
	if !opened {
		t.Errorf("opened the mouth: %v", sent)
	}
	if len(sent) == 0 || sent[len(sent)-1] != 0 {
		t.Errorf("closed at the end: %v", sent)
	}
	if !onlyChanges(sent) {
		t.Errorf("only changes are sent: %v", sent)
	}
}

func TestItStopsWhenTheSwitchIsDroppedWhileTalking(t *testing.T) {
	sw := make(chan bool)
	join := spawnSim(t, sw, nil)
	sw <- true
	close(sw)
	join()
}

func TestTheSimYieldsTheMouthToAVoiceConversation(t *testing.T) {
	sw := make(chan bool)
	voice := &atomic.Bool{}
	join := spawnSim(t, sw, voice)
	sw <- true
	time.Sleep(60 * time.Millisecond)
	voice.Store(true) // a conversation opens: the voice loop owns the mouth
	time.Sleep(60 * time.Millisecond)
	voice.Store(false)
	time.Sleep(60 * time.Millisecond)
	close(sw)
	_, sent := join()
	firstZero, lastOpen := -1, -1
	for i, l := range sent {
		if l == 0 && firstZero < 0 {
			firstZero = i
		}
		if l > 0 {
			lastOpen = i
		}
	}
	if firstZero < 0 {
		t.Fatalf("closed for the conversation: %v", sent)
	}
	if lastOpen <= firstZero {
		t.Errorf("resumes after it: %v", sent)
	}
	if !onlyChanges(sent) {
		t.Errorf("only changes are sent: %v", sent)
	}
}

func TestDuringAConversationItSendsNothing(t *testing.T) {
	sw := make(chan bool)
	voice := &atomic.Bool{}
	voice.Store(true)
	join := spawnSim(t, sw, voice)
	sw <- true
	time.Sleep(60 * time.Millisecond)
	close(sw)
	_, sent := join()
	if len(sent) != 0 {
		t.Fatalf("the mouth stays the voice loop's: %v", sent)
	}
}
