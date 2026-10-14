package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestPercentagesAndDivisionAreExact(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"expression":"0.18 * 64"}`), `{"result":"11.52"}`)
	eq(t, call(t, c, `{"expression":"7 / 2"}`), `{"result":"3.5"}`) // integers divide as decimals
	eq(t, call(t, c, `{"expression":"1234 * 5678"}`), `{"result":"7006652"}`)
}

func TestFunctionsConstantsAndTypographicOperatorsWork(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"expression":"sqrt(2)"}`), `{"result":"1.41421"}`)
	eq(t, call(t, c, `{"expression":"2 ^ 10 - round(2.6)"}`), `{"result":"1021"}`)
	eq(t, call(t, c, `{"expression":"12 × 3 ÷ 4 − 1"}`), `{"result":"8"}`)
	eq(t, call(t, c, `{"expression":"pi * 2"}`), `{"result":"6.28319"}`)
	eq(t, call(t, c, `{"expression":".5 + 1.5e3"}`), `{"result":"1500.5"}`)
	eq(t, call(t, c, `{"expression":"max(3, 9, 4) % 4"}`), `{"result":"1"}`)
}

func TestBadInputIsAnErrorNotAGuess(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"expression":"1 / 0"}`), `{"error":"the result is infinite (division by zero?)"}`)
	eq(t, call(t, c, `{"expression":"sqrt(-1)"}`), `{"error":"the result is not a real number"}`)
	if e := errOf(call(t, c, `{"expression":"2 +"}`)); !strings.HasPrefix(e, "can't evaluate") {
		t.Fatalf("got %q", e)
	}
	long := strings.Repeat("1+", 150) + "1"
	eq(t, call(t, c, fmt.Sprintf(`{"expression":%q}`, long)), `{"error":"expression too long"}`)
	eq(t, call(t, c, `{}`), `{"error":"pass an expression, or value with from and to"}`)
}

func TestStringsAssignmentAndChainingAreRefused(t *testing.T) {
	c := Calculate{}
	doubling := `a="xy";` + strings.Repeat("a+=a;", 20) + "len(a)"
	got := call(t, c, fmt.Sprintf(`{"expression":%q}`, doubling))
	eq(t, got, `{"error":"can't evaluate: \"=\" isn't arithmetic"}`)
	if e := errOf(call(t, c, `{"expression":"a + 1"}`)); !strings.HasPrefix(e, "can't evaluate") {
		t.Fatalf("unknown names are errors, got %q", e)
	}
	if _, err := Evaluate("x = 2"); err == nil {
		t.Fatal("assignment accepted")
	}
	if _, err := Evaluate("1; 2"); err == nil {
		t.Fatal("chaining accepted")
	}
}

func TestUnitsConvertWithinADimension(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"value":10,"from":"miles","to":"km"}`), `{"result":"10 miles is 16.0934 kilometers"}`)
	eq(t, call(t, c, `{"value":2,"from":"cups","to":"ml"}`), `{"result":"2 cups is 473.176 milliliters"}`)
	eq(t, call(t, c, `{"value":150,"from":"lbs","to":"Kilograms"}`), `{"result":"150 pounds is 68.0389 kilograms"}`)
	eq(t, call(t, c, `{"value":60,"from":"mph","to":"km/h"}`), `{"result":"60 miles per hour is 96.5606 kilometers per hour"}`)
}

func TestTemperaturesConvertByFormula(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"value":72,"from":"°F","to":"celsius"}`), `{"result":"72 degrees Fahrenheit is 22.2222 degrees Celsius"}`)
	eq(t, call(t, c, `{"value":-40,"from":"C","to":"degrees fahrenheit"}`), `{"result":"-40 degrees Celsius is -40 degrees Fahrenheit"}`)
	eq(t, call(t, c, `{"value":0,"from":"kelvin","to":"c"}`), `{"result":"0 kelvin is -273.15 degrees Celsius"}`)
}

func TestOneOfAUnitIsSingular(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"value":1,"from":"mile","to":"feet"}`), `{"result":"1 mile is 5280 feet"}`)
	eq(t, call(t, c, `{"value":1,"from":"m/s","to":"km/h"}`), `{"result":"1 meter per second is 3.6 kilometers per hour"}`)
	eq(t, call(t, c, `{"value":33.8,"from":"f","to":"c"}`), `{"result":"33.8 degrees Fahrenheit is 1 degree Celsius"}`)
	eq(t, call(t, c, `{"value":12,"from":"inches","to":"ft"}`), `{"result":"12 inches is 1 foot"}`)
	eq(t, call(t, c, `{"value":0.09290304,"from":"m2","to":"sq ft"}`), `{"result":"0.092903 square meters is 1 square foot"}`)
}

func TestMismatchedOrUnknownUnitsAreErrors(t *testing.T) {
	c := Calculate{}
	eq(t, call(t, c, `{"value":1,"from":"kg","to":"miles"}`), `{"error":"can't convert kilograms to miles"}`)
	eq(t, call(t, c, `{"value":8,"from":"oz","to":"ml"}`), `{"error":"can't convert ounces to milliliters (for volume, say fl oz)"}`)
	eq(t, call(t, c, `{"value":1e308,"from":"miles","to":"mm"}`), `{"error":"the result is infinite (division by zero?)"}`)
	eq(t, call(t, c, `{"value":1,"from":"kg","to":"smoots"}`), `{"error":"unknown unit \"smoots\""}`)
}

func TestSpokenNumbersAreShort(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{{2, "2"}, {-0.0000001, "-0.0000001"}, {-1e-13, "0"}, {1234567, "1234567"}, {0.333333333, "0.333333"}} {
		if got := Spoken(c.in); got != c.want {
			t.Errorf("Spoken(%v)=%q want %q", c.in, got, c.want)
		}
	}
}

// Extra (no Rust twin): evalexpr-style precedence the Rust tests only exercise implicitly.
func TestPrecedenceAndUnary(t *testing.T) {
	for in, want := range map[string]float64{"2 + 3 * 4": 14, "(2 + 3) * 4": 20, "-2 * 3": -6, "2 ^ -1": 0.5, "10 % 4 + 1": 3, "min(4, 2, 8)": 2} {
		got, err := Evaluate(in)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", in, got, err, want)
		}
	}
}
