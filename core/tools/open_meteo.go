package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Open-Meteo: free geocoding and forecasts with no API key. Its free tier is for
// non-commercial use; recheck the terms before a commercial release.
const (
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
	// userAgent follows the rename rule (spec 6): weather user agent "deskpixie".
	userAgent = "deskpixie"
)

// OpenMeteo implements Geocoder and WeatherSource. The URLs and Client are fields so tests
// point them at httptest.
type OpenMeteo struct {
	Client       *http.Client
	GeocodingURL string
	ForecastURL  string
}

func NewOpenMeteo() *OpenMeteo {
	return &OpenMeteo{Client: &http.Client{Timeout: LookupTimeout}, GeocodingURL: geocodingURL, ForecastURL: forecastURL}
}

func (o *OpenMeteo) get(ctx context.Context, base string, q url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, LookupFailed("request failed")
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, classify(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, LookupFailed(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classify(err)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, LookupFailed("bad response: " + err.Error())
	}
	return v, nil
}

func classify(err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrTimeout
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return LookupFailed("no connection")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return LookupFailed("request failed")
}

func (o *OpenMeteo) Find(ctx context.Context, query string) (*Place, error) {
	name, qualifier := splitQuery(query)
	if name == "" {
		return nil, nil
	}
	p, err := o.find(ctx, name, qualifier)
	if p != nil || err != nil || qualifier != "" {
		return p, err
	}
	// Nothing for the whole name and no qualifier was recognised: the end may be a region we
	// don't know ("Toronto Ontario", "Bothell Washington State"). Try the shorter names, with the
	// rest as the qualifier, at most twice.
	words := strings.Fields(name)
	for i, tries := len(words)-1, 0; i >= 1 && tries < 2; i, tries = i-1, tries+1 {
		rest := expand(strings.ToLower(strings.Join(words[i:], " ")))
		if p, err := o.find(ctx, strings.Join(words[:i], " "), rest); p != nil || err != nil {
			return p, err
		}
	}
	return nil, nil
}

func (o *OpenMeteo) find(ctx context.Context, name, qualifier string) (*Place, error) {
	count := "1"
	if qualifier != "" {
		count = "10"
	}
	body, err := o.get(ctx, o.GeocodingURL, url.Values{"name": {name}, "count": {count}, "language": {"en"}, "format": {"json"}})
	if err != nil {
		return nil, err
	}
	return bestPlace(body, qualifier), nil
}

func (o *OpenMeteo) Forecast(ctx context.Context, p Place, u Units) (Forecast, error) {
	q := url.Values{
		"latitude":      {strconv.FormatFloat(p.Latitude, 'f', -1, 64)},
		"longitude":     {strconv.FormatFloat(p.Longitude, 'f', -1, 64)},
		"current":       {"temperature_2m,weather_code,wind_speed_10m"},
		"daily":         {"temperature_2m_max,temperature_2m_min,precipitation_probability_max"},
		"forecast_days": {"1"},
		"timezone":      {"auto"},
	}
	if u == Imperial {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	body, err := o.get(ctx, o.ForecastURL, q)
	if err != nil {
		return Forecast{}, err
	}
	f, ok := parseForecast(body)
	if !ok {
		return Forecast{}, LookupFailed("bad response: missing fields")
	}
	return f, nil
}

// splitQuery turns "Portland, Oregon, USA" into ("Portland", "oregon"): Open-Meteo searches names
// only, so the next part after the name narrows the results. A state abbreviation or a country
// alias is spelled out ("ME" -> "maine", "UK" -> "gb"). Without a comma, a known state or country
// at the end is split off the same way. An empty qualifier means none.
func splitQuery(query string) (string, string) {
	parts := strings.Split(query, ",")
	name := strings.TrimSpace(parts[0])
	qualifier := ""
	if len(parts) > 1 {
		if q := strings.TrimSpace(parts[1]); q != "" {
			qualifier = expand(strings.ToLower(q))
		}
		return name, qualifier
	}
	// No comma: a known state or country at the end is the qualifier ("Bothell WA",
	// "Raleigh North Carolina", "London UK"). A single word is always the name.
	words := strings.Fields(name)
	for n := 2; n >= 1; n-- {
		if len(words) <= n {
			continue
		}
		tail := strings.ToLower(strings.Join(words[len(words)-n:], " "))
		if q := expand(tail); q != tail || isKnownState(q) {
			return strings.Join(words[:len(words)-n], " "), q
		}
	}
	return name, qualifier
}

func expand(q string) string {
	if state, ok := usStates[q]; ok {
		return state
	}
	switch q {
	case "usa", "us", "u.s.", "u.s.a.", "united states of america", "america":
		return "us"
	case "uk", "u.k.", "britain", "great britain", "england", "scotland", "wales":
		return "gb"
	}
	return q
}

var usStates = map[string]string{
	"al": "alabama", "ak": "alaska", "az": "arizona", "ar": "arkansas", "ca": "california",
	"co": "colorado", "ct": "connecticut", "de": "delaware", "dc": "district of columbia", "fl": "florida",
	"ga": "georgia", "hi": "hawaii", "id": "idaho", "il": "illinois", "in": "indiana",
	"ia": "iowa", "ks": "kansas", "ky": "kentucky", "la": "louisiana", "me": "maine",
	"md": "maryland", "ma": "massachusetts", "mi": "michigan", "mn": "minnesota", "ms": "mississippi",
	"mo": "missouri", "mt": "montana", "ne": "nebraska", "nv": "nevada", "nh": "new hampshire",
	"nj": "new jersey", "nm": "new mexico", "ny": "new york", "nc": "north carolina", "nd": "north dakota",
	"oh": "ohio", "ok": "oklahoma", "or": "oregon", "pa": "pennsylvania", "ri": "rhode island",
	"sc": "south carolina", "sd": "south dakota", "tn": "tennessee", "tx": "texas", "ut": "utah",
	"vt": "vermont", "va": "virginia", "wa": "washington", "wv": "west virginia", "wi": "wisconsin",
	"wy": "wyoming",
}

func isKnownState(q string) bool {
	for _, s := range usStates {
		if s == q {
			return true
		}
	}
	return false
}

// bestPlace is the first result whose region, country or country code matches qualifier; with no
// qualifier, the first result. A US state or country we know that nothing matches finds no place,
// rather than quietly answering for a different city of the same name. Other qualifiers ("ON",
// "NSW") may be spelled differently in the results, so they fall back to the first result.
func bestPlace(body map[string]any, qualifier string) *Place {
	raw, _ := body["results"].([]any)
	var places []Place
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			if p, ok := parsePlace(m); ok {
				places = append(places, p)
			}
		}
	}
	if len(places) == 0 {
		return nil
	}
	if qualifier == "" {
		return &places[0]
	}
	for i, p := range places {
		for _, f := range []string{p.Region, p.Country, p.CountryCode} {
			if f != "" && strings.ToLower(f) == qualifier {
				return &places[i]
			}
		}
	}
	if qualifier == "us" || qualifier == "gb" || isKnownState(qualifier) {
		return nil
	}
	return &places[0]
}

