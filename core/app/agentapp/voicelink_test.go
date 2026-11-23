package agentapp

import (
	"fmt"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The real voice loop can drop a held notice (#339).
var _ agent.HeldWithdrawer = (*voice.Runtime)(nil)

// The real voice loop can open a conversation only to speak (#433).
var _ agent.SpeakOpener = (*voice.Runtime)(nil)

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

func (v *withdrawingVoice) WithdrawQuestion(id uint64) {
	v.withdrawn = append(v.withdrawn, fmt.Sprint("#", id))
}

// #490: as for a held notice, a question's withdrawal.
func TestVoiceLinkPassesAQuestionsWithdrawalOn(t *testing.T) {
	var l VoiceLink
	l.WithdrawQuestion(7) // nothing attached yet
	v := &withdrawingVoice{}
	l.attach(v)
	l.WithdrawQuestion(7)
	if len(v.withdrawn) != 1 || v.withdrawn[0] != "#7" {
		t.Fatalf("%q", v.withdrawn)
	}
	var other VoiceLink
	other.attach(plainVoice{})
	other.WithdrawQuestion(7)
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

type speakingVoice struct {
	plainVoice
	toSpeak int
}

func (v *speakingVoice) OpenToSpeak() bool { v.toSpeak++; return true }

// #433: the link opens a voice loop to speak when it can, opens one that can't as usual, and
// with none attached does nothing.
func TestVoiceLinkOpensToSpeak(t *testing.T) {
	var l VoiceLink
	if l.OpenToSpeak() {
		t.Fatal("opened with nothing attached")
	}
	v := &speakingVoice{}
	l.attach(v)
	if !l.OpenToSpeak() || v.toSpeak != 1 {
		t.Fatal("not opened to speak")
	}
	var other VoiceLink
	other.attach(plainVoice{}) // its Open answers false
	if other.OpenToSpeak() {
		t.Fatal("a plain voice loop's answer wasn't passed on")
	}
}
