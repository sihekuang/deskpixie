//go:build live

package pixhost_test

// Opt-in live checks of #373: a forced send into the real pix around the installed Claude Code
// and Codex stops a busy turn and goes in past what you're typing, your draft put back after.
// Run with:
//
//	go test -tags live -run LiveForced -v -timeout 15m ./pixhost/

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

func TestLiveForcedSendClaude(t *testing.T) {
	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "haiku"
	}
	forcedSend(t, startLivePix(t, "claude", "--model", model))
}

func TestLiveForcedSendCodex(t *testing.T) {
	forcedSend(t, startLivePix(t, "codex"))
}

// forcedSend: a long turn, your draft in its prompt, a message that waits, then the forced send.
func forcedSend(t *testing.T, p *livePix) {
	t.Helper()
	ctx := context.Background()
	if _, err := p.host.PromptDelivery(ctx, p.addr, "Write the numbers from 1 to 600, one per line, with no other text."); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, p.events, p.addr, "long prompt → working", 60*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneWorking })
	time.Sleep(2 * time.Second)
	p.term.Write([]byte("my draft"))
	time.Sleep(time.Second)
	d, err := p.host.PromptDelivery(ctx, p.addr, "Stop counting. Reply with the word banana in capital letters, and nothing else.")
	if err != nil || d.Status != "waiting" {
		t.Fatalf("not waiting: %+v %v", d, err)
	}
	t.Logf("✓ waits: %s", d.Reason)
	d, err = p.host.Flush(ctx, p.addr, agent.ForcedNotes{Typing: agent.TypingNote, Stopped: agent.StoppedNote})
	if err != nil || (d.Status != "typed" && d.Reason != "stopping") {
		t.Fatalf("forced: %+v %v", d, err)
	}
	t.Logf("✓ forced: %s %s", d.Status, d.Reason)
	if !screenShows(p.screen, "[stoppedandsent]Stopcounting.", 60*time.Second) {
		t.Fatal("the forced message never went in")
	}
	t.Log("✓ the turn stopped and the message went in")
	if !screenShows(p.screen, "BANANA", 90*time.Second) {
		t.Fatal("no reply to the forced message")
	}
	t.Log("✓ it was answered")
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		b, _ := os.ReadFile(p.screen)
		flat := strings.Join(strings.Fields(csi.ReplaceAllString(string(b), "")), "")
		if i := strings.Index(flat, "[stoppedandsent]"); i >= 0 && strings.Contains(flat[i:], "mydraft") {
			break
		}
		if time.Now().After(end) {
			t.Fatal("your draft didn't come back")
		}
	}
	t.Log("✓ your draft is back")
}
