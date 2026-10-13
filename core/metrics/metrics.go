// Package metrics records the Phase 3 go/no-go numbers: mic capture -> websocket write
// latency, the worst Go GC pause, Swift playback underruns and dropped mic frames.
package metrics

import (
	"math"
	"runtime/metrics"
	"sort"
	"sync"
	"time"
)

// window is how many recent mic-to-send samples the percentiles cover.
const window = 1024

const gcPauses = "/sched/pauses/total/gc:seconds"

// Snapshot is a point-in-time summary. Milliseconds are float64.
type Snapshot struct {
	MicToSendP50Ms float64 `json:"micToSendP50Ms"`
	MicToSendP95Ms float64 `json:"micToSendP95Ms"`
	MicSamples     int     `json:"micSamples"` // samples behind the percentiles (<= window)
	MaxGCPauseMs   float64 `json:"maxGcPauseMs"`
	Underruns      int64   `json:"underruns"`
	MicDrops       uint64  `json:"micDrops"`
}

// Recorder is safe for concurrent use. The zero value is ready.
type Recorder struct {
	mu        sync.Mutex
	samples   [window]time.Duration
	n         int // samples recorded (capped at window)
	next      int // ring write index
	underruns int64
	drops     uint64
}

// MicToSend records one capture-timestamp -> websocket-write latency.
func (r *Recorder) MicToSend(d time.Duration) {
	r.mu.Lock()
	r.samples[r.next] = d
	r.next = (r.next + 1) % window
	if r.n < window {
		r.n++
	}
	r.mu.Unlock()
}

// Underruns stores the cumulative underrun count reported by Swift.
func (r *Recorder) Underruns(total int64) {
	r.mu.Lock()
	r.underruns = total
	r.mu.Unlock()
}

// MicDrops stores the cumulative count of mic frames dropped before the runtime saw them.
func (r *Recorder) MicDrops(total uint64) {
	r.mu.Lock()
	r.drops = total
	r.mu.Unlock()
}

// Snapshot summarises what has been recorded, plus the process's worst GC pause so far.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	sorted := append([]time.Duration(nil), r.samples[:r.n]...)
	s := Snapshot{MicSamples: r.n, Underruns: r.underruns, MicDrops: r.drops}
	r.mu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	s.MicToSendP50Ms = ms(percentile(sorted, 50))
	s.MicToSendP95Ms = ms(percentile(sorted, 95))
	s.MaxGCPauseMs = MaxGCPause().Seconds() * 1000
	return s
}

// percentile is the nearest-rank percentile of an ascending slice (0 when empty).
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(float64(p) / 100 * float64(len(sorted))))
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// MaxGCPause is the upper bound of the highest non-empty bucket of the runtime's GC
// stop-the-world pause histogram (since process start). Infinite bounds fall back to the
// bucket's lower bound.
func MaxGCPause() time.Duration {
	sample := []metrics.Sample{{Name: gcPauses}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindFloat64Histogram {
		return 0
	}
	h := sample[0].Value.Float64Histogram()
	for i := len(h.Counts) - 1; i >= 0; i-- {
		if h.Counts[i] == 0 {
			continue
		}
		v := h.Buckets[i+1]
		if math.IsInf(v, 0) {
			v = h.Buckets[i]
		}
		if math.IsInf(v, 0) || v < 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	}
	return 0
}
