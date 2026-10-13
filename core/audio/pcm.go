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
