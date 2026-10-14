package tools

import (
	"fmt"
	"strings"
)

type dimension int

const (
	dimLength dimension = iota
	dimMass
	dimVolume
	dimSpeed
	dimArea
	dimCelsius
	dimFahrenheit
	dimKelvin
)

func (d dimension) isTemperature() bool { return d >= dimCelsius }

func (d dimension) kelvin(x float64) float64 {
	switch d {
	case dimCelsius:
		return x + 273.15
	case dimFahrenheit:
		return (x-32)*5/9 + 273.15
	}
	return x
}

func (d dimension) outOfKelvin(k float64) float64 {
	switch d {
	case dimCelsius:
		return k - 273.15
	case dimFahrenheit:
		return (k-273.15)*9/5 + 32
	}
	return k
}

type unitDef struct {
	spoken  string // plural spoken name
	aliases []string
	dim     dimension
	size    float64 // size in the dimension's base unit; unused for temperatures (formula)
}

var units = []unitDef{
	{"millimeters", []string{"mm", "millimeter", "millimetre", "millimetres"}, dimLength, 0.001},
	{"centimeters", []string{"cm", "centimeter", "centimetre", "centimetres"}, dimLength, 0.01},
	{"meters", []string{"m", "meter", "metre", "metres"}, dimLength, 1},
	{"kilometers", []string{"km", "kilometer", "kilometre", "kilometres", "kms"}, dimLength, 1000},
	{"inches", []string{"in", "inch", "\""}, dimLength, 0.0254},
	{"feet", []string{"ft", "foot", "'"}, dimLength, 0.3048},
	{"yards", []string{"yd", "yard", "yds"}, dimLength, 0.9144},
	{"miles", []string{"mi", "mile"}, dimLength, 1609.344},
	{"nautical miles", []string{"nmi", "nautical mile"}, dimLength, 1852},
	{"milligrams", []string{"mg", "milligram"}, dimMass, 0.001},
	{"grams", []string{"g", "gram", "gramme", "grammes"}, dimMass, 1},
	{"kilograms", []string{"kg", "kilogram", "kilo", "kilos", "kgs"}, dimMass, 1000},
	{"tonnes", []string{"t", "tonne", "metric ton", "metric tons"}, dimMass, 1_000_000},
	{"ounces", []string{"oz", "ounce"}, dimMass, 28.349523125},
	{"pounds", []string{"lb", "lbs", "pound"}, dimMass, 453.59237},
	{"stone", []string{"st", "stones"}, dimMass, 6350.29318},
	{"milliliters", []string{"ml", "milliliter", "millilitre", "millilitres"}, dimVolume, 0.001},
	{"liters", []string{"l", "liter", "litre", "litres"}, dimVolume, 1},
	{"teaspoons", []string{"tsp", "teaspoon"}, dimVolume, 0.00492892159375},
	{"tablespoons", []string{"tbsp", "tablespoon"}, dimVolume, 0.01478676478125},
	{"fluid ounces", []string{"fl oz", "floz", "fluid ounce"}, dimVolume, 0.0295735295625},
	{"cups", []string{"cup"}, dimVolume, 0.2365882365},
	{"pints", []string{"pt", "pint"}, dimVolume, 0.473176473},
	{"quarts", []string{"qt", "quart"}, dimVolume, 0.946352946},
	{"gallons", []string{"gal", "gallon"}, dimVolume, 3.785411784},
	{"meters per second", []string{"m/s", "mps", "meter per second"}, dimSpeed, 1},
	{"kilometers per hour", []string{"km/h", "kph", "kmh", "kilometer per hour", "kilometres per hour"}, dimSpeed, 1000.0 / 3600.0},
	{"miles per hour", []string{"mph", "mile per hour"}, dimSpeed, 0.44704},
	{"knots", []string{"kn", "kt", "knot"}, dimSpeed, 1852.0 / 3600.0},
	{"square meters", []string{"m2", "m²", "sq m", "square meter", "square metre", "square metres"}, dimArea, 1},
	{"square feet", []string{"ft2", "ft²", "sq ft", "square foot"}, dimArea, 0.09290304},
	{"acres", []string{"acre", "ac"}, dimArea, 4046.8564224},
	{"hectares", []string{"ha", "hectare"}, dimArea, 10_000},
	{"degrees Celsius", []string{"c", "°c", "celsius", "centigrade", "degrees c", "degree celsius"}, dimCelsius, 0},
	{"degrees Fahrenheit", []string{"f", "°f", "fahrenheit", "degrees f", "degree fahrenheit"}, dimFahrenheit, 0},
	{"kelvin", []string{"k", "kelvins"}, dimKelvin, 0},
}

func findUnit(name string) *unitDef {
	n := strings.ToLower(strings.TrimSpace(name))
	if rest, ok := strings.CutPrefix(n, "degrees "); ok && !strings.HasPrefix(rest, "c") && !strings.HasPrefix(rest, "f") {
		n = rest
	}
	for i := range units {
		u := &units[i]
		if strings.ToLower(u.spoken) == n {
			return u
		}
		for _, a := range u.aliases {
			if a == n {
				return u
			}
		}
	}
	return nil
}

// Convert converts value in from to the to unit, returning the amount and both spoken (plural) names.
func Convert(value float64, from, to string) (float64, string, string, error) {
	a, b := findUnit(from), findUnit(to)
	if a == nil || b == nil {
		unknown := to
		if a == nil {
			unknown = from
		}
		return 0, "", "", fmt.Errorf("unknown unit \"%s\"", unknown)
	}
	var x float64
	switch {
	case a.dim.isTemperature() && b.dim.isTemperature():
		x = b.dim.outOfKelvin(a.dim.kelvin(value))
	case a.dim == b.dim:
		x = value * a.size / b.size
	default:
		hint := ""
		if a.spoken == "ounces" || b.spoken == "ounces" {
			hint = " (for volume, say fl oz)"
		}
		return 0, "", "", fmt.Errorf("can't convert %s to %s%s", a.spoken, b.spoken, hint)
	}
	x, err := finite(x)
	if err != nil {
		return 0, "", "", err
	}
	return x, a.spoken, b.spoken, nil
}

// named is "1 mile", "2 miles": the unit's name to follow amount.
func named(amount, plural string) string {
	var singular string
	switch {
	case plural == "feet":
		singular = "foot"
	case plural == "square feet":
		singular = "square foot"
	case plural == "inches":
		singular = "inch"
	case plural == "stone" || plural == "kelvin":
		singular = plural
	case strings.HasPrefix(plural, "degrees "):
		if amount == "1" {
			return strings.Replace(plural, "degrees", "degree", 1)
		}
		return plural
	default:
		head, _, hasPer := strings.Cut(plural, " per ")
		if !hasPer {
			head = plural
		}
		singular = strings.TrimSuffix(head, "s")
	}
	if amount != "1" {
		return plural
	}
	if _, rest, ok := strings.Cut(plural, " per "); ok {
		return singular + " per " + rest
	}
	return singular
}
