package tools

import "time"

// Clock is the time and the computer's zone, behind an interface so tests can fix "now".
type Clock interface {
	Now() time.Time
	// Zone is the computer's time zone.
	Zone() *time.Location
}

// SystemClock reads the real clock and the system zone (the zone name, not "Local").
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) Zone() *time.Location {
	if loc, err := time.LoadLocation(systemZoneName()); err == nil {
		return loc
	}
	return time.UTC
}

// FixedClock is stopped at NowTime in Location.
type FixedClock struct {
	NowTime  time.Time
	Location *time.Location
}

func (f FixedClock) Now() time.Time       { return f.NowTime }
func (f FixedClock) Zone() *time.Location { return f.Location }
