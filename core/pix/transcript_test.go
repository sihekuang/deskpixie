package pix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResumedDescriptionIsYourLastPrompt(t *testing.T) {
	p := writeTranscript(t,
		`{"type":"ai-title","aiTitle":"Superpowers handoff"}`,
		`{"type":"user","message":{"role":"user","content":"Please add a stop-loss to the arbitrage bot"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"user","isMeta":true,"origin":{"kind":"peer"},"message":{"role":"user","content":"Task from the coordinator: x"}}`,
		`{"type":"user","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>done"}}`,
		`not json`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
		// Shapes seen in real transcripts (Claude Code 2.1.300).
		`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","message":{"role":"user","content":"/compact"}}`,
		`{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"This session is being continued from a previous conversation"}}`,
	)
	if got := ResumedDescription(p); got != "add a stop-loss to the arbitrage bot" {
		t.Fatalf("%q", got)
	}
}

func TestResumedDescriptionTakesTypedPrompts(t *testing.T) {
	p := writeTranscript(t,
		`{"type":"user","origin":{"kind":"human"},"promptSource":"typed","message":{"role":"user","content":"restart the traders for me"}}`,
		`{"type":"user","isMeta":true,"origin":{"kind":"peer"},"promptSource":"system","message":{"role":"user","content":"Another Claude session sent a message:\nTask from the coordinator"}}`,
		`{"type":"user","origin":{"kind":"human"},"promptSource":"typed","message":{"role":"user","content":"got it"}}`,
	)
	if got := ResumedDescription(p); got != "restart the traders for me" {
		t.Fatalf("%q", got)
	}
}

func TestResumedDescriptionFallsBackToTitle(t *testing.T) {
	p := writeTranscript(t, `{"type":"ai-title","aiTitle":"DeskPixie port design spec"}`)
	if got := ResumedDescription(p); got != "DeskPixie port design spec" {
		t.Fatalf("%q", got)
	}
	if got := ResumedDescription(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestSessionStartOnResumeDescribesIt(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	p := writeTranscript(t, `{"type":"user","message":{"role":"user","content":"fix the flaky upload test"}}`)
	h.s.Hook("SessionStart", map[string]any{"source": "resume", "transcript_path": p, "session_id": "s-1"})
	waitEvent(t, h.d.events, "resumed description", func(m wire.Msg) bool {
		return m.Event == "description" && m.Source == "auto" && m.Description == "fix the flaky upload test"
	})
	h.s.runner.Hangup()
	h.s.finish()
}
