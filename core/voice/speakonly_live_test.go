//go:build live

package voice_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

// aboutTheUser: Pixie telling you where you are, or that you can't answer (#491).
var aboutTheUser = regexp.MustCompile(`(?i)\b(you (?:can(?:'|’)t|cannot|can not|aren(?:'|’)t able to|are unable to) (?:answer|respond|reply)|you(?:'|’| a)re (?:not |un|away|busy)|you aren(?:'|’)t|not around|unavailable|you were (?:away|out|gone)|(?:the|your) mic|microphone)\b`)

// #433: a conversation opened only to speak says the notice it is given, sends no mic audio,
// and ends on its own. #436: it says only the update, with no follow-up nobody can answer.
// #443: with agent.listenAfterUpdates "never" (SpeakListens false), a question too is said with
// no mic, and it still ends on its own.
// Run with: go test -tags live -run LiveSpeakOnly/gemini -v ./voice/
func TestLiveSpeakOnly(t *testing.T) {
	cases := []struct {
		name, notice string
		check        func(t *testing.T, said string)
	}{
		{"update", "Updates:\n- Coordinator reply: The build passed and all 212 tests are green. It can deploy to staging next if you want.",
			func(t *testing.T, said string) {
				if strings.HasSuffix(strings.TrimSpace(said), "?") {
					t.Errorf("asked a follow-up nobody can answer: %q", said)
				}
			}},
		{"question", "Updates:\n- Session update: orchard-care-guide is asking: should care.md be sorted alphabetically or by season?",
			func(t *testing.T, said string) {
				if l := strings.ToLower(said); !strings.Contains(l, "alphabetic") || !strings.Contains(l, "season") {
					t.Errorf("didn't say the question: %q", said)
				}
			}},
	}
	secrets := liveSecrets(t)
	for _, p := range liveProviders() {
		for _, tc := range cases {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				if _, ok := secrets.APIKey(p.name); !ok {
					v, _ := voice.VarFor(p.name)
					t.Skipf("no %s found in the environment or the app's .env files", v)
				}
				cfg := p.cfg
				cfg.Instructions = liveInstructions + " When told about an update, say it to the user in one short sentence." +
					` End with "Anything else?" only if the user probably wants more: the update offers a choice or a next step.`
				r := newLiveRig(t, p.model(&liveWireLog{}, t), cfg, secrets)
				if !r.rt.OpenToSpeak() {
					t.Fatal("refused at rest")
				}
				if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
					return
				}
				r.waitFor("open", 2*time.Second, r.rt.IsOpen)
				r.rt.Inject(voice.Notice{Text: tc.notice})
				r.waitFor("the notice said", 20*time.Second, func() bool { return r.playedMs() > 500 && r.liveSpy.assistant() != "" })
				r.waitFor("it ends on its own", 20*time.Second, func() bool { return !r.rt.IsOpen() })
				r.liveSpy.mu.Lock()
				sent := r.liveSpy.audioSent
				r.liveSpy.mu.Unlock()
				if sent != 0 {
					t.Errorf("sent %d mic frames", sent)
				}
				if r.rt.IsOpen() {
					t.Error("still open: it should end once said")
				}
				tc.check(t, r.liveSpy.assistant())
				if m := aboutTheUser.FindString(r.liveSpy.assistant()); m != "" {
					t.Errorf("said %q of the user (#491): %q", m, r.liveSpy.assistant())
				}
				t.Logf("%s played=%d ms", r.liveSpy.summary(), r.playedMs())
			})
		}
	}
}
