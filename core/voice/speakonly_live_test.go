//go:build live

package voice_test

import (
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

// #433: a conversation opened only to speak says the notice it is given, sends no mic audio,
// and ends on its own. Run with: go test -tags live -run LiveSpeakOnly/gemini -v ./voice/
func TestLiveSpeakOnly(t *testing.T) {
	secrets := liveSecrets(t)
	for _, p := range liveProviders() {
		t.Run(p.name, func(t *testing.T) {
			if _, ok := secrets.APIKey(p.name); !ok {
				v, _ := voice.VarFor(p.name)
				t.Skipf("no %s found in the environment or the app's .env files", v)
			}
			cfg := p.cfg
			cfg.Instructions = liveInstructions + " When told about an update, say it to the user in one short sentence."
			r := newLiveRig(t, p.model(&liveWireLog{}, t), cfg, secrets)
			if !r.rt.OpenToSpeak() {
				t.Fatal("refused at rest")
			}
			if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
				return
			}
			r.waitFor("open", 2*time.Second, r.rt.IsOpen)
			r.rt.Inject(voice.Notice{Text: "Coordinator replies while you were away:\n- Coordinator reply: The build passed and all 212 tests are green."})
			r.waitFor("the notice said", 20*time.Second, func() bool { return r.playedMs() > 500 && r.liveSpy.assistant() != "" })
			r.waitFor("it ends on its own", 20*time.Second, func() bool { return !r.rt.IsOpen() })
			r.liveSpy.mu.Lock()
			sent := r.liveSpy.audioSent
			r.liveSpy.mu.Unlock()
			if sent != 0 {
				t.Errorf("sent %d mic frames", sent)
			}
			t.Logf("%s played=%d ms", r.liveSpy.summary(), r.playedMs())
		})
	}
}
