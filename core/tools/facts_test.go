package tools

import (
	"strings"
	"testing"
	"time"
)

func factsClock() Clock {
	return fixedClock(time.Date(2026, 10, 11, 16, 5, 0, 0, time.UTC), "Europe/London")
}

func TestFactsGiveTheLocalDateTimeAndZone(t *testing.T) {
	f := LocalFacts{Clock: factsClock()}.Facts()
	if !strings.HasPrefix(f, "Local facts:\n- This conversation started on Sunday, October 11, 2026 at 5:05 PM, time zone Europe/London.") {
		t.Fatal(f)
	}
	if strings.Contains(f, "home city") {
		t.Fatal(f)
	}
	for _, s := range []string{"call get_time", "get_weather", "calculate", "call timer", "call media", `"Timer done:"`} {
		if !strings.Contains(f, s) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestTheHomeCityIsIncludedWhenSet(t *testing.T) {
	f := LocalFacts{Clock: factsClock(), Home: "Leeds"}.Facts()
	if !strings.Contains(f, "\n- The user's home city is Leeds.\n") {
		t.Fatal(f)
	}
}
