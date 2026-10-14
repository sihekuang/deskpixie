package canvas

import (
	"strings"
	"testing"
)

func TestCspMatchesTheSpecExactly(t *testing.T) {
	want := "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'none'; frame-src 'none'; " +
		"form-action 'none'; base-uri 'none'"
	if CSP != want {
		t.Fatalf("CSP = %q", CSP)
	}
}

func TestCspAllowsNoNetworkAndNoEval(t *testing.T) {
	if strings.Contains(CSP, "http") || strings.Contains(CSP, "unsafe-eval") || strings.Contains(CSP, "*") {
		t.Fatalf("CSP too permissive: %q", CSP)
	}
	for _, d := range []string{"connect-src 'none'", "frame-src 'none'", "form-action 'none'", "base-uri 'none'", "default-src 'none'"} {
		if !strings.Contains(CSP, d) {
			t.Errorf("missing %s", d)
		}
	}
}
