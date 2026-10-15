package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPersonaIsPixie(t *testing.T) {
	if !strings.Contains(DefaultPersona(), "You are Pixie") {
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

// Rust persona.rs tests, by name (TestDefaultPersonaIsPixie/TestInstructionsFallsBack above
// already covered the fallback; these pin the exact Rust assertions).

func TestDefaultPersonaMentionsPixieAndEndConversation(t *testing.T) {
	p := DefaultPersona()
	if !strings.Contains(p, "Pixie") || !strings.Contains(p, "end_conversation") {
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

// #72: the knowledge boundary. Facts about the user's world and anything current come from a
// tool or the coordinator, never from memory; general knowledge and chat stay with Pixie.
func TestDefaultPersonaDrawsTheKnowledgeBoundary(t *testing.T) {
	p := DefaultPersona()
	for _, phrase := range []string{
		"come only from a tool or the coordinator, never from memory",
		"files, repositories, builds, jobs and workers",
		"news, prices, scores, software versions and today's events",
		"If you aren't certain, delegate rather than guess",
		"General knowledge, small talk",
	} {
		if !strings.Contains(p, phrase) {
			t.Errorf("the persona doesn't say %q", phrase)
		}
	}
	// The old rule only said "say so" when unsure; with a delegate tool that now conflicts.
	if strings.Contains(p, "If you aren't certain and have no delegate tool to check with, say so") {
		t.Error("the old unsure rule is still there")
	}
}

// #72: Pixie never claims an action it didn't take, and knows what a correction means.
func TestDefaultPersonaExplainsClaimCorrections(t *testing.T) {
	p := DefaultPersona()
	prefix := strings.TrimSuffix(CorrectionPrefix, " ")
	if !strings.Contains(p, `Lines starting "`+prefix+`"`) {
		t.Errorf("the persona doesn't explain %q lines", prefix)
	}
	if !strings.Contains(p, "Never say you did something") {
		t.Error("the persona doesn't forbid claiming undone actions")
	}
}
