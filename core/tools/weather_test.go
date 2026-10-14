package tools

import (
	"strings"
	"testing"
	"time"
)

func ip(n int) *int { return &n }

func rainy() Forecast {
	return Forecast{Temperature: 17.6, Code: 61, Wind: 11.8, High: 21.2, Low: 11.6, PrecipitationChance: ip(60)}
}

func paris() Place { return mkPlace("Paris", "Île-de-France", "France", "FR", "Europe/Paris") }
func portland() Place {
	return mkPlace("Portland", "Oregon", "United States", "US", "America/Los_Angeles")
}

func TestANamedPlaceGetsASpokenSummary(t *testing.T) {
	w := GetWeather{Places: atlasWith(paris()), Sky: &fakeSky{forecast: rainy()}}
	eq(t, call(t, w, `{"location":"Paris"}`), `{"summary":"Paris, France: 18°C and light rain. Today's high 21°C, low 12°C, 60% chance of rain, wind 12 km/h."}`)
}

func TestNoPlaceMeansTheHomeCityInItsOwnUnits(t *testing.T) {
	sky := &fakeSky{forecast: rainy()}
	atlas := atlasWith(portland())
	w := GetWeather{Places: atlas, Sky: sky, Home: "Portland"}
	out := call(t, w, `{}`)
	if s, _ := out["summary"].(string); !strings.HasPrefix(s, "Portland, Oregon: 18°F") {
		t.Fatal(out)
	}
	if len(sky.asked) != 1 || sky.asked[0] != (skyAsk{"Portland", Imperial}) {
		t.Fatal(sky.asked)
	}
	if len(atlas.asked) != 1 || atlas.asked[0] != "Portland" {
		t.Fatal(atlas.asked)
	}
}

func TestNoPlaceAndNoHomeCityAsksTheUser(t *testing.T) {
	w := GetWeather{Places: atlasWith(), Sky: &fakeSky{forecast: rainy()}}
	eq(t, call(t, w, `{"location":""}`), `{"error":"no location given and no home city set: ask the user which city"}`)
}

func TestWeatherAnUnknownPlaceSaysSo(t *testing.T) {
	w := GetWeather{Places: atlasWith(), Sky: &fakeSky{forecast: rainy()}}
	eq(t, call(t, w, `{"location":"Atlantis"}`), `{"error":"no place called \"Atlantis\""}`)
}

func TestWeatherAFailedLookupIsReported(t *testing.T) {
	a := atlasWith()
	a.fail = true
	w := GetWeather{Places: a, Sky: &fakeSky{forecast: rainy()}, Home: "Paris"}
	eq(t, call(t, w, `{}`), `{"error":"weather lookup failed: HTTP 500"}`)
}

func TestASlowForecastTimesOut(t *testing.T) {
	w := GetWeather{Places: atlasWith(paris()), Sky: &fakeSky{forecast: rainy(), delay: 10 * time.Second}, Timeout: 20 * time.Millisecond}
	eq(t, call(t, w, `{"location":"Paris"}`), `{"error":"timeout"}`)
}

func TestADryDayLeavesOutTheChanceOfRainAndSnowSaysSnow(t *testing.T) {
	dry := rainy()
	dry.PrecipitationChance, dry.Code = ip(0), 0
	if got := Summary(paris(), dry); got != "Paris, France: 18°C and clear skies. Today's high 21°C, low 12°C, wind 12 km/h." {
		t.Fatal(got)
	}
	snowy := rainy()
	snowy.Temperature, snowy.Code, snowy.PrecipitationChance = -0.3, 73, ip(80)
	s := Summary(paris(), snowy)
	if !strings.Contains(s, "0°C and snow.") || !strings.Contains(s, "80% chance of snow") {
		t.Fatal(s)
	}
	cold := rainy()
	cold.Temperature, cold.Low = -10.4, -12
	if s := Summary(paris(), cold); !strings.HasPrefix(s, "Paris, France: -10°C") {
		t.Fatal(s)
	}
}
