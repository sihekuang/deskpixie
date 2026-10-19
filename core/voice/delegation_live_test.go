//go:build live

package voice_test

// #157/#160, live: Pixie hands off what it hears as said, above all several requests in one
// breath, and keeps only what is all its own. Steps in an order or a
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
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
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

// ownTool is one of Pixie's own tools with its real name, description and schema, recording
// that it was called and answering with a fixed result.
type ownTool struct {
	voice.Tool
	out   string
	mu    *sync.Mutex
	calls *[]string
}

func (o ownTool) Invoke(context.Context, json.RawMessage) voice.ToolOutcome {
	o.mu.Lock()
	*o.calls = append(*o.calls, o.Name())
	o.mu.Unlock()
	return voice.ToolOutcome{Output: json.RawMessage(o.out)}
}

// ownTools are the tools Pixie keeps for itself (delegation.md), stubbed.
func ownTools(mu *sync.Mutex, calls *[]string) []voice.Tool {
	om := tools.NewOpenMeteo()
	stub := func(t voice.Tool, out string) voice.Tool { return ownTool{Tool: t, out: out, mu: mu, calls: calls} }
	return []voice.Tool{
		stub(app.AdaptTool(tools.GetTime{Clock: tools.SystemClock{}, Places: om}), `{"time":"3:04 PM","date":"Sunday, October 18"}`),
		stub(app.AdaptTool(tools.GetWeather{Places: om, Sky: om}), `{"place":"Seattle","temperature_c":12,"conditions":"cloudy"}`),
		stub(app.AdaptTool(tools.Calculate{}), `{"result":12}`),
		stub(app.AdaptTool(tools.TimerTool{}), `{"ok":true,"timer":"10 minutes","ends_in":"10 minutes"}`),
		stub(app.AdaptTool(tools.Media{}), `{"ok":true,"state":"paused"}`),
	}
}

// severalCase is one utterance and what Pixie should do with it (#160): hand off everything
// said in one call, with nothing dropped (every word in words appears in the task); when part
// of it is for Pixie's own tools, call them and leave that part out of the task (notWords); when all of it
// is, call them and hand off nothing.
type severalCase struct {
	name, say string
	// calls: delegate calls wanted, 0 or 1; anyCalls allows either.
	calls    int
	anyCalls bool
	// words must all appear in the task (lowercased), notWords must not; own are tools Pixie
	// must call itself.
	words, notWords, own []string
}

// wrong returns what's wrong with what Pixie did ("" when right).
func (c severalCase) wrong(calls []handOff, own []string) string {
	switch {
	case c.anyCalls && len(calls) > 1, !c.anyCalls && len(calls) != c.calls:
		return fmt.Sprintf("%d delegate calls, want %d", len(calls), c.calls)
	case len(calls) == 1 && len(c.own) == 0 && len(own) > 0:
		return "handed off, but did part of it itself"
	}
	if len(calls) == 1 {
		for _, w := range c.words {
			if !strings.Contains(calls[0].task, w) {
				return fmt.Sprintf("the task dropped %q", w)
			}
		}
		for _, w := range c.notWords {
			// The task only: the context may say Pixie did it ("the timer is set"), which the
			// coordinator treats as background.
			if strings.Contains(calls[0].task, w) {
				return fmt.Sprintf("handed off %q, which Pixie did itself", w)
			}
		}
	}
	for _, name := range c.own {
		if !has(own, name) {
			return name + " is Pixie's own tool"
		}
	}
	return ""
}

// has reports whether name is among the tools called.
func has(own []string, name string) bool {
	for _, n := range own {
		if n == name {
			return true
		}
	}
	return false
}

