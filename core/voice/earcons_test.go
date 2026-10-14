package voice

import "testing"

// Port of the 4 #[test]s in ai-mascot crates/voice-core/src/earcons.rs.

func zeroCrossings(s []int16) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return n
}

func TestChimesLast180ms(t *testing.T) {
	for _, k := range []Chime{ChimeStart, ChimeEnd} {
		if n := len(ChimeSamples(k)); n != 2*2160 {
			t.Errorf("%v: %d samples, want two 90 ms notes at 24 kHz", k, n)
		}
	}
}

func TestChimesAreAudibleButNotClipping(t *testing.T) {
	for _, k := range []Chime{ChimeStart, ChimeEnd} {
		s := ChimeSamples(k)
		if r := RMS(s); r <= 0.1 {
			t.Errorf("%v rms %v", k, r)
		}
		limit := 8519 // 0.26 * 32767, truncated like Rust's `as u16`
		for _, v := range s {
			if a := int(v); a > limit || -a > limit {
				t.Fatalf("%v: sample %d clips", k, v)
			}
		}
	}
}

func TestStartRisesAndEndFalls(t *testing.T) {
	s := ChimeSamples(ChimeStart)
	a, b := s[:len(s)/2], s[len(s)/2:]
	if zeroCrossings(b) <= zeroCrossings(a) {
		t.Error("start chime should rise in pitch")
	}
	e := ChimeSamples(ChimeEnd)
	a, b = e[:len(e)/2], e[len(e)/2:]
	if zeroCrossings(b) >= zeroCrossings(a) {
		t.Error("end chime should fall in pitch")
	}
}

func TestChimesFadeInAndOut(t *testing.T) {
	s := ChimeSamples(ChimeStart)
	abs := func(v int16) int { return max(int(v), -int(v)) }
	if abs(s[0]) >= 200 || abs(s[len(s)-1]) >= 200 {
		t.Fatalf("edges %d %d", s[0], s[len(s)-1])
	}
}
