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

// Rust persona.rs tests, by name (TestDefaultPersonaIsBit/TestInstructionsFallsBack above
// already covered the fallback; these pin the exact Rust assertions).

func TestDefaultPersonaMentionsBitAndEndConversation(t *testing.T) {
	p := DefaultPersona()
	if !strings.Contains(p, "Bit") || !strings.Contains(p, "end_conversation") {
		t.Fatal(p)
	}
}

func TestDefaultPersonaNeverGuessesFacts(t *testing.T) {
	p := DefaultPersona()
	if !strings.Contains(p, "Only state facts you are certain of") || !strings.Contains(p, "Never guess") {
		t.Fatal(p)
	}
}

func TestCustomPersonaIsUsed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "persona.md")
	if err := os.WriteFile(p, []byte("You are a pirate.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Instructions(p); got != "You are a pirate." {
		t.Fatalf("%q", got)
	}
}

func TestMissingOrEmptyPersonaFallsBack(t *testing.T) {
	want := strings.TrimSpace(DefaultPersona())
	if Instructions("") != want || Instructions("/definitely/not/here.md") != want {
		t.Fatal("missing")
	}
	p := filepath.Join(t.TempDir(), "persona.md")
	if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Instructions(p) != want {
		t.Fatal("empty")
	}
}
