package audio

import (
	"encoding/binary"
	"math"
)

// EncodeLE packs samples as little-endian int16 bytes.
func EncodeLE(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// DecodeLE unpacks little-endian int16 bytes; an odd trailing byte is returned in rest.
func DecodeLE(b []byte) (samples []int16, rest []byte) {
	n := len(b) / 2
	samples = make([]int16, n)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return samples, b[2*n:]
}

// RMS returns loudness normalised to 0..1.
func RMS(s []int16) float32 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		x := float64(v) / 32768.0
		sum += x * x
	}
	return float32(math.Sqrt(sum / float64(len(s))))
}

// Int16ToFloat maps a sample to [-1, 1): s / 32768.
func Int16ToFloat(s int16) float32 { return float32(s) / 32768 }

// FloatToInt16 maps a [-1, 1] sample back, rounding half away from zero (as Rust's f32::round)
// and clamping to the int16 range. Int16ToFloat then FloatToInt16 is exact.
func FloatToInt16(s float32) int16 {
	v := s * 32768
	if v >= 0 {
		v += 0.5
	} else {
		v -= 0.5
	}
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(v)
}
