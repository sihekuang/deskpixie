package gemini

// The 15th case of ai-mascot crates/voice-gemini/src/session_tests.rs
// (passes_voice_core_conformance_against_a_scripted_gemini), against the fake Gemini in
// session_test.go. Uses the conformance suite P2 ported to core/voice/voicetest.

import (
	"encoding/json"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/voicetest"
)

func TestPassesVoiceCoreConformanceAgainstAScriptedGemini(t *testing.T) {
	url, sn := fakeGemini(t, []reply{{chunks: 3}, {chunks: 40}, {chunks: 1, tool: "end_conversation"}}, false)
	c := cfg()
	c.Tools = []voice.ToolSpec{{Name: "end_conversation", Description: "End it.",
		Parameters: json.RawMessage(`{ "type": "object", "properties": {}, "additionalProperties": false }`)}}
	frames := func(n int, f func() []int16) [][]int16 {
		out := make([][]int16, n)
		for i := range out {
			out[i] = f()
		}
		return out
	}
	loud := func() []int16 { return voicetest.Tone(voicetest.FrameMs, 0.3) }
	clips := voicetest.Clips{
		Question: frames(5, loud),
		Goodbye:  frames(5, loud),
		Silence:  frames(8, func() []int16 { return voicetest.Silence(voicetest.FrameMs) }),
	}
	report, err := voicetest.Run(New(url), "k", c, clips)
	if err != nil {
		t.Fatalf("conforms: %v", err)
	}
	if len(report.Steps) != 5 {
		t.Fatalf("%q", report.Steps)
	}
	if len(report.Transcripts) == 0 || report.Transcripts[0] != "Four." {
		t.Fatalf("%q", report.Transcripts)
	}
	// The tool result went back (poll: the socket may still be delivering it).
	jsonEq(t, waitFor(t, sn, "toolResponse"),
		`{ "functionResponses": [{ "id": "fc_1", "name": "end_conversation", "response": { "output": { "ok": true } } }] }`)
	msgs, _ := sn.snapshot()
	if got := at(msgs[0], "setup", "tools", 0, "functionDeclarations", 0, "name"); got != "end_conversation" {
		t.Fatal(got)
	}
}
