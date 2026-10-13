package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// GetWeather is `get_weather`: today's weather somewhere, by default in the user's home city.
type GetWeather struct {
	Places Geocoder
	Sky    WeatherSource
	// Home is Settings > Voice > Home city; used when the user names no place.
	Home string
	// Timeout bounds the whole lookup; 0 means LookupTimeout.
	Timeout time.Duration
}

func (GetWeather) Name() string { return "get_weather" }

func (GetWeather) Description() string {
	return "Today's weather and forecast for a place. Answers in about a second: use it instead of delegate " +
		"for weather questions. Say the summary in your own words."
}

func (GetWeather) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "location": {
      "type": "string",
      "description": "A city, e.g. \"Paris\" or \"Portland, Oregon\". Leave out for the user's home city."
    }
  },
  "additionalProperties": false
}`)
}

func (g GetWeather) Invoke(ctx context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	asked, ok := parseArgs(raw).text("location")
	if !ok {
		asked = g.Home
	}
	if asked == "" {
		return fail("no location given and no home city set: ask the user which city")
	}
	ctx, cancel := context.WithTimeout(ctx, orDefault(g.Timeout))
	defer cancel()
	place, fc, err := g.lookup(ctx, asked)
	switch {
	case err != nil && isTimeout(err):
		return fail("timeout")
	case err != nil:
		return fail("weather lookup failed: " + err.Error())
	case place == nil:
		return fail("no place called \"" + asked + "\"")
	}
	return reply(map[string]any{"summary": Summary(*place, fc)})
}

func (g GetWeather) lookup(ctx context.Context, asked string) (*Place, Forecast, error) {
	place, err := g.Places.Find(ctx, asked)
	if err != nil || place == nil {
		return nil, Forecast{}, err
	}
	fc, err := g.Sky.Forecast(ctx, *place, place.Units())
	return place, fc, err
}

// Summary is one or two sentences the model can say, e.g. "Paris, France: 18°C and light rain.
// Today's high 21°C, low 12°C, 60% chance of rain, wind 12 km/h."
func Summary(p Place, f Forecast) string {
	u := p.Units()
	t, s := u.Temperature(), u.Speed()
	out := fmt.Sprintf("%s: %s%s and %s. Today's high %s%s, low %s%s",
		p.Label(), whole(f.Temperature), t, Conditions(f.Code), whole(f.High), t, whole(f.Low), t)
	if f.PrecipitationChance != nil && *f.PrecipitationChance > 0 {
		kind := "rain"
		if isSnow(f.Code) {
			kind = "snow"
		}
		out += fmt.Sprintf(", %d%% chance of %s", *f.PrecipitationChance, kind)
	}
	return out + fmt.Sprintf(", wind %s %s.", whole(f.Wind), s)
}

// whole rounds to a whole number (half away from zero), never "-0".
func whole(x float64) string {
	r := math.Round(x)
	if r == 0 {
		r = 0
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

func isSnow(code int) bool { return (code >= 71 && code <= 77) || code == 85 || code == 86 }

// Conditions names a WMO weather interpretation code, as Open-Meteo documents them.
func Conditions(code int) string {
	switch code {
	case 0:
		return "clear skies"
	case 1:
		return "mostly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61:
		return "light rain"
	case 63:
		return "rain"
	case 65:
		return "heavy rain"
	case 66, 67:
		return "freezing rain"
	case 71:
		return "light snow"
	case 73:
		return "snow"
	case 75:
		return "heavy snow"
	case 77:
		return "snow grains"
	case 80, 81:
		return "rain showers"
	case 82:
		return "violent rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorms"
	case 96, 99:
		return "thunderstorms with hail"
	}
	return "unsettled weather"
}
