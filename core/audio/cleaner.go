// Package audio holds the app-wide audio primitives. All app audio is 24 kHz mono int16 LE.
package audio

const (
	SampleRate   = 24000
	FrameSamples = 960 // 40 ms
)

// Cleaner post-processes mic frames (e.g. noise suppression).
type Cleaner interface {
	Process(in []int16) []int16
	Close() error
}

// Passthrough returns frames unchanged.
type Passthrough struct{}

func (Passthrough) Process(in []int16) []int16 { return in }
func (Passthrough) Close() error               { return nil }
