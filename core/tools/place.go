package tools

import (
	"context"
	"errors"
)

// Place is a geocoded location. Empty strings mean unknown.
type Place struct {
	Name string
	// Region is the state, province or region (admin1).
	Region  string
	Country string
	// CountryCode is ISO 3166-1 alpha-2, e.g. "US".
	CountryCode string
	Latitude    float64
	Longitude   float64
	// Timezone is an IANA name, e.g. "Europe/Paris".
	Timezone string
}

// Label is how the model names the place: "Portland, Oregon" in the US, "Paris, France" elsewhere.
func (p Place) Label() string {
	second := p.Country
	if p.CountryCode == "US" {
		second = p.Region
	}
	if second != "" && second != p.Name {
		return p.Name + ", " + second
	}
	return p.Name
}

// Units are Fahrenheit and mph where people use them; Celsius and km/h elsewhere.
type Units int

const (
	Metric Units = iota
	Imperial
)

// Units picks the units people at this place use.
func (p Place) Units() Units {
	switch p.CountryCode {
	case "US", "LR", "MM", "BS", "BZ", "KY", "PW":
		return Imperial
	}
	return Metric
}

func (u Units) Temperature() string {
	if u == Imperial {
		return "°F"
	}
	return "°C"
}

func (u Units) Speed() string {
	if u == Imperial {
		return "mph"
	}
	return "km/h"
}

// Forecast is today's weather at a place, in the units it was asked for.
type Forecast struct {
	Temperature float64
	// Code is the WMO weather interpretation code.
	Code int
	Wind float64
	High float64
	Low  float64
	// PrecipitationChance is the chance of rain or snow today in percent; nil when unknown.
	PrecipitationChance *int
}

// ErrTimeout is a lookup that ran out of time.
var ErrTimeout = errors.New("timeout")

type lookupFailed string

func (e lookupFailed) Error() string { return string(e) }

// LookupFailed is a lookup that failed for a reason the model can be told.
func LookupFailed(why string) error { return lookupFailed(why) }

func isTimeout(err error) bool {
	return errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded)
}

// Geocoder finds a place by name.
type Geocoder interface {
	// Find returns the best match for "Paris" or "Portland, Oregon"; (nil, nil) if nothing matches.
	Find(ctx context.Context, query string) (*Place, error)
}

// WeatherSource fetches today's forecast.
type WeatherSource interface {
	Forecast(ctx context.Context, p Place, u Units) (Forecast, error)
}
