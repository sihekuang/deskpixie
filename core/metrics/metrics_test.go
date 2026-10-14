package metrics

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

func TestMicToSendPercentiles(t *testing.T) {
	var r Recorder
	// Feed 100..1 ms in reverse so ordering doesn't matter.
	for i := 100; i >= 1; i-- {
		r.MicToSend(time.Duration(i) * time.Millisecond)
	}
	s := r.Snapshot()
	if s.MicToSendP50Ms != 50 || s.MicToSendP95Ms != 95 {
		t.Errorf("p50/p95 = %v/%v, want 50/95", s.MicToSendP50Ms, s.MicToSendP95Ms)
	}
	if s.MicSamples != 100 {
		t.Errorf("samples = %d", s.MicSamples)
	}
}

func TestEmptyRecorder(t *testing.T) {
	var r Recorder
	s := r.Snapshot()
	if s.MicToSendP50Ms != 0 || s.MicToSendP95Ms != 0 || s.Underruns != 0 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestWindowKeepsRecentSamples(t *testing.T) {
	var r Recorder
	for i := 0; i < window; i++ {
		r.MicToSend(1000 * time.Millisecond)
	}
	for i := 0; i < window; i++ {
		r.MicToSend(2 * time.Millisecond)
	}
	if s := r.Snapshot(); s.MicToSendP95Ms != 2 || s.MicSamples != window {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestUnderrunsAndDrops(t *testing.T) {
	var r Recorder
	r.Underruns(3)
	r.Underruns(7)
	r.MicDrops(5)
	s := r.Snapshot()
	if s.Underruns != 7 || s.MicDrops != 5 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestMaxGCPause(t *testing.T) {
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	var r Recorder
	if s := r.Snapshot(); s.MaxGCPauseMs <= 0 {
		t.Errorf("MaxGCPauseMs = %v, want > 0", s.MaxGCPauseMs)
	}
}

func TestSnapshotJSON(t *testing.T) {
	var r Recorder
	r.MicToSend(4 * time.Millisecond)
	b, err := json.Marshal(r.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	for _, k := range []string{"micToSendP50Ms", "micToSendP95Ms", "maxGcPauseMs", "underruns", "micDrops", "micSamples"} {
		if _, ok := v[k]; !ok {
			t.Errorf("missing %q in %s", k, b)
		}
	}
}
