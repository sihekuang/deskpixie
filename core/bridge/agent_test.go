package bridge

import (
	"strings"
	"sync"
	"testing"

	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The engine builds the agent through Options.Agent and dp_destroy (Close) shuts it down.

type bridgeAgent struct {
	mu           sync.Mutex
	opened, shut int
}

func (b *bridgeAgent) Tools() []voice.Tool                       { return nil }
func (b *bridgeAgent) Persona() string                           { return "" }
func (b *bridgeAgent) Tray() app.AgentTray                       { return app.AgentTray{Enabled: true} }
func (b *bridgeAgent) Menu(string) bool                          { return true }
func (b *bridgeAgent) ChatSend(string) (app.ChatSent, error)     { return app.ChatSent{}, nil }
func (b *bridgeAgent) ChatDelegate(string) (string, error)       { return "", nil }
func (b *bridgeAgent) ChatStop()                                 {}
func (b *bridgeAgent) Interrupt(string, string) error            { return nil }
func (b *bridgeAgent) SetTextMode(bool)                          {}
func (b *bridgeAgent) Attachments() *attach.Store                { return nil }
func (b *bridgeAgent) OpenTerminal()                             { b.mu.Lock(); b.opened++; b.mu.Unlock() }
func (b *bridgeAgent) AttachVoice(app.AgentVoice)                {}
func (b *bridgeAgent) OnMouth(int)                               {}
func (b *bridgeAgent) Shutdown()                                 { b.mu.Lock(); b.shut++; b.mu.Unlock() }
func (b *bridgeAgent) UpdatesSeen([]uint64)                      {}
func (b *bridgeAgent) UpdatesDismissed([]uint64)                 {}
func (b *bridgeAgent) RepliesShown([]uint64)                     {}
func (b *bridgeAgent) PendingReplies() any                       { return []any{} }
func (b *bridgeAgent) Decide(string, bool)                       {}
func (b *bridgeAgent) Choose(string, int)                        {}
func (b *bridgeAgent) TypedAnswer(string, string)                {}
func (b *bridgeAgent) ChatAbout(string)                          {}
func (b *bridgeAgent) Updates() any                              { return []any{} }
func (b *bridgeAgent) SetAway(bool)                              {}
func (b *bridgeAgent) FocusJob(string, string)                   {}
func (b *bridgeAgent) Roster() any                               { return struct{}{} }
func (b *bridgeAgent) FocusAgent(string)                         {}
func (b *bridgeAgent) FocusSession(string)                       {}
func (b *bridgeAgent) SessionPlaces([]string) []app.SessionPlace { return nil }
func (b *bridgeAgent) ReportFocusOutcome(bool, string)           {}

func TestTheEngineBuildsTheAgentAndCloseShutsItDown(t *testing.T) {
	ag := &bridgeAgent{}
	e := newEngine(t, Options{Agent: func(settings.Config, string, app.AgentHooks) app.Agent { return ag }})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	if res := callJSON(t, e.Call("pixie.click", []byte(`{}`))); res["ok"] == nil {
		t.Fatalf("pixie.click: %s", res["error"])
	}
	state := callJSON(t, e.Call("app.state", []byte(`{}`)))
	if string(state["ok"]) == "" || !strings.Contains(string(state["ok"]), `"agent":{"enabled":true`) {
		t.Fatalf("app.state: %s", state["ok"])
	}
	e.Close()
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.opened != 1 || ag.shut != 1 {
		t.Fatalf("opened %d, shut %d", ag.opened, ag.shut)
	}
}
