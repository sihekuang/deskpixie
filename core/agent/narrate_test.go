package agent

import (
	"strings"
	"testing"
)

// Ported from crates/agent-core/src/narrate.rs (6 tests).

func phraseOr(t *testing.T, tool string, input map[string]any) (string, bool) {
	t.Helper()
	return Phrase(tool, input)
}

func TestBashUsesItsDescription(t *testing.T) {
	if p, ok := phraseOr(t, "Bash", map[string]any{"command": "cargo test", "description": "Run the tests"}); !ok || p != "run the tests" {
		t.Fatal(p)
	}
	if p, ok := phraseOr(t, "Bash", map[string]any{"command": "ls"}); !ok || p != "running a command" {
		t.Fatal(p)
	}
}

func TestEditsNameTheFileOnly(t *testing.T) {
	if p, _ := phraseOr(t, "Edit", map[string]any{"file_path": "/Users/x/focus/src/upload.rs"}); p != "editing upload.rs" {
		t.Fatal(p)
	}
	if p, _ := phraseOr(t, "Write", map[string]any{"file_path": "notes.md"}); p != "editing notes.md" {
		t.Fatal(p)
	}
	if _, ok := phraseOr(t, "Edit", map[string]any{}); ok {
		t.Fatal("expected none")
	}
}

func TestStartWorkerSpeaksOnlyTheLastPathComponent(t *testing.T) {
	p := func(repo string) string {
		s, ok := Phrase("mcp__mascot__start_worker", map[string]any{"repo": repo})
		if !ok {
			t.Fatalf("%q: none", repo)
		}
		return s
	}
	if got := p("/Users/x/projects/focus"); got != "starting a worker in focus" {
		t.Fatal(got)
	}
	if got := p("focus/"); got != "starting a worker in focus" {
		t.Fatal(got)
	}
	if got := p(""); got != "starting a worker" {
		t.Fatal(got)
	}
}

func TestOtherToolsAreSilent(t *testing.T) {
	if _, ok := Phrase("Read", map[string]any{"file_path": "a"}); ok {
		t.Fatal("Read")
	}
	if _, ok := Phrase("Grep", map[string]any{}); ok {
		t.Fatal("Grep")
	}
}

func TestPhrasesAreShortAndClean(t *testing.T) {
	d := "Run `cargo test` for **every** crate in the workspace and then collect all the failures"
	p, _ := Phrase("Bash", map[string]any{"description": d})
	if len(strings.Fields(p)) != 12 || strings.ContainsAny(p, "`*") {
		t.Fatal(p)
	}
}

func TestNarrationIsGatedToOncePer30SecondsPerJob(t *testing.T) {
	n := NewNarrator(NarrateIntervalMs)
	a, b := JobIDFromBits(1), JobIDFromBits(2)
	if !n.Allow(a, 0) {
		t.Fatal("first")
	}
	if n.Allow(a, 29_999) {
		t.Fatal("within interval")
	}
	if !n.Allow(b, 29_999) {
		t.Fatal("per job")
	}
	if !n.Allow(a, 30_000) {
		t.Fatal("after interval")
	}
	n.Forget(a)
	if !n.Allow(a, 30_001) {
		t.Fatal("after forget")
	}
}
