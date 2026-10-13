package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxTimers: more running timers than this is surely a mistake.
	MaxTimers = 10
	// MaxSeconds is the longest timer: a day.
	MaxSeconds = 24 * 60 * 60
)

// DoneNotice is what the model is told when a timer ends during a conversation (LocalFacts
// explains the prefix).
func DoneNotice(label string, length time.Duration) string {
	return fmt.Sprintf("Timer done: %s (%s).", label, SpokenLength(length))
}

// Alarm is how a finished timer reaches the user (the app: the model says it, or a notification).
type Alarm interface {
	// Fire is called once when a timer ends. label is what the user called it, e.g. "pasta";
	// spoken is how long it ran, e.g. "5 minutes".
	Fire(label string, spoken string)
}

// TimerClock schedules timers; tests inject a fake they advance by hand.
type TimerClock interface {
	Now() time.Time
	// AfterFunc runs f after d, unless the returned stop is called first.
	AfterFunc(d time.Duration, f func()) (stop func())
}

type realTimerClock struct{}

func (realTimerClock) Now() time.Time { return time.Now() }
func (realTimerClock) AfterFunc(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

type running struct {
	id    uint64
	label string
	ends  time.Time
	stop  func()
}

// Timers are the running timers, shared by the tool and the callbacks that ring them.
type Timers struct {
	alarm    Alarm
	clock    TimerClock
	mu       sync.Mutex
	running_ []running
	next     uint64
}

func NewTimers(alarm Alarm) *Timers { return NewTimersWithClock(alarm, realTimerClock{}) }

func NewTimersWithClock(alarm Alarm, clock TimerClock) *Timers {
	return &Timers{alarm: alarm, clock: clock}
}

// Start begins a timer; it fails when too many are running.
func (t *Timers) Start(length time.Duration, label string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.running_) >= MaxTimers {
		return fmt.Errorf("%d timers are already running", MaxTimers)
	}
	t.next++
	id := t.next
	stop := t.clock.AfterFunc(length, func() {
		t.mu.Lock()
		still := false
		for i, r := range t.running_ {
			if r.id == id {
				t.running_ = append(t.running_[:i], t.running_[i+1:]...)
				still = true
				break
			}
		}
		t.mu.Unlock()
		// A cancel that won the lock after the timer fired has already told the user it stopped.
		if still {
			t.alarm.Fire(label, SpokenLength(length))
		}
	})
	t.running_ = append(t.running_, running{id: id, label: label, ends: t.clock.Now().Add(length), stop: stop})
	return nil
}

// Cancel stops the timers labelled label, or the only running timer if label is nil, and returns
// their labels. The check and the removal happen under one lock, so a timer ending in between
// can't change the answer.
func (t *Timers) Cancel(label *string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := func(rs []running) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.label)
		}
		return strings.Join(s, ", ")
	}
	if len(t.running_) == 0 {
		return nil, fmt.Errorf("no timers are running")
	}
	if label == nil && len(t.running_) > 1 {
		return nil, fmt.Errorf("several timers are running (%s): ask which", names(t.running_))
	}
	var gone, kept []running
	for _, r := range t.running_ {
		if label == nil || sameLabel(r.label, *label) {
			gone = append(gone, r)
		} else {
			kept = append(kept, r)
		}
	}
	if len(gone) == 0 {
		return nil, fmt.Errorf("no timer called \"%s\"; running: %s", *label, names(t.running_))
	}
	t.running_ = kept
	out := make([]string, 0, len(gone))
	for _, r := range gone {
		r.stop()
		out = append(out, r.label)
	}
	return out, nil
}

// TimerLeft is a running timer and the time it has left.
type TimerLeft struct {
	Label string
	Left  time.Duration
}

// List returns the running timers, soonest first.
func (t *Timers) List() []TimerLeft {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock.Now()
	out := make([]TimerLeft, 0, len(t.running_))
	for _, r := range t.running_ {
		out = append(out, TimerLeft{r.label, max(0, r.ends.Sub(now))})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Left < out[j].Left })
	return out
}

