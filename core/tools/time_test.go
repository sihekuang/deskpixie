package tools

import (
	"testing"
	"time"
)

// 2026-10-11 22:42 UTC: 3:42 PM in Los Angeles, already Monday in Tokyo.
func timeTool(a *fakeAtlas) GetTime {
	return GetTime{
		Clock:  fixedClock(time.Date(2026, 10, 11, 22, 42, 5, 0, time.UTC), "America/Los_Angeles"),
		Places: a,
	}
}

func TestNoPlaceIsTheUsersOwnTime(t *testing.T) {
	eq(t, call(t, timeTool(atlasWith()), `{}`), `{"time":"3:42 PM","date":"Sunday, October 11, 2026","timezone":"America/Los_Angeles"}`)
}

func TestATimeZoneNameNeedsNoLookup(t *testing.T) {
	a := atlasWith()
	out := call(t, timeTool(a), `{"place":"Asia/Tokyo"}`)
	if out["time"] != "7:42 AM" || out["date"] != "Monday, October 12, 2026" {
		t.Fatal(out)
	}
	if len(a.asked) != 0 {
		t.Fatal("looked up", a.asked)
	}
}

func TestZoneNamesIgnoreCaseAndUSAbbreviationsFollowDaylightSaving(t *testing.T) {
	tl := timeTool(atlasWith())
	if call(t, tl, `{"place":"asia/tokyo"}`)["timezone"] != "Asia/Tokyo" {
		t.Fatal("case")
	}
	est := call(t, tl, `{"place":"EST"}`)
	if est["time"] != "6:42 PM" || est["timezone"] != "America/New_York" {
		t.Fatal(est) // EDT in October
	}
	if call(t, tl, `{"place":"pacific time"}`)["time"] != "3:42 PM" {
		t.Fatal("pacific")
	}
}

func TestACityIsLookedUpForItsTimeZone(t *testing.T) {
	a := atlasWith(mkPlace("Paris", "Île-de-France", "France", "FR", "Europe/Paris"))
	eq(t, call(t, timeTool(a), `{"place":"paris"}`), `{"time":"12:42 AM","date":"Monday, October 12, 2026","timezone":"Europe/Paris","place":"Paris, France"}`)
}

func TestTimeAnUnknownPlaceSaysSo(t *testing.T) {
	eq(t, call(t, timeTool(atlasWith()), `{"place":"Atlantis"}`), `{"error":"no place called \"Atlantis\""}`)
}

func TestABlankPlaceIsTheUsersOwnTime(t *testing.T) {
	if call(t, timeTool(atlasWith()), `{"place":"  "}`)["timezone"] != "America/Los_Angeles" {
		t.Fatal("blank")
	}
}

func TestTimeAFailedLookupIsReported(t *testing.T) {
	a := atlasWith()
	a.fail = true
	eq(t, call(t, timeTool(a), `{"place":"Paris"}`), `{"error":"place lookup failed: HTTP 500"}`)
}

func TestASlowLookupTimesOut(t *testing.T) {
	a := atlasWith(mkPlace("Paris", "x", "France", "FR", "Europe/Paris"))
	a.delay = 10 * time.Second
	tl := timeTool(a)
	tl.Timeout = 20 * time.Millisecond
	eq(t, call(t, tl, `{"place":"Paris"}`), `{"error":"timeout"}`)
}
