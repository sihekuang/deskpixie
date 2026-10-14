package tools

import "testing"

func TestUSPlacesAreNamedByStateAndUseImperialUnits(t *testing.T) {
	p := mkPlace("Portland", "Oregon", "United States", "US", "America/Los_Angeles")
	if p.Label() != "Portland, Oregon" || p.Units() != Imperial {
		t.Fatal(p.Label(), p.Units())
	}
}

func TestOtherPlacesAreNamedByCountryAndUseMetricUnits(t *testing.T) {
	p := mkPlace("Paris", "Île-de-France", "France", "FR", "Europe/Paris")
	if p.Label() != "Paris, France" || p.Units() != Metric {
		t.Fatal(p.Label(), p.Units())
	}
}

func TestACityStateIsNotNamedTwice(t *testing.T) {
	if l := mkPlace("Singapore", "Singapore", "Singapore", "SG", "Asia/Singapore").Label(); l != "Singapore" {
		t.Fatal(l)
	}
}