// sameLabel matches ignoring case, plurals and the word "timer": "the 5 minute timer" finds
// "5 minute timer", and "Pasta" finds "pasta".
func sameLabel(a, b string) bool {
	words := func(s string) []string {
		var out []string
		for _, w := range strings.Fields(s) {
			w = strings.ToLower(w)
			switch w {
			case "timer", "timers", "the", "my", "a":
				continue
			}
			out = append(out, strings.TrimSuffix(w, "s"))
		}
		return out
	}
	x, y := words(a), words(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// defaultLabel is "5 minute timer", "1 hour 30 minute timer": the name of an unlabelled timer.
func defaultLabel(length time.Duration) string {
	words := strings.Split(SpokenLength(length), " ")
	for i, w := range words {
		words[i] = strings.TrimSuffix(w, "s")
	}
	return strings.Join(words, " ") + " timer"
}

// SpokenLength is how the model says a length of time: "1 hour 5 minutes", "45 seconds".
func SpokenLength(d time.Duration) string {
	s := uint64(d / time.Second)
	if d%time.Second >= 500*time.Millisecond {
		s++
	}
	h, m, sec := s/3600, s/60%60, s%60
	var parts []string
	for _, p := range []struct {
		n    uint64
		unit string
	}{{h, "hour"}, {m, "minute"}, {sec, "second"}} {
		switch p.n {
		case 0:
		case 1:
			parts = append(parts, "1 "+p.unit)
		default:
			parts = append(parts, fmt.Sprintf("%d %ss", p.n, p.unit))
		}
	}
	if len(parts) == 0 {
		return "0 seconds"
	}
	return strings.Join(parts, " ")
}

// TimerTool is the `timer` tool: start, cancel or list the app's timers.
type TimerTool struct{ Timers *Timers }

func (TimerTool) Name() string { return "timer" }

func (TimerTool) Description() string {
	return "Start, cancel or list countdown timers. When one ends the user is told, even if no conversation " +
		"is open. Use it for \"set a timer\" and \"remind me in N minutes\". For a clock time (\"at 5 pm\"), " +
		"call get_time first and pass the seconds until then."
}

func (TimerTool) Parameters() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": ["start", "cancel", "list"] },
    "duration_seconds": { "type": "integer", "minimum": 1, "maximum": %d, "description": "For start: how long." },
    "label": { "type": "string", "description": "For start: what it's for, e.g. \"pasta\". For cancel: which timer; leave out if only one is running." }
  },
  "required": ["action"],
  "additionalProperties": false
}`, MaxSeconds))
}

func (tt TimerTool) Invoke(_ context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	a := parseArgs(raw)
	label, hasLabel := a.text("label")
	action, _ := a.str("action")
	switch action {
	case "start":
		secs, ok := a.uint("duration_seconds")
		if !ok || secs < 1 || secs > MaxSeconds {
			return fail(fmt.Sprintf("duration_seconds must be 1 to %d", MaxSeconds))
		}
		length := time.Duration(secs) * time.Second
		if !hasLabel {
			label = defaultLabel(length)
		}
		if err := tt.Timers.Start(length, label); err != nil {
			return fail(err.Error())
		}
		return reply(map[string]any{"started": SpokenLength(length), "label": label})
	case "cancel":
		var want *string
		if hasLabel {
			want = &label
		}
		gone, err := tt.Timers.Cancel(want)
		if err != nil {
			return fail(err.Error())
		}
		return reply(map[string]any{"cancelled": gone})
	case "list":
		timers := []map[string]any{}
		for _, t := range tt.Timers.List() {
			timers = append(timers, map[string]any{"label": t.Label, "left": SpokenLength(t.Left)})
		}
		return reply(map[string]any{"timers": timers})
	}
	return fail("action must be start, cancel or list")
}