var severalCases = []severalCase{
	{name: "before-we-do-that", calls: 1, words: []string{"merg", "spacex"},
		say: "Let's go ahead and get the build merged, since I've tested it. Oh actually, before we do that, write a program to check the latest SpaceX stock price."},
	{name: "instead", calls: 1, words: []string{"project", "taco"},
		say: "Start a new project for my recipe collection. Wait, instead, just make me a shopping list for tacos."},
	{name: "and-then", calls: 1, words: []string{"summar", "email"},
		say: "Summarize my open GitHub issues for deskpixie, and then draft an email to Sam with that summary."},
	{name: "unrelated", calls: 1, words: []string{"pasta", "test"},
		say: "Find me a good pasta recipe for tonight. Also, separately, check whether the deskpixie tests pass."},
	{name: "three-unrelated", calls: 1, words: []string{"gift", "haiku", "build"},
		say: "Find me a birthday gift idea for my mom. Also write a haiku about coffee. And separately, check whether the deskpixie build passes."},
	{name: "first-said-last", calls: 1, words: []string{"pull request", "rebase"},
		say: "Open a pull request for my login branch, but first rebase it on main."},
	// "pass", not "test": speech recognition sometimes hears "tasks".
	{name: "after-that-if", calls: 1, words: []string{"pull request", "pass"},
		say: "Run the deskpixie tests, and after that, if they pass, open a pull request."},
	{name: "detail-corrected", calls: 1, words: []string{"tuesday", "thursday"},
		say: "Write an email to Sam inviting him to lunch on Tuesday. Wait, make that Thursday."},
	// What's left is a joke, Pixie's own: it may tell it, or hand off the whole thing.
	{name: "never-mind-all", anyCalls: true, words: []string{"landlord", "joke"},
		say: "Draft an email to my landlord about the broken heater. Actually, never mind, forget that. Just tell me a joke."},
	// The timer is Pixie's own: it sets it and hands off only the research.
	{name: "own-tool-and-work", calls: 1, words: []string{"pan"}, notWords: []string{"timer"}, own: []string{"timer"},
		say: "Set a timer for ten minutes, and research the best cast iron pans."},
	{name: "ordered-and-unrelated", calls: 1, words: []string{"email", "repl", "banana"},
		say: "Summarize my unread emails, then draft replies to the urgent ones. Also, find a recipe for banana bread."},
	{name: "ordered-and-unrelated-2", calls: 1, words: []string{"photo", "space", "movie"},
		say: "Back up my photos to the external drive, and after that free up some disk space. Oh, and what's a good movie to watch tonight?"},
	{name: "fact", calls: 1, words: []string{"world series"},
		say: "Who won the World Series last year?"},
	{name: "kept-arithmetic", calls: 0, own: []string{"calculate"},
		say: "What's fifteen percent of eighty?"},
	{name: "kept-timer-and-weather", calls: 0, own: []string{"timer", "get_weather"},
		say: "Set a timer for ten minutes, and tell me the weather in Seattle."},
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
						var own []string
						reg := voice.NewToolRegistry()
						reg.Register(recordingDelegate{mu: &mu, calls: &calls})
						for _, o := range ownTools(&mu, &own) {
							reg.Register(o)
						}
						cfg := p.cfg
						cfg.Instructions = instructions
						r := newLiveRigWith(t, p.model(&liveWireLog{}, t), cfg, secrets, reg)
						r.rt.Hotkey()
						if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
							return
						}
						r.say(append(append([]int16(nil), lead...), speech...))
						got := func() ([]handOff, []string) {
							mu.Lock()
							defer mu.Unlock()
							return append([]handOff(nil), calls...), append([]string(nil), own...)
						}
						r.waitFor("a tool call or a reply", 30*time.Second, func() bool {
							hs, ow := got()
							return len(hs)+len(ow) > 0 || r.liveSpy.count(voice.EvResponseDone) > 0
						})
						time.Sleep(5 * time.Second) // more calls come in the same response, or after a tool's result
						// GPT-Live's backend model calls tools after the voice has spoken: give it time.
						for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
							if hs, ow := got(); len(hs)+len(ow) > 0 {
								break
							}
						}
						r.rt.Hotkey()
						hs, ow := got()
						t.Logf("run %d: %s; delegate calls %+v; own tools %v", run, r.liveSpy.summary(), hs, ow)
						if why := c.wrong(hs, ow); why != "" {
							t.Errorf("run %d: %s: %+v; own tools %v", run, why, hs, ow)
						}
					}
				})
			}
		})
	}
}
