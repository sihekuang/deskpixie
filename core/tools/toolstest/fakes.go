// Package toolstest has fakes for the local tools' outside boundaries (place lookup, weather,
// the music player, the timer clock), for tests outside core/tools that drive the real tools
// without the network, a music app or real time.
package toolstest

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/tools"
)

// Atlas is a Geocoder that knows a fixed set of places (by name, case-insensitive).
type Atlas struct{ Places []tools.Place }

func (a Atlas) Find(_ context.Context, q string) (*tools.Place, error) {
	for _, p := range a.Places {
		if strings.EqualFold(p.Name, q) {
			return &p, nil
		}
	}
	return nil, nil
}

// Sky is a WeatherSource that always reports Today.
type Sky struct{ Today tools.Forecast }

func (s Sky) Forecast(context.Context, tools.Place, tools.Units) (tools.Forecast, error) {
	return s.Today, nil
}

// Player is a MediaPlayer that records each action and answers Said.
type Player struct {
	Said string
	mu   sync.Mutex
	acts []tools.MediaAction
}

func (p *Player) Act(_ context.Context, a tools.MediaAction) (string, error) {
	p.mu.Lock()
	p.acts = append(p.acts, a)
	p.mu.Unlock()
	return p.Said, nil
}

// Acts are the actions asked so far.
func (p *Player) Acts() []tools.MediaAction {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]tools.MediaAction(nil), p.acts...)
}

// TimerClock is a tools.TimerClock that moves only when Advance is called.
type TimerClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*pending
}

type pending struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

// NewTimerClock starts at a fixed instant.
func NewTimerClock() *TimerClock { return &TimerClock{now: time.Unix(1_000_000, 0)} }

func (c *TimerClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *TimerClock) AfterFunc(d time.Duration, f func()) (stop func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := &pending{at: c.now.Add(d), f: f}
	c.pending = append(c.pending, p)
	return func() { c.mu.Lock(); p.stopped = true; c.mu.Unlock() }
}

// Advance moves the clock by d, running each timer due by then in order (outside the lock).
func (c *TimerClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.pending, func(i, j int) bool { return c.pending[i].at.Before(c.pending[j].at) })
		var next *pending
		for _, p := range c.pending {
			if !p.stopped && !p.fired && !p.at.After(target) {
				next = p
				break
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.fired = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}
