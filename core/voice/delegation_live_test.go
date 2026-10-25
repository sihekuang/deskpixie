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
//
// Gemini runs first; when it fails, the OpenAI providers are skipped (LIVE_ALL_PROVIDERS=1
// runs them anyway).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
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
	"github.com/sihekuang/deskpixie/core/voice/gemini"
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
		// jobs too: the coordinator's requests, which Pixie must not mistake for sessions (#162).
		stub(agent.Jobs{}, `{"jobs":[{"job_id":"job_1","status":"done","task":"Where's the test build coming from?","age_secs":137},`+
			`{"job_id":"job_2","status":"failed","task":"Ask the deskpixie session what the timer case decision is.","age_secs":3040}]}`),
	}
}

// severalCase is one utterance and what Pixie should do with it (#160): hand off everything
// said in one call, with nothing dropped (every word in words appears in the task); when part
// of it is for Pixie's own tools, call them and leave that part out of the task (notWords); when all of it
// is, call them and hand off nothing.
type severalCase struct {
	name, say string
	// notice: something Pixie was told just before (a Coordinator or Session line), if any.
	notice string
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
	// Sessions aren't jobs: the coordinator lists them (#162).
	{name: "sessions-question", calls: 1, words: []string{"session"},
		say: "How many sessions do I have open right now?"},
	// Something from before goes to the coordinator, which answers from memory or asks the
	// session that has it (#164). As on Oct 19, Pixie had just heard 153 mentioned.
	{name: "state-after-mention", calls: 1, words: []string{"153"},
		notice: "Session update: deskpixie: open and merge PR is done: still open: #159, and #153, which #156 may already have fixed; I can close it.",
		say:    "Is issue 153 still relevant?"},
	{name: "memory-question", calls: 1, words: []string{"logo"},
		say: "What did we decide about the logo this morning?"},
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
	// Gemini first: it costs far less, and when it fails the OpenAI runs are skipped
	// (LIVE_ALL_PROVIDERS=1 runs them anyway).
	providers := liveProviders()
	sort.SliceStable(providers, func(i, j int) bool { return providers[i].name == gemini.Name && providers[j].name != gemini.Name })
	geminiFailed := false
	for _, p := range providers {
		passed := t.Run(p.name, func(t *testing.T) {
			if p.name != gemini.Name && geminiFailed && os.Getenv("LIVE_ALL_PROVIDERS") == "" {
				t.Skip("Gemini failed: fix that first (LIVE_ALL_PROVIDERS=1 runs this anyway)")
			}
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
						if c.notice != "" {
							r.rt.Inject(voice.Notice{Text: c.notice})
							r.waitFor("Pixie passing on the notice", 30*time.Second, func() bool { return r.liveSpy.count(voice.EvResponseDone) > 0 })
							time.Sleep(3 * time.Second) // its audio finishes playing
							mu.Lock()
							calls, own = nil, nil // what it did with the notice isn't the case
							mu.Unlock()
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
		if p.name == gemini.Name && !passed {
			geminiFailed = true
		}
	}
}

// recordingAnswer is answer_job with its real description and schema, recording each answer and
// replying as the app does when the session that asked gets it (#161).
type recordingAnswer struct {
	agent.AnswerJob
	mu      *sync.Mutex
	answers *[]string
}

func (a recordingAnswer) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	var v struct {
		Answer string
		JobID  string `json:"job_id"`
	}
	_ = json.Unmarshal(raw, &v)
	a.mu.Lock()
	*a.answers = append(*a.answers, strings.ToLower(v.Answer+" | "+v.JobID))
	a.mu.Unlock()
	return voice.ToolOutcome{Output: json.RawMessage(`{"to":"the session that asked","status":"answered"}`)}
}

// #161, live: Pixie relays a question, the user answers it, and the answer goes through
// answer_job without a job id; Pixie never makes up a confirm word.
func TestLiveAnsweringARelayedQuestion(t *testing.T) {
	secrets := liveSecrets(t)
	instructions := app.Instructions(voice.DefaultPersona(), agent.DelegationPersona)
	lead := make([]int16, audio.SampleRate/2)
	cases := []struct{ name, notice, say string }{
		{"session-asks", "Session update: deskpixie: hand-off tests is asking: should I rebuild it so you can try it, and open the PR?", "Yes, let's do that."},
		{"coordinator-asks", "Coordinator question: The deskpixie session asks whether to make that the rule: Pixie does its own tool part and hands off the rest.", "Yes, make it the rule."},
	}
	providers := liveProviders()
	sort.SliceStable(providers, func(i, j int) bool { return providers[i].name == gemini.Name && providers[j].name != gemini.Name })
	geminiFailed := false
	for _, p := range providers {
		passed := t.Run(p.name, func(t *testing.T) {
			if p.name != gemini.Name && geminiFailed && os.Getenv("LIVE_ALL_PROVIDERS") == "" {
				t.Skip("Gemini failed: fix that first (LIVE_ALL_PROVIDERS=1 runs this anyway)")
			}
			if _, ok := secrets.APIKey(p.name); !ok {
				t.Skip("no key")
			}
			for _, c := range cases {
				speech := liveSpeech(t, c.say)
				t.Run(c.name, func(t *testing.T) {
					var mu sync.Mutex
					var calls []handOff
					var answers, own []string
					reg := voice.NewToolRegistry()
					reg.Register(recordingDelegate{mu: &mu, calls: &calls})
					reg.Register(recordingAnswer{mu: &mu, answers: &answers})
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
					r.rt.Inject(voice.Notice{Text: c.notice})
					r.waitFor("Pixie relaying the question", 30*time.Second, func() bool { return r.liveSpy.count(voice.EvResponseDone) > 0 })
					time.Sleep(3 * time.Second) // its audio finishes playing
					r.say(append(append([]int16(nil), lead...), speech...))
					r.waitFor("an answer_job call", 30*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(answers) > 0 })
					time.Sleep(3 * time.Second)
					r.rt.Hotkey()
					mu.Lock()
					defer mu.Unlock()
					t.Logf("%s; answers %q; delegate calls %+v", r.liveSpy.summary(), answers, calls)
					if len(answers) != 1 || len(calls) != 0 {
						t.Errorf("want one answer_job and no delegate: answers %q, delegate %+v", answers, calls)
					}
					if strings.Contains(strings.ToLower(r.liveSpy.assistant()), "say confirm") {
						t.Errorf("Pixie made up a confirm word: %q", r.liveSpy.assistant())
					}
				})
			}
		})
		if p.name == gemini.Name && !passed {
			geminiFailed = true
		}
	}
}

// TestLiveNoMadeUpConfirmWord (#166): the voice log's 22:09 notice, a question with no confirm
// word, which Pixie relayed with "Say confirm hawk." added. With the claim check on as in the
// app, Pixie's last words must not ask for a confirm word: either it never makes one up, or the
// app's correction gets it to drop it. Gemini only: this is the voice model's own behavior.
//
//	go test -count=1 -tags live -run TestLiveNoMadeUpConfirmWord -v ./voice/
func TestLiveNoMadeUpConfirmWord(t *testing.T) {
	secrets := liveSecrets(t)
	if _, ok := secrets.APIKey(gemini.Name); !ok {
		t.Skip("no Gemini key")
	}
	var p liveProvider
	for _, lp := range liveProviders() {
		if lp.name == gemini.Name {
			p = lp
		}
	}
	notice := "Coordinator question: The deskpixie session never answered whether everything is committed, and it's busy now. Want me to check the repo's git status myself?"
	sayConfirm := regexp.MustCompile(`(?i)\bconfirm\s+[a-z]+`)
	for run := 1; run <= 3; run++ {
		t.Run(fmt.Sprint("run-", run), func(t *testing.T) {
			var mu sync.Mutex
			var calls []handOff
			var answers, own []string
			reg := voice.NewToolRegistry()
			reg.Register(recordingDelegate{mu: &mu, calls: &calls})
			reg.Register(recordingAnswer{mu: &mu, answers: &answers})
			for _, o := range ownTools(&mu, &own) {
				reg.Register(o)
			}
			cfg := p.cfg
			cfg.Instructions = app.Instructions(voice.DefaultPersona(), agent.DelegationPersona)
			conv := voice.DefaultConversationConfig()
			conv.CheckClaims, conv.HandOff, conv.ClaimTools = true, true, voice.ClaimTools
			conv.IdleTimeout = 30 * time.Second
			r := newLiveRigConv(t, p.model(&liveWireLog{}, t), cfg, secrets, reg, conv)
			r.rt.Hotkey()
			if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
				return
			}
			r.rt.Inject(voice.Notice{Text: notice})
			r.waitFor("Pixie relaying the question", 30*time.Second, func() bool { return r.liveSpy.count(voice.EvResponseDone) > 0 })
			time.Sleep(12 * time.Second) // a correction, if one was needed, and Pixie's reply to it
			// And how it takes the correction itself, sent as if it had made one up.
			done := r.liveSpy.count(voice.EvResponseDone)
			r.rt.Inject(voice.Notice{Text: voice.MadeUpConfirmFix})
			r.waitFor("Pixie's reply to the correction", 30*time.Second, func() bool { return r.liveSpy.count(voice.EvResponseDone) > done })
			time.Sleep(3 * time.Second)
			r.rt.Hotkey()
			r.liveSpy.mu.Lock()
			said := append([]string(nil), r.liveSpy.said...)
			r.liveSpy.mu.Unlock()
			t.Logf("Pixie said %q", said)
			if len(said) == 0 {
				t.Fatal("Pixie said nothing")
			}
			if last := said[len(said)-1]; sayConfirm.MatchString(last) || strings.Contains(strings.ToLower(last), "correction") {
				t.Errorf("after the correction Pixie asks for a confirm word or reads the correction out: %q", last)
			}
		})
	}
}

