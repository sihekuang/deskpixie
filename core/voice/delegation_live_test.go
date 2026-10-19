//go:build live

package voice_test

// #157, live: how Pixie hands off what it hears, above all several requests in one breath. Steps in an order or a
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

type severalCase struct {
	name, say string
	calls     int
	// check returns what's wrong with the delegate calls and the names of Pixie's own tools it
	// called ("" when right).
	check func(calls []handOff, own []string) string
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

// tasks joins the calls' tasks, for checks that don't care which call says what.
func tasks(c []handOff) string {
	var t []string
	for _, h := range c {
		t = append(t, h.task)
	}
	return strings.Join(t, " | ")
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
		check: func(c []handOff, _ []string) string {
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
		check: func(c []handOff, _ []string) string {
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
		check: func(c []handOff, _ []string) string {
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
		check: func(c []handOff, _ []string) string {
			all := c[0].task + " | " + c[1].task
			if !strings.Contains(all, "pasta") || !strings.Contains(all, "test") {
				return "one call each for the recipe and the tests"
			}
			return ""
		},
	},
	{
		name:  "three-unrelated",
		say:   "Find me a birthday gift idea for my mom. Also write a haiku about coffee. And separately, check whether the deskpixie build passes.",
		calls: 3,
		check: func(c []handOff, _ []string) string {
			all := tasks(c)
			if !strings.Contains(all, "gift") || !strings.Contains(all, "haiku") || !strings.Contains(all, "build") {
				return "one call each for the gift, the haiku and the build"
			}
			return ""
		},
	},
	{
		name:  "first-said-last",
		say:   "Open a pull request for my login branch, but first rebase it on main.",
		calls: 1,
		check: func(c []handOff, _ []string) string {
			// The user's own "…, but first rebase it" keeps the order too.
			if !before(c[0].task, "rebase", "pull request") && !strings.Contains(c[0].task, "first rebase") {
				return "the rebase comes first, though it was said last"
			}
			return ""
		},
	},
	{
		name:  "after-that-if",
		say:   "Run the deskpixie tests, and after that, if they pass, open a pull request.",
		calls: 1,
		check: func(c []handOff, _ []string) string {
			// "run" rather than "test": speech recognition sometimes hears "tasks".
			if !before(c[0].task, "run", "pull request") {
				return "the tests run first, then the pull request"
			}
			if !strings.Contains(c[0].task+c[0].context, "pass") {
				return "the pull request only if the tests pass"
			}
			return ""
		},
	},
	{
		name:  "detail-corrected",
		say:   "Write an email to Sam inviting him to lunch on Tuesday. Wait, make that Thursday.",
		calls: 1,
		check: func(c []handOff, _ []string) string {
			if !strings.Contains(c[0].task+c[0].context, "thursday") {
				return "the lunch is on Thursday"
			}
			if strings.Contains(c[0].task+c[0].context, "tuesday") {
				return "Tuesday was taken back"
			}
			return ""
		},
	},
	{
		name:  "never-mind-all",
		say:   "Draft an email to my landlord about the broken heater. Actually, never mind, forget that. Just tell me a joke.",
		calls: 0,
		check: func([]handOff, []string) string { return "" },
	},
	{
		name:  "own-tool-and-work",
		say:   "Set a timer for ten minutes, and research the best cast iron pans.",
		calls: 1,
		check: func(c []handOff, own []string) string {
			if !has(own, "timer") {
				return "the timer is Pixie's own tool"
			}
			if !strings.Contains(c[0].task, "pan") || strings.Contains(c[0].task, "timer") {
				return "only the research is handed off"
			}
			return ""
		},
	},
	{
		name:  "ordered-and-unrelated",
		say:   "Summarize my unread emails, then draft replies to the urgent ones. Also, find a recipe for banana bread.",
		calls: 2,
		check: func(c []handOff, _ []string) string {
			var mail, bread string
			for _, h := range c {
				switch {
				case strings.Contains(h.task, "banana"):
					bread = h.task
				case strings.Contains(h.task, "email"):
					mail = h.task
				}
			}
			if mail == "" || bread == "" {
				return "one call for the emails, one for the banana bread"
			}
			if !before(mail, "summar", "repl") {
				return "the summary comes first, then the replies"
			}
			return ""
		},
	},
	{
		name:  "ordered-and-unrelated-2",
		say:   "Back up my photos to the external drive, and after that free up some disk space. Oh, and what's a good movie to watch tonight?",
		calls: 2,
		check: func(c []handOff, _ []string) string {
			var disk, movie string
			for _, h := range c {
				switch {
				case strings.Contains(h.task, "movie"):
					movie = h.task
				case strings.Contains(h.task, "photo"):
					disk = h.task
				}
			}
			if disk == "" || movie == "" {
				return "one call for the photos and disk space, one for the movie"
			}
			if !before(disk, "back", "space") {
				return "the backup comes first, then freeing space"
			}
			return ""
		},
	},
	{
		name:  "fact-check",
		say:   "Who won the World Series last year?",
		calls: 1,
		check: func(c []handOff, _ []string) string {
			if !strings.HasPrefix(c[0].task, "check:") {
				return `a fact Pixie isn't sure of is a "Check: " task`
			}
			return ""
		},
	},
	{
		name:  "kept-arithmetic",
		say:   "What's fifteen percent of eighty?",
		calls: 0,
		check: func(_ []handOff, own []string) string {
			if !has(own, "calculate") {
				return "arithmetic is Pixie's own calculate tool"
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
						r.rt.Hotkey()
						hs, ow := got()
						t.Logf("run %d: %s; delegate calls %+v; own tools %v", run, r.liveSpy.summary(), hs, ow)
						switch {
						case len(hs) != c.calls:
							t.Errorf("run %d: %d delegate calls, want %d: %+v; own tools %v", run, len(hs), c.calls, hs, ow)
						default:
							if why := c.check(hs, ow); why != "" {
								t.Errorf("run %d: %s: %+v; own tools %v", run, why, hs, ow)
							}
						}
					}
				})
			}
		})
	}
}
