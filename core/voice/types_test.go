package voice

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// Port of the 6 #[test]s in ai-mascot crates/voice-core/src/types.rs.
// failure_status_lines_are_exact lives in failure_test.go.

func TestFrameDurationIsSamplesOver24k(t *testing.T) {
	if got := FrameDuration(make([]int16, 2400)); got != 100*time.Millisecond {
		t.Fatalf("2400 samples = %v", got)
	}
	if got := FrameDuration(nil); got != 0 {
		t.Fatalf("empty = %v", got)
	}
}

func TestRMSOfSilenceIsZeroAndFullScaleIsOne(t *testing.T) {
	if RMS(make([]int16, 100)) != 0 || RMS(nil) != 0 {
		t.Fatal("silence must be 0")
	}
	sq := make([]int16, 100)
	for i := range sq {
		if i%2 == 0 {
			sq[i] = math.MaxInt16
		} else {
			sq[i] = math.MinInt16
		}
	}
	if r := RMS(sq); math.Abs(float64(r)-1) >= 0.001 {
		t.Fatalf("square rms %v", r)
	}
}

func TestSecretNeverPrintsItsValue(t *testing.T) {
	s := NewSecret("sk-very-secret")
	for _, f := range []string{"%v", "%s", "%+v", "%#v", "%q"} {
		if got := fmt.Sprintf(f, s); got != "Secret(***)" && got != `"Secret(***)"` {
			t.Errorf("%s printed %q", f, got)
		}
	}
	if s.Expose() != "sk-very-secret" {
		t.Fatal("Expose")
	}
}

func TestPixieStatesUseSlice1Names(t *testing.T) {
	cases := map[PixieState]string{
		StateIdle: "idle", StateListening: "listening", StateThinking: "thinking",
		StateDelegating: "delegating", StateHappy: "happy", StateError: "error",
	}
	for s, want := range cases {
		if string(s) != want {
			t.Errorf("%q != %q", s, want)
		}
	}
}

func TestAudioErrorsMapToFailures(t *testing.T) {
	cases := []struct {
		e    AudioError
		want Failure
	}{
		{AudioError{Kind: AudioPermissionDenied}, Failure{Kind: FailMicDenied}},
		{AudioError{Kind: AudioDeviceChanged}, Failure{Kind: FailAudioDeviceChanged}},
		{AudioError{Kind: AudioOther, Msg: "boom"}, SetupFailure("audio: boom")},
	}
	for _, c := range cases {
		if got := FailureFromAudio(c.e); got != c.want {
			t.Errorf("%+v -> %+v, want %+v", c.e, got, c.want)
		}
	}
}
