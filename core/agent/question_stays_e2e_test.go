package agent_test

import (
	"strings"
	"testing"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
)

// #347 end to end (the real pix, fakeclaude standing in for Claude Code): a question a session
// waits on stays in Pixie's bubble and in the chat window's requests until it is answered, by
// a tap or in the session's own terminal, whatever happens meanwhile: a background subagent's
// tool (#344) or another of its own, its hook's wait running out, a redraw, the bubble seen.

// heldFor is how long the session's hook holds a question for Pixie (agent.questionWaitSecs):
// short, so the test sees it run out.
const heldFor = time.Second

func questionRig(t *testing.T) (*pixSession, *AgentHandle) {
	p := startPixSessionHolding(t, heldFor)
	h, _ := pixRuntime(t, p, config(false), nil)
	return p, h
}

// asked waits for the bubble to list the session's question with a confirm token, and returns it.
func (p *pixSession) asked(h *AgentHandle) UpdateItem {
	p.t.Helper()
	var it UpdateItem
	p.waitFor("the question in the bubble", func() bool {
		items := h.Updates()
		if len(items) != 1 || items[0].Token == "" || items[0].Session != p.addr {
			return false
		}
		it = items[0]
		return true
	})
	return it
}

// stillAsked fails unless the bubble and the chat window's requests still list item, and
// nothing was answered in the session.
func (p *pixSession) stillAsked(h *AgentHandle, item UpdateItem, after string) {
	p.t.Helper()
	items := h.Updates()
	if len(items) != 1 || items[0].Token != item.Token || items[0].ID != item.ID {
		p.t.Fatalf("after %s: the bubble lists %+v, want %+v", after, items, item)
	}
	needs := agentapp.RequestEntries(items, RosterView{})
	if len(needs) != 1 || needs[0].Request.Token != item.Token {
		p.t.Fatalf("after %s: the chat window's requests are %+v", after, needs)
	}
	if a := p.read("answers"); a != "" {
		p.t.Fatalf("after %s: answered %q", after, a)
	}
}

// gone waits for the session to have answered want, and the bubble to list nothing.
func (p *pixSession) gone(h *AgentHandle, want string) {
	p.t.Helper()
	p.waitFor("answered "+want, func() bool { return strings.TrimSpace(p.read("answers")) == want })
	p.waitFor("the bubble empty", func() bool { return len(h.Updates()) == 0 })
}

// The maintainer's case: a menu goes up while a background subagent works. It waits through
// the subagent's tools, its hook letting go (the terminal shows it), a redraw and the bubble
// being seen, and a tap on an option answers it in the session.
func TestASessionsMenuStaysUntilYouTapAnOption(t *testing.T) {
	p, h := questionRig(t)
	p.step("work")
	p.step("ask")
	item := p.asked(h)
	if strings.Join(item.Options, "|") != "Push|Don't push" {
		t.Fatalf("%+v", item)
	}
	p.step("subtool")
	p.stillAsked(h, item, "a subagent's tool, while held")
	p.step("tool") // a tool of its own it ran beside the menu, ending after it went up
	p.stillAsked(h, item, "its own other tool, while held")
	time.Sleep(heldFor + 500*time.Millisecond) // its hook lets go: Claude shows the menu
	p.waitFor("blocked on the menu", func() bool { return p.status() == PaneBlocked })
	p.stillAsked(h, item, "the hold ran out")
	p.step("subtool")
	p.stillAsked(h, item, "a subagent's tool, at the menu")
	if st := p.status(); st != PaneBlocked {
		t.Fatalf("a subagent's tool set the session %v", st)
	}
	p.step("redraw")
	p.stillAsked(h, item, "a redraw")
	h.UpdatesSeen([]uint64{item.ID})
	p.stillAsked(h, item, "the bubble seen")
	time.Sleep(500 * time.Millisecond)
	p.stillAsked(h, item, "a while longer")
	h.Choose(item.Token, 2)
	p.gone(h, "menu key 2")
	p.waitFor("working again", func() bool { return p.status() == PaneWorking })
}

// Answered in the session's own terminal instead: it goes from Pixie then, and not before.
func TestASessionsMenuAnsweredInItsTerminalLeavesPixie(t *testing.T) {
	p, h := questionRig(t)
	p.step("work")
	p.step("ask")
	item := p.asked(h)
	time.Sleep(heldFor + 500*time.Millisecond)
	p.waitFor("blocked on the menu", func() bool { return p.status() == PaneBlocked })
	p.stillAsked(h, item, "the hold ran out")
	p.typeKeys("1")
	p.gone(h, "menu key 1")
}

// Answered in Pixie while still held: the hook takes the answer, nothing is typed.
func TestASessionsMenuAnsweredWhileHeld(t *testing.T) {
	p, h := questionRig(t)
	p.step("work")
	p.step("ask")
	item := p.asked(h)
	p.step("subtool")
	p.stillAsked(h, item, "a subagent's tool")
	h.Choose(item.Token, 1)
	p.gone(h, "menu Push")
}

// The foreground case of #344: a subagent's own permission dialog waits through its hook
// letting go and a redraw; Allow types its Yes, and the subagent's tool ending clears it and
// the blocked status.
func TestASubagentsPermissionStaysUntilYouAllowIt(t *testing.T) {
	p, h := questionRig(t)
	p.step("work")
	p.step("subperm")
	item := p.asked(h)
	if len(item.Options) != 3 || item.Options[0] != "Yes" { // its dialog, as shown
		t.Fatalf("%+v", item)
	}
	time.Sleep(heldFor + 500*time.Millisecond)
	p.waitFor("blocked on the dialog", func() bool { return p.status() == PaneBlocked })
	p.stillAsked(h, item, "the hold ran out")
	p.step("redraw")
	p.stillAsked(h, item, "a redraw")
	h.Decide(item.Token, true)
	p.gone(h, "subperm key 1")
	p.waitFor("working again", func() bool { return p.status() == PaneWorking })
}