func parsePlace(v map[string]any) (Place, bool) {
	text := func(k string) string { s, _ := v[k].(string); return s }
	lat, ok1 := v["latitude"].(float64)
	lon, ok2 := v["longitude"].(float64)
	if text("name") == "" || !ok1 || !ok2 {
		return Place{}, false
	}
	return Place{
		Name: text("name"), Region: text("admin1"), Country: text("country"), CountryCode: text("country_code"),
		Latitude: lat, Longitude: lon, Timezone: text("timezone"),
	}, true
}

func parseForecast(body map[string]any) (Forecast, bool) {
	current, ok1 := body["current"].(map[string]any)
	daily, ok2 := body["daily"].(map[string]any)
	if !ok1 || !ok2 {
		return Forecast{}, false
	}
	first := func(k string) (float64, bool) {
		arr, _ := daily[k].([]any)
		if len(arr) == 0 {
			return 0, false
		}
		f, ok := arr[0].(float64)
		return f, ok
	}
	temp, ok1 := current["temperature_2m"].(float64)
	code, ok2 := current["weather_code"].(float64)
	wind, ok3 := current["wind_speed_10m"].(float64)
	high, ok4 := first("temperature_2m_max")
	low, ok5 := first("temperature_2m_min")
	if !(ok1 && ok2 && ok3 && ok4 && ok5) || code < 0 || code > 255 || code != math.Trunc(code) {
		return Forecast{}, false
	}
	f := Forecast{Temperature: temp, Code: int(code), Wind: wind, High: high, Low: low}
	if p, ok := first("precipitation_probability_max"); ok {
		n := int(math.Round(math.Min(100, math.Max(0, p))))
		f.PrecipitationChance = &n
	}
	return f, true
}
