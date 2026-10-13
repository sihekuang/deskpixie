package tools

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeTimerClock is a manually advanced clock; due callbacks run inside Advance.
type fakeTimerClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeTimerClock() *fakeTimerClock { return &fakeTimerClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeTimerClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeTimerClock) AfterFunc(d time.Duration, f func()) (stop func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{at: c.now.Add(d), f: f}
	c.pending = append(c.pending, ft)
	return func() { c.mu.Lock(); ft.stopped = true; c.mu.Unlock() }
}

func (c *fakeTimerClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.Slice(c.pending, func(i, j int) bool { return c.pending[i].at.Before(c.pending[j].at) })
		var next *fakeTimer
		for i, p := range c.pending {
			if p.stopped {
				continue
			}
			if !p.at.After(target) {
				next = p
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				break
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}

type bellRing struct {
	label  string
	spoken string
}

type bell struct {
	mu    sync.Mutex
	rings []bellRing
}

func (b *bell) Fire(label, spoken string) {
	b.mu.Lock()
	b.rings = append(b.rings, bellRing{label, spoken})
	b.mu.Unlock()
}

func (b *bell) all() []bellRing {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bellRing(nil), b.rings...)
}

func rig() (TimerTool, *bell, *fakeTimerClock) {
	b, c := &bell{}, newFakeTimerClock()
	return TimerTool{Timers: NewTimersWithClock(b, c)}, b, c
}

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

func TestATimerRingsOnceWhenItEnds(t *testing.T) {
	tl, b, c := rig()
	eq(t, call(t, tl, `{"action":"start","duration_seconds":300,"label":"pasta"}`), `{"started":"5 minutes","label":"pasta"}`)
	c.Advance(sec(299))
	if len(b.all()) != 0 {
		t.Fatal("rang early")
	}
	c.Advance(sec(2))
	if !reflect.DeepEqual(b.all(), []bellRing{{"pasta", "5 minutes"}}) {
		t.Fatal(b.all())
	}
	eq(t, call(t, tl, `{"action":"list"}`), `{"timers":[]}`) // a rung timer is gone
}

func TestAnUnlabelledTimerIsNamedByItsLength(t *testing.T) {
	tl, _, _ := rig()
	eq(t, call(t, tl, `{"action":"start","duration_seconds":3725}`), `{"started":"1 hour 2 minutes 5 seconds","label":"1 hour 2 minute 5 second timer"}`)
	if call(t, tl, `{"action":"start","duration_seconds":300}`)["label"] != "5 minute timer" {
		t.Fatal("label")
	}
	eq(t, call(t, tl, `{"action":"cancel","label":"the 5 minutes timer"}`), `{"cancelled":["5 minute timer"]}`)
}

func TestSameNamedTimersAreListedAndCancelledTogether(t *testing.T) {
	tl, b, c := rig()
	call(t, tl, `{"action":"start","duration_seconds":60,"label":"tea"}`)
	call(t, tl, `{"action":"start","duration_seconds":120,"label":"tea"}`)
	if n := len(call(t, tl, `{"action":"list"}`)["timers"].([]any)); n != 2 {
		t.Fatal(n)
	}
	eq(t, call(t, tl, `{"action":"cancel","label":"tea"}`), `{"cancelled":["tea","tea"]}`)
	eq(t, call(t, tl, `{"action":"list"}`), `{"timers":[]}`)
	c.Advance(sec(200))
	if len(b.all()) != 0 {
		t.Fatal("rang")
	}
}

func TestListShowsTimeLeftSoonestFirst(t *testing.T) {
	tl, _, c := rig()
	call(t, tl, `{"action":"start","duration_seconds":600,"label":"eggs"}`)
	call(t, tl, `{"action":"start","duration_seconds":90,"label":"tea"}`)
	c.Advance(sec(30))
	eq(t, call(t, tl, `{"action":"list"}`), `{"timers":[{"label":"tea","left":"1 minute"},{"label":"eggs","left":"9 minutes 30 seconds"}]}`)
}

func TestACancelledTimerNeverRings(t *testing.T) {
	tl, b, c := rig()
	call(t, tl, `{"action":"start","duration_seconds":60,"label":"Tea"}`)
	eq(t, call(t, tl, `{"action":"cancel","label":" tea "}`), `{"cancelled":["Tea"]}`)
	c.Advance(sec(120))
	if len(b.all()) != 0 {
		t.Fatal("rang")
	}
}

func TestCancelWithoutALabelNeedsExactlyOneTimer(t *testing.T) {
	tl, _, _ := rig()
	eq(t, call(t, tl, `{"action":"cancel"}`), `{"error":"no timers are running"}`)
	call(t, tl, `{"action":"start","duration_seconds":60,"label":"tea"}`)
	call(t, tl, `{"action":"start","duration_seconds":90,"label":"eggs"}`)
	eq(t, call(t, tl, `{"action":"cancel"}`), `{"error":"several timers are running (tea, eggs): ask which"}`)
	eq(t, call(t, tl, `{"action":"cancel","label":"rice"}`), `{"error":"no timer called \"rice\"; running: tea, eggs"}`)
	call(t, tl, `{"action":"cancel","label":"eggs"}`)
	eq(t, call(t, tl, `{"action":"cancel"}`), `{"cancelled":["tea"]}`)
}

func TestBadStartsAreRefused(t *testing.T) {
	tl, _, _ := rig()
	for _, a := range []string{`{"action":"start"}`, `{"action":"start","duration_seconds":0}`, fmt.Sprintf(`{"action":"start","duration_seconds":%d}`, MaxSeconds+1)} {
		eq(t, call(t, tl, a), `{"error":"duration_seconds must be 1 to 86400"}`)
	}
	eq(t, call(t, tl, `{"action":"snooze"}`), `{"error":"action must be start, cancel or list"}`)
	for i := 0; i < MaxTimers; i++ {
		call(t, tl, `{"action":"start","duration_seconds":60}`)
	}
	eq(t, call(t, tl, `{"action":"start","duration_seconds":60}`), `{"error":"10 timers are already running"}`)
}

func TestLengthsAreSpokenInWholeUnits(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{sec(45), "45 seconds"}, {sec(3600), "1 hour"}, {59600 * time.Millisecond, "1 minute"}, {0, "0 seconds"}} {
		if got := SpokenLength(c.d); got != c.want {
			t.Errorf("%v -> %q want %q", c.d, got, c.want)
		}
	}
}

// Extra (no Rust twin): the notice text the app injects, and the real-clock constructor.
func TestDoneNotice(t *testing.T) {
	if got := DoneNotice("pasta", sec(300)); got != "Timer done: pasta (5 minutes)." {
		t.Fatal(got)
	}
	if NewTimers(&bell{}) == nil {
		t.Fatal("nil")
	}
}
