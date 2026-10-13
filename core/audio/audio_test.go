package audio

import (
	"bytes"
	"math"
	"slices"
	"testing"
)

func TestPassthroughReturnsInput(t *testing.T) {
	in := []int16{1, -2, 3}
	if got := (Passthrough{}).Process(in); !slices.Equal(got, in) {
		t.Fatalf("got %v", got)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768}
	out, rest := DecodeLE(EncodeLE(in))
	if !slices.Equal(out, in) || len(rest) != 0 {
		t.Fatalf("got %v rest %v", out, rest)
	}
}

func TestDecodeCarriesOddByte(t *testing.T) {
	out, rest := DecodeLE([]byte{0x01, 0x00, 0x7f})
	if !slices.Equal(out, []int16{1}) || !bytes.Equal(rest, []byte{0x7f}) {
		t.Fatal(out, rest)
	}
}

func TestRMS(t *testing.T) {
	if RMS(nil) != 0 {
		t.Fatal("empty")
	}
	if r := RMS([]int16{16384, -16384}); math.Abs(float64(r)-0.5) > 1e-3 {
		t.Fatal(r)
	}
}
