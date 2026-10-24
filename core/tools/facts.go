package tools

import "fmt"

// LocalFacts is what the model should know without asking: today's date, the time zone and the
// home city, appended to the instructions when each conversation connects.
type LocalFacts struct {
	Clock Clock
	// Home is Settings > Voice > Home city; "" when unset.
	Home string
}

// Facts implements SessionFacts.
func (f LocalFacts) Facts() string {
	zone := f.Clock.Zone()
	local := f.Clock.Now().In(zone)
	out := fmt.Sprintf("Local facts:\n- This conversation started on %s at %s, time zone %s.",
		local.Format("Monday, January 2, 2006"), local.Format("3:04 PM"), zone.String())
	if f.Home != "" {
		out += fmt.Sprintf("\n- The user's home city is %s.", f.Home)
	}
	out += "\n- For the current time call get_time, for the weather call get_weather, and for any arithmetic " +
		"or unit conversion call calculate, for timers and \"remind me in…\" call timer, and to pause, skip " +
		"or change the volume of music call media (when you have it), and to show where a DeskPixie setting " +
		"is or open Settings call open_settings (when you have it). They answer at once: never delegate " +
		"these.\n- A line starting \"Timer done:\" means one of the user's timers just ended: tell them " +
		"in one short sentence."
	return out
}