// recordingFocus is focus_session with its real description and schema, recording each name and
// replying as the app does when it shows the session.
type recordingFocus struct {
	agent.FocusSessionTool
	mu    *sync.Mutex
	names *[]string
}

func (f recordingFocus) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	var v struct{ Name string }
	_ = json.Unmarshal(raw, &v)
	f.mu.Lock()
	*f.names = append(*f.names, v.Name)
	f.mu.Unlock()
	out, _ := json.Marshal(map[string]any{"name": v.Name, "status": "shown"})
	return voice.ToolOutcome{Output: out}
}

// TestLiveFocusingASession (#170): "show me the arbitrage trader session", with the user's
// sessions in Pixie's memory as the app lists them, is one focus_session call with that
// session's name, and no hand-off. Gemini only: this is the voice model's own behavior.
//
//	go test -count=1 -tags live -run TestLiveFocusingASession -v ./voice/
func TestLiveFocusingASession(t *testing.T) {
	secrets := liveSecrets(t)
	if _, ok := secrets.APIKey(gemini.Name); !ok {
		t.Skip("no Gemini key")
	}
	var p liveProvider
	for _, lp := range liveProviders() {
		if lp.name == gemini.Name {
			p = lp
		}
	}
	sessions := agent.YourSessionsFacts([]string{
		"deskpixie-claude: deskpixie: design focus-a-window feature (Warp)",
		"arbitrage-trader-claude: arbitrage pair families (Warp)",
		"api-claude: claude in api (iTerm)",
	})
	var mu sync.Mutex
	var calls []handOff
	var names, own []string
	reg := voice.NewToolRegistry()
	reg.Register(recordingDelegate{mu: &mu, calls: &calls})
	reg.Register(recordingFocus{mu: &mu, names: &names})
	for _, o := range ownTools(&mu, &own) {
		reg.Register(o)
	}
	cfg := p.cfg
	cfg.Instructions = app.Instructions(voice.DefaultPersona(), agent.DelegationPersona) + "\n\n" + sessions
	speech := liveSpeech(t, "Can you show me the arbitrage trader session?")
	r := newLiveRigWith(t, p.model(&liveWireLog{}, t), cfg, secrets, reg)
	r.rt.Hotkey()
	if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
		return
	}
	r.say(append(make([]int16, audio.SampleRate/2), speech...))
	r.waitFor("a focus_session call", 30*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(names) > 0 })
	time.Sleep(3 * time.Second)
	r.rt.Hotkey()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("%s; focus %q; delegate calls %+v", r.liveSpy.summary(), names, calls)
	if !slices.Equal(names, []string{"arbitrage-trader-claude"}) || len(calls) != 0 {
		t.Errorf("want one focus_session(arbitrage-trader-claude) and no delegate: focus %q, delegate %+v", names, calls)
	}
}
