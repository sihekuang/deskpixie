//go:build live

package voice_test

// #157, live: Pixie hands off several requests said in one breath. Steps in an order or a
// change of mind come out as one delegate call, in the user's order, with nothing taken back;
// unrelated asks come out as one call each. Each utterance is spoken (say + afconvert) to the
// real provider with the app's persona and delegation rules and a fake delegate that records
// what it was handed. Providers without a key skip, as in live_test.go. Run:
//
//	LIVE_DELEGATION_N=3 go test -tags live -run TestLiveDelegationOfSeveralRequests -v ./voice/

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/openailive"
)

// handOff is one delegate call: its task and context, lowercased.
type handOff struct{ task, context string }

// recordingDelegate is the delegate tool with its real name, description and schema, recording
// each call and answering like the app (a job id; the conversation ends after the reply).
type recordingDelegate struct {
	agent.Delegate
	mu    *sync.Mutex
	calls *[]handOff
}

func (d recordingDelegate) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	var a struct{ Task, Context string }
	_ = json.Unmarshal(raw, &a)
	d.mu.Lock()
	*d.calls = append(*d.calls, handOff{strings.ToLower(a.Task), strings.ToLower(a.Context)})
	n := len(*d.calls)
	d.mu.Unlock()
	out, _ := json.Marshal(map[string]any{"job_id": fmt.Sprintf("job_live%02d", n), "status": "delegated"})
	return voice.ToolOutcome{Output: out, EndAfterReply: !strings.HasPrefix(a.Task, "Check:")}
}

type severalCase struct {
	name, say string
	calls     int
	// check returns what's wrong with the calls ("" when right).
	check func(calls []handOff) string
}

// before reports whether a appears before b in s (both present).
func before(s, a, b string) bool {
	i, j := strings.Index(s, a), strings.Index(s, b)
	return i >= 0 && j >= 0 && i < j
}

var severalCases = []severalCase{
	{
		name:  "before-we-do-that",
		say:   "Let's go ahead and get the build merged, since I've tested it. Oh actually, before we do that, write a program to check the latest SpaceX stock price.",
		calls: 1,
		check: func(c []handOff) string {
			if !before(c[0].task, "spacex", "merg") {
				return "the program should come first, then the merge"
			}
			if strings.Contains(c[0].task+c[0].context, "instead") {
				return "the merge was put off, not taken back"
			}
			return ""
		},
	},
	{
		name:  "instead",
		say:   "Start a new project for my recipe collection. Wait, instead, just make me a shopping list for tacos.",
		calls: 1,
		check: func(c []handOff) string {
			if !strings.Contains(c[0].task, "taco") {
				return "the shopping list is what's left"
			}
			if strings.Contains(c[0].task, "project") {
				return "the project was taken back"
			}
			return ""
		},
	},
	{
		name:  "and-then",
		say:   "Summarize my open GitHub issues for deskpixie, and then draft an email to Sam with that summary.",
		calls: 1,
		check: func(c []handOff) string {
			if !before(c[0].task, "summar", "email") {
				return "the summary comes first, then the email"
			}
			return ""
		},
	},
	{
		name:  "unrelated",
		say:   "Find me a good pasta recipe for tonight. Also, separately, check whether the deskpixie tests pass.",
		calls: 2,
		check: func(c []handOff) string {
			all := c[0].task + " | " + c[1].task
			if !strings.Contains(all, "pasta") || !strings.Contains(all, "test") {
				return "one call each for the recipe and the tests"
			}
			return ""
		},
	},
}

func TestLiveDelegationOfSeveralRequests(t *testing.T) {
	secrets := liveSecrets(t)
	n := 1
	if v, err := strconv.Atoi(os.Getenv("LIVE_DELEGATION_N")); err == nil && v > 0 {
		n = v
	}
	instructions := app.Instructions(voice.DefaultPersona(), agent.DelegationPersona)
	lead := make([]int16, audio.SampleRate/2) // 0.5 s of silence before speaking
	for _, p := range liveProviders() {
		if p.name == openailive.Name {
			continue // GPT-Live hands off by client delegation, not a delegate call
		}
		t.Run(p.name, func(t *testing.T) {
			if _, ok := secrets.APIKey(p.name); !ok {
				v, _ := voice.VarFor(p.name)
				t.Skipf("no %s found in the environment or the app's .env files", v)
			}
			for _, c := range severalCases {
				speech := liveSpeech(t, c.say)
				t.Run(c.name, func(t *testing.T) {
					for run := 1; run <= n; run++ {
						var mu sync.Mutex
						var calls []handOff
						reg := voice.NewToolRegistry()
						reg.Register(recordingDelegate{mu: &mu, calls: &calls})
						cfg := p.cfg
						cfg.Instructions = instructions
						r := newLiveRigWith(t, p.model(&liveWireLog{}, t), cfg, secrets, reg)
						r.rt.Hotkey()
						if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
							return
						}
						r.say(append(append([]int16(nil), lead...), speech...))
						got := func() []handOff { mu.Lock(); defer mu.Unlock(); return append([]handOff(nil), calls...) }
						r.waitFor("a delegate call", 30*time.Second, func() bool { return len(got()) > 0 })
						time.Sleep(4 * time.Second) // a second call comes in the same response
						r.rt.Hotkey()
						hs := got()
						t.Logf("run %d: heard %q; delegate calls %+v", run, r.liveSpy.summary(), hs)
						switch {
						case len(hs) != c.calls:
							t.Errorf("run %d: %d delegate calls, want %d: %+v", run, len(hs), c.calls, hs)
						default:
							if why := c.check(hs); why != "" {
								t.Errorf("run %d: %s: %+v", run, why, hs)
							}
						}
					}
				})
			}
		})
	}
}
