package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func portlands() map[string]any {
	var v map[string]any
	_ = json.Unmarshal([]byte(`{"results":[
	 {"name":"Portland","latitude":45.52,"longitude":-122.68,"country_code":"US","timezone":"America/Los_Angeles","country":"United States","admin1":"Oregon"},
	 {"name":"Portland","latitude":43.66,"longitude":-70.26,"country_code":"US","timezone":"America/New_York","country":"United States","admin1":"Maine"}]}`), &v)
	return v
}

func TestAQualifierIsSplitOffTheName(t *testing.T) {
	for _, c := range []struct{ in, name, q string }{
		{"Portland, Maine", "Portland", "maine"},
		{"Portland, Oregon, USA", "Portland", "oregon"}, // only the part after the name
		{"  Paris ", "Paris", ""},
		{"Paris,", "Paris", ""},
	} {
		n, q := splitQuery(c.in)
		if n != c.name || q != c.q {
			t.Errorf("%q -> %q,%q want %q,%q", c.in, n, q, c.name, c.q)
		}
	}
}

func TestStateCodesAndCountryAliasesAreSpelledOut(t *testing.T) {
	for in, want := range map[string]string{"Portland, ME": "maine", "Springfield, IL": "illinois", "London, UK": "gb", "Paris, USA": "us"} {
		if _, q := splitQuery(in); q != want {
			t.Errorf("%q -> %q want %q", in, q, want)
		}
	}
}

func TestTheQualifierPicksAmongSameNamedPlaces(t *testing.T) {
	p := bestPlace(portlands(), "maine")
	if p == nil || p.Region != "Maine" || p.Timezone != "America/New_York" {
		t.Fatal(p)
	}
	p = bestPlace(portlands(), "us")
	if p == nil || p.Region != "Oregon" { // a country code matches too; the first such result wins
		t.Fatal(p)
	}
}

func TestAKnownButUnmatchedQualifierFindsNoPlaceRatherThanTheWrongOne(t *testing.T) {
	_, q := splitQuery("Portland, TX")
	if bestPlace(portlands(), q) != nil {
		t.Error("known state that doesn't match")
	}
	_, q = splitQuery("Portland, UK")
	if bestPlace(portlands(), q) != nil {
		t.Error("known country that doesn't match")
	}
	if p := bestPlace(portlands(), "on"); p == nil || p.Region != "Oregon" {
		t.Error("an unknown qualifier falls back")
	}
	_, q = splitQuery("Portland, OR")
	if p := bestPlace(portlands(), q); p == nil || p.Region != "Oregon" {
		t.Error("OR")
	}
	if p := bestPlace(portlands(), ""); p == nil || p.Latitude != 45.52 {
		t.Error("no qualifier")
	}
}

func TestNoResultsIsNoPlace(t *testing.T) {
	if bestPlace(map[string]any{"generationtime_ms": 0.5}, "") != nil {
		t.Error("missing results")
	}
	if bestPlace(map[string]any{"results": []any{}}, "x") != nil {
		t.Error("empty results")
	}
}

func TestAForecastIsReadFromCurrentAndDaily(t *testing.T) {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{
	 "current":{"time":"2026-10-11T15:45","temperature_2m":17.6,"weather_code":61,"wind_speed_10m":11.8},
	 "daily":{"time":["2026-10-11"],"temperature_2m_max":[21.2],"temperature_2m_min":[11.6],"precipitation_probability_max":[60]}}`), &body)
	got, ok := parseForecast(body)
	if !ok || !reflect.DeepEqual(got, rainy()) {
		t.Fatalf("%+v %v", got, ok)
	}
}

func TestAForecastWithoutRainOddsStillReadsAndMissingFieldsDoNot(t *testing.T) {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{
	 "current":{"temperature_2m":5.0,"weather_code":3,"wind_speed_10m":2.0},
	 "daily":{"temperature_2m_max":[7.0],"temperature_2m_min":[1.0],"precipitation_probability_max":[null]}}`), &body)
	f, ok := parseForecast(body)
	if !ok || f.PrecipitationChance != nil {
		t.Fatalf("%+v %v", f, ok)
	}
	if _, ok := parseForecast(map[string]any{"current": map[string]any{}}); ok {
		t.Fatal("missing fields parsed")
	}
}

// Extra (no Rust twin): the HTTP adapter end to end against httptest, no network.
func TestOpenMeteoOverHTTP(t *testing.T) {
	var sawUA, sawUnits string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUA = r.UserAgent()
		switch r.URL.Path {
		case "/geo":
			_ = json.NewEncoder(w).Encode(portlands())
		case "/fc":
			sawUnits = r.URL.Query().Get("temperature_unit")
			_, _ = w.Write([]byte(`{"current":{"temperature_2m":60,"weather_code":3,"wind_speed_10m":5},"daily":{"temperature_2m_max":[70],"temperature_2m_min":[50],"precipitation_probability_max":[10]}}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	om := NewOpenMeteo()
	om.GeocodingURL, om.ForecastURL = srv.URL+"/geo", srv.URL+"/fc"
	p, err := om.Find(context.Background(), "Portland, Maine")
	if err != nil || p == nil || p.Region != "Maine" {
		t.Fatal(p, err)
	}
	f, err := om.Forecast(context.Background(), *p, Imperial)
	if err != nil || f.Temperature != 60 || sawUnits != "fahrenheit" || sawUA != "deskpixie" {
		t.Fatal(f, err, sawUnits, sawUA)
	}
	om.ForecastURL = srv.URL + "/nope"
	if _, err := om.Forecast(context.Background(), *p, Metric); err == nil || err.Error() != "HTTP 500" {
		t.Fatal(err)
	}
}
