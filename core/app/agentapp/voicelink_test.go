package agentapp

import (
	"fmt"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The real voice loop can drop a held notice (#339).
var _ agent.HeldWithdrawer = (*voice.Runtime)(nil)

// plainVoice is a voice loop that can't withdraw; withdrawingVoice can.
type plainVoice struct{}

func (plainVoice) Inject(voice.Notice)                 {}
func (plainVoice) HoldOpen(bool)                       {}
func (plainVoice) Open() bool                          { return false }
func (plainVoice) Subscribe() <-chan voice.VoiceSignal { return nil }

type withdrawingVoice struct {
	plainVoice
	withdrawn []string
}

func (v *withdrawingVoice) WithdrawHeld(s string, upTo uint64) {
	v.withdrawn = append(v.withdrawn, fmt.Sprint(s, "≤", upTo))
}

// #339: the link passes a withdrawal on to a voice loop that can take it; with none attached,
// or one that can't, it does nothing.
func TestVoiceLinkPassesAWithdrawalOn(t *testing.T) {
	var l VoiceLink
	l.WithdrawHeld("repo-claude", 3) // nothing attached yet
	v := &withdrawingVoice{}
	l.attach(v)
	l.WithdrawHeld("repo-claude", 3)
	if len(v.withdrawn) != 1 || v.withdrawn[0] != "repo-claude≤3" {
		t.Fatalf("%q", v.withdrawn)
	}
	var other VoiceLink
	other.attach(plainVoice{})
	other.WithdrawHeld("repo-claude", 3)
}
