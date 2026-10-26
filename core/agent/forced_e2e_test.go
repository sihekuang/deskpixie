package agent_test

import (
	"strings"
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

// #340 end to end: what pix types into the session (fakeclaude under the real pix) for each
// forced path, with agent.markForcedSends on and off, and for an ordinary message.

func forcedRig(t *testing.T, mark bool) (*pixSession, *AgentHandle) {
	p := startPixSession(t)
	cfg := config(false)
	cfg.Settings.MarkForcedSends = mark
	h, _ := pixRuntime(t, p, cfg, nil)
	return p, h
}

// lastPrompt waits for the session to have n prompts typed, and returns the last.
func (p *pixSession) lastPrompt(n int) string {
	p.t.Helper()
	p.waitFor("prompt typed", func() bool { return len(p.prompts()) >= n })
	got := p.prompts()
	if len(got) != n {
		p.t.Fatalf("prompts %q, want %d", got, n)
	}
	return got[n-1]
}

func TestStopAndSendTypesItsNoteIntoTheSession(t *testing.T) {
	for _, mark := range []bool{true, false} {
		p, h := forcedRig(t, mark)
		p.step("work") // busy with a prompt of your own
		if v := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: "check the tests"}); v["status"] != "waiting" || v["reason"] != "working" {
			t.Fatalf("message: %v", v)
		}
		if v := sessionCall(t, h, McpCall{Kind: CallStopAndSendSession, Name: p.addr}); v["status"] != "sent" {
			t.Fatalf("stop and send: %v", v)
		}
		want := "check the tests"
		if mark {
			want = "[stopped and sent] check the tests"
		}
		if got := p.lastPrompt(1); got != want {
			t.Fatalf("mark=%v: typed %q, want %q", mark, got, want)
		}
		if !strings.Contains(p.read("typed"), "\x1b") {
			t.Fatalf("mark=%v: no Esc stopped it: %q", mark, p.read("typed"))
		}
	}
}

func TestAForcedMessageWhileYouTypeTypesItsNote(t *testing.T) {
	for _, mark := range []bool{true, false} {
		p, h := forcedRig(t, mark)
		p.typeKeys("my half-typed line")
		p.waitFor("the gate sees you typing", func() bool { return strings.Contains(p.read("typed"), "my half-typed line") })
		if v := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: "use the staging bucket", Force: true}); v["status"] != "sent" {
			t.Fatalf("forced message: %v", v)
		}
		want := "use the staging bucket"
		if mark {
			want = "[sent while you were typing] use the staging bucket"
		}
		if got := p.lastPrompt(1); got != want {
			t.Fatalf("mark=%v: typed %q, want %q", mark, got, want)
		}
	}
}

func TestFlushSessionWhileYouTypeTypesItsNote(t *testing.T) {
	for _, mark := range []bool{true, false} {
		p, h := forcedRig(t, mark)
		p.typeKeys("my half-typed line")
		p.waitFor("the gate sees you typing", func() bool { return strings.Contains(p.read("typed"), "my half-typed line") })
		if v := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: "rerun the build"}); v["status"] != "waiting" || v["reason"] != "typing" {
			t.Fatalf("message: %v", v)
		}
		if v := sessionCall(t, h, McpCall{Kind: CallFlushSession, Name: p.addr}); v["status"] != "sent" {
			t.Fatalf("flush: %v", v)
		}
		want := "rerun the build"
		if mark {
			want = "[sent while you were typing] rerun the build"
		}
		if got := p.lastPrompt(1); got != want {
			t.Fatalf("mark=%v: typed %q, want %q", mark, got, want)
		}
	}
}

// An ordinary message goes in as given; so does a forced one that forced nothing (nobody was
// typing), and a stop-and-send with nothing to stop.
func TestAnOrdinaryMessageIsTypedAsGiven(t *testing.T) {
	p, h := forcedRig(t, true)
	if v := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: "plain words"}); v["status"] != "sent" {
		t.Fatalf("message: %v", v)
	}
	if got := p.lastPrompt(1); got != "plain words" {
		t.Fatalf("typed %q", got)
	}
	p.waitFor("working on it", func() bool { return p.status() == PaneWorking })
	p.step("stop")
	p.waitFor("idle", func() bool { return p.status() == PaneIdle })
	if v := sessionCall(t, h, McpCall{Kind: CallMessageSession, Name: p.addr, Text: "forced but free", Force: true}); v["status"] != "sent" {
		t.Fatalf("forced message: %v", v)
	}
	if got := p.lastPrompt(2); got != "forced but free" {
		t.Fatalf("typed %q", got)
	}
}
