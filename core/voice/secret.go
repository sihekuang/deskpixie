package voice

import (
	"fmt"
	"math"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// Secret is an API key. It never prints its value: every fmt verb shows "Secret(***)".
// Port of voice-core types.rs `Secret`.
type Secret struct{ v string }

// NewSecret wraps a key.
func NewSecret(v string) Secret { return Secret{v} }

// Expose returns the raw value, for an auth header. Never log the result.
func (s Secret) Expose() string { return s.v }

func (s Secret) String() string   { return "Secret(***)" }
func (s Secret) GoString() string { return "Secret(***)" }

// Format makes every verb (%v, %+v, %#v, %s, %q, ...) print the redacted form.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprint(f, `"Secret(***)"`)
		return
	}
	_, _ = fmt.Fprint(f, "Secret(***)")
}

// FrameDuration is how long samples at 24 kHz last.
func FrameDuration(samples []int16) time.Duration {
	return time.Duration(len(samples)) * time.Second / audio.SampleRate
}

// RMS is root-mean-square loudness: 0 for silence, 1 for a full-scale square wave.
func RMS(samples []int16) float32 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		x := float64(s) / 32768
		sum += x * x
	}
	return float32(math.Sqrt(sum / float64(len(samples))))
}

// AudioErrorKind mirrors voice-core types.rs `AudioError`.
type AudioErrorKind int

const (
	AudioPermissionDenied AudioErrorKind = iota
	AudioDeviceChanged
	AudioOther
)

// AudioError is what the app's audio side reports (Runtime.AudioFailed).
type AudioError struct {
	Kind AudioErrorKind
	Msg  string // AudioOther
}

func (e AudioError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	switch e.Kind {
	case AudioPermissionDenied:
		return "microphone access denied"
	case AudioDeviceChanged:
		return "audio device changed"
	}
	return "audio error"
}

// FailureFromAudio maps an audio error to the conversation failure it causes.
func FailureFromAudio(e AudioError) Failure {
	switch e.Kind {
	case AudioPermissionDenied:
		return Failure{Kind: FailMicDenied}
	case AudioDeviceChanged:
		return Failure{Kind: FailAudioDeviceChanged}
	}
	return SetupFailure("audio: " + e.Msg)
}
