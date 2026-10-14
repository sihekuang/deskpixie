package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// call invokes a tool with JSON args and decodes the output object.
func call(t *testing.T, tool Tool, args string) map[string]any {
	t.Helper()
	out, end, err := tool.Invoke(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	if end {
		t.Fatalf("tool ended the conversation")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("bad output %s: %v", out, err)
	}
	return m
}

// eq asserts the decoded output equals the JSON literal want.
func eq(t *testing.T, got map[string]any, want string) {
	t.Helper()
	var w map[string]any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want %s: %v", want, err)
	}
	if !reflect.DeepEqual(got, w) {
		g, _ := json.Marshal(got)
		t.Fatalf("got  %s\nwant %s", g, want)
	}
}

func errOf(m map[string]any) string {
	s, _ := m["error"].(string)
	return s
}

func fixedClock(now time.Time, zone string) Clock {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		panic(err)
	}
	return FixedClock{NowTime: now, Location: loc}
}

// fakeAtlas knows a fixed set of places; Delay makes every lookup slow (respecting ctx).
type fakeAtlas struct {
	places map[string]Place
	delay  time.Duration
	fail   bool
	mu     sync.Mutex
	asked  []string
}

func atlasWith(ps ...Place) *fakeAtlas {
	a := &fakeAtlas{places: map[string]Place{}}
	for _, p := range ps {
		a.places[strings.ToLower(p.Name)] = p
	}
	return a
}

func (a *fakeAtlas) Find(ctx context.Context, q string) (*Place, error) {
	a.mu.Lock()
	a.asked = append(a.asked, q)
	a.mu.Unlock()
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if a.fail {
		return nil, LookupFailed("HTTP 500")
	}
	if p, ok := a.places[strings.ToLower(q)]; ok {
		return &p, nil
	}
	return nil, nil
}

type skyAsk struct {
	name  string
	units Units
}

type fakeSky struct {
	forecast Forecast
	delay    time.Duration
	mu       sync.Mutex
	asked    []skyAsk
}

func (s *fakeSky) Forecast(ctx context.Context, p Place, u Units) (Forecast, error) {
	s.mu.Lock()
	s.asked = append(s.asked, skyAsk{p.Name, u})
	s.mu.Unlock()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return Forecast{}, ctx.Err()
		}
	}
	return s.forecast, nil
}

func mkPlace(name, region, country, code, tz string) Place {
	return Place{Name: name, Region: region, Country: country, CountryCode: code, Latitude: 1, Longitude: 2, Timezone: tz}
}
