package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPersonaIsBit(t *testing.T) {
	if !strings.Contains(DefaultPersona(), "You are Bit") {
		t.Fatal()
	}
}

func TestInstructionsFallsBack(t *testing.T) {
	if Instructions("") != DefaultPersona() {
		t.Fatal("empty path")
	}
	if Instructions("/nonexistent/x.md") != DefaultPersona() {
		t.Fatal("missing file")
	}
	p := filepath.Join(t.TempDir(), "p.md")
	os.WriteFile(p, []byte("  \n"), 0o600)
	if Instructions(p) != DefaultPersona() {
		t.Fatal("blank file")
	}
	os.WriteFile(p, []byte("Be terse."), 0o600)
	if Instructions(p) != "Be terse." {
		t.Fatal("custom")
	}
}
