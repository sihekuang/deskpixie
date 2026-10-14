package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// GetTime is `get_time`: the time here, or in another place or time zone.
type GetTime struct {
	Clock  Clock
	Places Geocoder
	// Timeout bounds a place lookup; 0 means LookupTimeout.
	Timeout time.Duration
}

func (GetTime) Name() string { return "get_time" }

func (GetTime) Description() string {
	return "The current time and date, here or somewhere else. Answers at once: use it instead of delegate " +
		"for any question about the time or date."
}

func (GetTime) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "place": {
      "type": "string",
      "description": "A city (\"Tokyo\", \"Portland, Oregon\") or IANA time zone. Leave out for the user's own time."
    }
  },
  "additionalProperties": false
}`)
}

func (g GetTime) Invoke(ctx context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	now := g.Clock.Now()
	asked, ok := parseArgs(raw).text("place")
	if !ok {
		return reply(timeIn(now, g.Clock.Zone(), ""))
	}
	if zone := zoneName(asked); zone != nil {
		return reply(timeIn(now, zone, ""))
	}
	ctx, cancel := context.WithTimeout(ctx, orDefault(g.Timeout))
	defer cancel()
	place, err := g.Places.Find(ctx, asked)
	if err != nil {
		if isTimeout(err) {
			return fail("timeout")
		}
		return fail("place lookup failed: " + err.Error())
	}
	if place == nil {
		return fail("no place called \"" + asked + "\"")
	}
	if place.Timezone != "" {
		if zone := loadZone(place.Timezone); zone != nil {
			return reply(timeIn(now, zone, place.Label()))
		}
	}
	return fail("no time zone known for " + place.Label())
}

func orDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return LookupTimeout
	}
	return d
}

// zoneName understands an IANA name in any case, or a common US zone abbreviation. "EST" and its
// kin mean the region's current time (daylight saving included), not the fixed offsets IANA gives them.
func zoneName(asked string) *time.Location {
	var region string
	switch strings.ToUpper(asked) {
	case "ET", "EST", "EDT", "EASTERN", "EASTERN TIME":
		region = "America/New_York"
	case "CT", "CST", "CDT", "CENTRAL", "CENTRAL TIME":
		region = "America/Chicago"
	// Denver, not Phoenix: most Mountain-time users have daylight saving (Arizona doesn't).
	case "MT", "MST", "MDT", "MOUNTAIN", "MOUNTAIN TIME":
		region = "America/Denver"
	case "PT", "PST", "PDT", "PACIFIC", "PACIFIC TIME":
		region = "America/Los_Angeles"
	case "AKT", "AKST", "AKDT":
		region = "America/Anchorage"
	case "HT", "HST":
		region = "Pacific/Honolulu"
	}
	if region != "" {
		return loadZone(region)
	}
	return loadZone(asked)
}

// loadZone resolves an IANA name case-insensitively and returns the location under its canonical
// spelling. Go's loader is case-sensitive (and on a case-insensitive filesystem keeps whatever
// case it was given), so the system zoneinfo directory is scanned for the canonical name first;
// without one (or on a miss) the name is tried as given, title-cased, then upper-cased.
func loadZone(name string) *time.Location {
	if name == "" || strings.EqualFold(name, "local") || strings.ContainsAny(name, `\.`) || strings.HasPrefix(name, "/") {
		return nil
	}
	if canon := canonicalZone(name); canon != "" {
		if loc, err := time.LoadLocation(canon); err == nil {
			return loc
		}
	}
	for _, try := range []string{titleZone(name), name, strings.ToUpper(name)} {
		if loc, err := time.LoadLocation(try); err == nil {
			return loc
		}
	}
	return nil
}

func canonicalZone(name string) string {
	var found string
	lower := strings.ToLower(name)
	root, err := filepath.EvalSymlinks("/usr/share/zoneinfo") // a symlink on macOS; WalkDir won't follow it
	if err != nil {
		return ""
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if rel, e := filepath.Rel(root, path); e == nil && !d.IsDir() && strings.ToLower(rel) == lower {
			found = rel
		}
		return nil
	})
	return found
}

// titleZone: "america/new_york" -> "America/New_York", "america/port-au-prince" -> "America/Port-Au-Prince".
func titleZone(name string) string {
	b := []byte(strings.ToLower(name))
	up := true
	for i, c := range b {
		if up && c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
		up = c == '/' || c == '_' || c == '-'
	}
	return string(b)
}

func timeIn(now time.Time, zone *time.Location, place string) map[string]any {
	local := now.In(zone)
	out := map[string]any{
		"time":     local.Format("3:04 PM"),
		"date":     local.Format("Monday, January 2, 2006"),
		"timezone": zone.String(),
	}
	if place != "" {
		out["place"] = place
	}
	return out
}
