//go:build live

package voice_test

// The GPT-Live agent-mode benchmark against the REAL coordinator (#70 follow-up): Claude Code
// with --model sonnet, on the dedicated herdr smoke session ("deskpixie-smoke", never the app's
// "deskpixie"), temp app data and projects root, a free hook port (never 48217). Same scenarios
// as bench_live_test.go, voice.liveDelegation "agent". Run with:
//
//	BENCH_SONNET=1 BENCH_OUT=../docs/parity/gpt-live-delegation-bench-sonnet.json \
//	  go test -tags live -run TestLiveBenchGPTLiveSonnet -v -timeout 120m ./voice/
//
// BENCH_N (default 2), BENCH_ONLY, BENCH_MAX_SECONDS (GPT-Live seconds budget, default 2400 ≈
// $2: the run stops early past it). Differences from the real app, so replies are heard in the
// session that asked: delegate's EndAfterReply is cleared (the conversation stays open until the
// reply has been spoken, at most replyWait), and each scenario's open jobs are cancelled before
// the next. Permission questions are never confirmed (they time out denied), so the coordinator
// can't change anything. Workers are off.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/openailive"
)

const (
	replyWait  = 100 * time.Second // the session waits this long (from the task) for the coordinator's reply
	replyAfter = 60 * time.Second  // then, closed, this much longer to record it
)

// sonnetChecks: what a relevant reply mentions (a relevance check, not a truth check).
var sonnetChecks = map[string]string{
	"weather": `degree|°|rain|cloud|sun|forecast|weather`, "timer": `timer|minute`, "math": `391|three hundred (and )?ninety[- ]one`,
	"percent": `\b12\b|twelve`, "music": `paus|music`, "build": `build`, "worker": `worker`,
	"jobs": `job|nothing|no .*running|idle`, "email": `sam`, "news": `mars`, "calendar": `calendar`,
	"flaky": `login|test`, "repo": `test|repo`,
}

// agentVoice is the voice loop as the agent sees it (app.AgentVoice): the runtime, with every
// line the agent gives it timestamped.
type agentVoice struct {
	rt    *voice.Runtime
	mu    sync.Mutex
	lines []agentLine
}

type agentLine struct {
	At      time.Time
	Text    string
	Bounced bool // arrived as a notification: no conversation was open
}

func (v *agentVoice) Inject(n voice.Notice) {
	v.mu.Lock()
	v.lines = append(v.lines, agentLine{At: time.Now(), Text: n.Text})
	v.mu.Unlock()
	v.rt.Inject(n)
}
func (v *agentVoice) HoldOpen(on bool)                       { v.rt.HoldOpen(on) }
func (v *agentVoice) Open() bool                             { return false } // never opens one on its own
func (v *agentVoice) Subscribe() <-chan voice.VoiceSignal    { return v.rt.Subscribe() }
func (v *agentVoice) Unsubscribe(c <-chan voice.VoiceSignal) { v.rt.Unsubscribe(c) }
func (v *agentVoice) note(title, body string) {
	v.mu.Lock()
	v.lines = append(v.lines, agentLine{At: time.Now(), Text: title + ": " + body, Bounced: true})
	v.mu.Unlock()
}
func (v *agentVoice) snapshot() []agentLine {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.lines)
}

// SonnetTurn extends BenchTurn with the coordinator's side.
type SonnetTurn struct {
	BenchTurn
	ReplyMs      int64    `json:"reply_ms"`       // end of user audio → first Agent reply/question line
	ProgressMs   int64    `json:"progress_ms"`    // → first Agent progress line
	ReplyAudioMs int64    `json:"reply_audio_ms"` // → Bit's first audio after that line was handed to GPT-Live
	ReplyKind    string   `json:"reply_kind,omitempty"`
	Reply        string   `json:"reply,omitempty"`
	ReplyBounced bool     `json:"reply_bounced,omitempty"`
	ReplySpoken  string   `json:"reply_spoken,omitempty"` // Bit's words after the reply
	ReplyOK      *bool    `json:"reply_ok,omitempty"`
	Clarifying   bool     `json:"clarifying,omitempty"`
	Permission   bool     `json:"permission,omitempty"`
	TimedOut     bool     `json:"timed_out,omitempty"`
	Lines        []string `json:"agent_lines,omitempty"`
}

func TestLiveBenchGPTLiveSonnet(t *testing.T) {
	if os.Getenv("BENCH_SONNET") == "" {
		t.Skip("set BENCH_SONNET=1")
	}
	secrets := liveSecrets(t)
	if _, ok := secrets.APIKey(openailive.Name); !ok {
		t.Skip("no OPENAI_API_KEY")
	}
	n := 2
	if v, err := strconv.Atoi(os.Getenv("BENCH_N")); err == nil && v > 0 {
		n = v
	}
	budget := 2400.0
	if v, err := strconv.ParseFloat(os.Getenv("BENCH_MAX_SECONDS"), 64); err == nil && v > 0 {
		budget = v
	}
	var only map[string]bool
	if v := os.Getenv("BENCH_ONLY"); v != "" {
		only = map[string]bool{}
		for _, id := range strings.Split(v, ",") {
			only[id] = true
		}
	}

	// The agent on the smoke session.
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("never the app's port")
	}
	s := settings.Default().Agent
	s.Session, s.Port = smoketest.SmokeSession, port
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled, s.Warm, s.KeepPanesOnQuit = false, true, false
	s.OpenConversationForUpdates = false
	sonnet := "sonnet"
	s.Model = &sonnet
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	av := &agentVoice{}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ,
		Hooks:  app.AgentHooks{Notify: func(title, body string) { av.note(title, body) }},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	defer a.Shutdown()

	// The voice loop: agent mode, the agent's tools (delegate without EndAfterReply), recorded.
	bt := &benchTools{}
	reg := voice.DefaultTools()
	var jobsTool voice.Tool
	for _, tool := range a.Tools() {
		tool := tool
		if tool.Name() == "jobs" {
			jobsTool = tool
		}
		reg.Register(benchTool{inner: tool, b: bt, fn: func(raw json.RawMessage) voice.ToolOutcome {
			o := tool.Invoke(context.Background(), raw)
			o.EndAfterReply = false
			return o
		}})
	}
	tap := &wireTap{}
	url := liveProxy(t, tap)
	r := &benchRig{stop: make(chan struct{}), done: make(chan struct{})}
	conv := voice.DefaultConversationConfig()
	conv.HandOff, conv.CheckClaims = true, true
	r.rt = voice.NewRuntime(benchModel{Model: openailive.New(url), r: r}, nil, voice.RuntimeConfig{
		Session: voice.SessionConfig{Model: openailive.DefaultModel, Voice: openailive.DefaultVoice,
			Instructions: app.Instructions(voice.Instructions(""), a.Persona()), ClientDelegation: true},
		Secrets: secrets, Tools: reg, MouthLevels: 4, Conversation: conv,
		Facts: tools.LocalFacts{Clock: tools.SystemClock{}, Home: "Paris, France"},
	}, nil)
	av.rt = r.rt
	a.AttachVoice(av)
	go r.loop()
	defer func() { close(r.stop); <-r.done; r.rt.Close() }()

	// Warm up: the coordinator's first answer (not measured).
	warm := time.Now()
	for _, tool := range a.Tools() {
		if tool.Name() == voice.DelegateTool {
			t.Logf("warm-up: %s", tool.Invoke(context.Background(), json.RawMessage(`{"task":"Reply with the word ready and nothing else."}`)).Output)
		}
	}
	for !slices.ContainsFunc(av.snapshot(), func(l agentLine) bool { return hasLine(l.Text, "reply") || l.Bounced }) {
		if time.Since(warm) > 4*time.Minute {
			t.Fatalf("the coordinator never answered the warm-up: %+v tray %+v", av.snapshot(), a.Tray())
		}
		time.Sleep(time.Second)
	}
	t.Logf("warm-up took %v", time.Since(warm).Round(time.Second))

	speech := map[string][]int16{}
	var all []SonnetTurn
	billed := 0.0
	save := func() {
		if out := os.Getenv("BENCH_OUT"); out != "" {
			b, _ := json.MarshalIndent(all, "", " ")
			_ = os.WriteFile(out, b, 0o644)
		}
	}
	for rep := 1; rep <= n; rep++ {
		for _, sc := range benchScenarios() {
			if only != nil && !only[sc.ID] {
				continue
			}
			if billed > budget {
				t.Logf("GPT-Live budget reached (%.0f s): stopping", billed)
				save()
				fmt.Println(sonnetSummary(all))
				return
			}
			for _, tn := range sc.Turns {
				if speech[tn.Say] == nil {
					speech[tn.Say] = liveSpeech(t, tn.Say)
				}
			}
			turns := runSonnetScenario(t, r, tap, bt, av, jobsTool, sc, speech)
			for i := range turns {
				turns[i].Rep = rep
				billed += turns[i].SessionSeconds
			}
			all = append(all, turns...)
			save()
			last := turns[len(turns)-1]
			t.Logf("rep %d %-9s route=%s task=%q reply=%d ms (%s) audio=%d ms %q | said %q", rep, sc.ID, last.Route, last.Task,
				last.ReplyMs, last.ReplyKind, last.ReplyAudioMs, trunc(last.Reply, 120), trunc(last.ReplySpoken, 120))
		}
	}
	t.Logf("GPT-Live billed %.0f s", billed)
	fmt.Println(sonnetSummary(all))
}

// hasLine: text is a coordinator line of kind ("reply", "question", …), new or old lead-in.
func hasLine(text, kind string) bool {
	return strings.HasPrefix(text, "Coordinator "+kind+":") || strings.HasPrefix(text, "Agent "+kind+":")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func runSonnetScenario(t *testing.T, r *benchRig, tap *wireTap, bt *benchTools, av *agentVoice, jobsTool voice.Tool,
	sc benchScenario, speech map[string][]int16) []SonnetTurn {
	tap.reset()
	r.mu.Lock()
	r.evs = nil
	r.mu.Unlock()
	scStart := time.Now()
	r.rt.Hotkey()
	for deadline := time.Now().Add(15 * time.Second); !r.rt.IsOpen(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			return []SonnetTurn{{BenchTurn: BenchTurn{Mode: "agent-sonnet", Scenario: sc.ID, Errors: []string{"never opened"}}}}
		}
	}
	time.Sleep(500 * time.Millisecond)
	var ends []time.Time
	var starts []time.Time
	for i, tn := range sc.Turns {
		starts = append(starts, time.Now())
		end := r.say(speech[tn.Say])
		ends = append(ends, end)
		wait := 10 * time.Second
		if i < len(sc.Turns)-1 {
			wait = 7 * time.Second
		}
		for r.rt.IsOpen() && time.Since(end) < 20*time.Second {
			if time.Since(end) >= wait && time.Since(r.lastAudio()) >= 1500*time.Millisecond {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !r.rt.IsOpen() {
			break
		}
	}
	// Agent work: wait for the coordinator's reply (or question) and Bit saying it.
	var taskAt time.Time
	for _, c := range bt.snapshot() {
		if c.At.After(scStart) && agentTools[c.Tool] && c.Tool != "jobs" {
			taskAt = c.At
			break
		}
	}
	isReply := func(l agentLine) bool {
		return l.At.After(taskAt) && (hasLine(l.Text, "reply") || hasLine(l.Text, "question") ||
			hasLine(l.Text, "says") || (l.Bounced && !strings.Contains(l.Text, "progress")))
	}
	timedOut := false
	if !taskAt.IsZero() {
		for !slices.ContainsFunc(av.snapshot(), isReply) {
			if time.Since(taskAt) > replyWait {
				timedOut = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !timedOut {
			// Bit says it: wait for its audio to start and stop (at most 30 s).
			got := time.Now()
			for time.Since(got) < 30*time.Second && r.rt.IsOpen() {
				la := r.lastAudio()
				if la.After(got) && time.Since(la) >= 2*time.Second {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	if r.rt.IsOpen() {
		r.rt.Hotkey()
	}
	for closeBy := time.Now().Add(3 * time.Second); time.Now().Before(closeBy); time.Sleep(50 * time.Millisecond) {
		if slices.ContainsFunc(tap.snapshot(), func(e wireEvent) bool { return e.Type == "session.closed" }) {
			break
		}
	}
	if timedOut {
		for extra := time.Now(); time.Since(extra) < replyAfter && !slices.ContainsFunc(av.snapshot(), isReply); {
			time.Sleep(time.Second)
		}
	}
	// Stop whatever is still running before the next scenario.
	if jobsTool != nil {
		for i := 0; i < 5; i++ {
			type jobRow struct {
				JobID  string `json:"job_id"`
				Status string `json:"status"`
			}
			var st struct {
				Jobs []jobRow `json:"jobs"`
			}
			_ = json.Unmarshal(jobsTool.Invoke(context.Background(), json.RawMessage(`{"action":"status"}`)).Output, &st)
			open := slices.DeleteFunc(st.Jobs, func(j jobRow) bool {
				return j.Status != "delegated" && j.Status != "working" && j.Status != "question"
			})
			if len(open) == 0 {
				break
			}
			for _, j := range open {
				jobsTool.Invoke(context.Background(), json.RawMessage(`{"action":"cancel","job_id":"`+j.JobID+`"}`))
			}
			time.Sleep(3 * time.Second)
		}
	}

	evs, wire, calls, lines := r.events(), tap.snapshot(), bt.snapshot(), av.snapshot()
	var out []SonnetTurn
	for i := range ends {
		tn := sc.Turns[i]
		next := time.Now().Add(time.Hour)
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		in := func(at time.Time) bool { return !at.Before(starts[i]) && at.Before(next) }
		after := func(at time.Time) int64 {
			if at.IsZero() {
				return -1
			}
			return (at.Sub(ends[i])).Milliseconds()
		}
		st := SonnetTurn{BenchTurn: BenchTurn{Mode: "agent-sonnet", Scenario: sc.ID, Kind: sc.Kind, Turn: i + 1, Say: tn.Say,
			Expect: tn.Agent, DelegationMs: -1, ToolCallMs: -1, TaskMs: -1, AudioMs: -1}, ReplyMs: -1, ProgressMs: -1, ReplyAudioMs: -1}
		var user, said []string
		for _, e := range evs {
			if !in(e.At) {
				continue
			}
			switch e.Kind {
			case voice.EvToolCall:
				st.ToolCalls = append(st.ToolCalls, e.Tool)
				if st.ToolCallMs < 0 {
					st.ToolCallMs = after(e.At)
				}
			case voice.EvAudioDelta:
				if st.AudioMs < 0 && !e.At.Before(ends[i]) {
					st.AudioMs = after(e.At)
				}
			case voice.EvTranscript:
				if e.Role == "user" {
					user = append(user, e.Text)
				} else {
					said = append(said, e.Text)
				}
			case voice.EvError:
				st.Errors = append(st.Errors, e.Text)
			}
		}
		st.User, st.Assistant = strings.Join(user, " | "), strings.Join(said, " | ")
		for _, w := range wire {
			if in(w.At) && w.Dir == "s" && w.Type == "session.delegation.created" && st.DelegationMs < 0 {
				st.DelegationMs = after(w.At)
			}
		}
		toAgent := false
		var turnTask time.Time
		for _, c := range calls {
			if !in(c.At) {
				continue
			}
			st.Invoked = append(st.Invoked, c.Tool)
			if agentTools[c.Tool] && !toAgent {
				toAgent, turnTask = true, c.At
				st.TaskMs = after(c.At)
				st.Task, _ = c.Args["task"].(string)
				if st.Task == "" {
					st.Task, _ = c.Args["answer"].(string)
				}
				st.Context, _ = c.Args["context"].(string)
			}
		}
		st.Route = routeNone
		if toAgent {
			st.Route = routeAgent
		}
		st.RouteOK = st.Expect == routeAny || st.Route == st.Expect
		if tn.Answer != "" && !toAgent {
			p := tn.Answer
			if p == "TIME" {
				p = timePattern(ends[i])
			}
			ok := regexp.MustCompile("(?i)" + p).MatchString(st.Assistant)
			st.AnswerOK = &ok
		}
		if toAgent {
			for _, l := range lines {
				if l.At.Before(turnTask) {
					continue
				}
				st.Lines = append(st.Lines, trunc(l.Text, 300))
				switch {
				case hasLine(l.Text, "progress"):
					if st.ProgressMs < 0 {
						st.ProgressMs = after(l.At)
					}
				case st.ReplyMs < 0 && (hasLine(l.Text, "reply") || hasLine(l.Text, "question") ||
					hasLine(l.Text, "says") || l.Bounced):
					st.ReplyMs, st.Reply, st.ReplyBounced = after(l.At), l.Text, l.Bounced
					st.ReplyKind = "reply"
					if hasLine(l.Text, "question") {
						st.ReplyKind, st.Clarifying = "question", !strings.Contains(l.Text, "Say confirm")
						st.Permission = strings.Contains(l.Text, "Say confirm")
					}
					// The notice reaches GPT-Live as a commentary append; Bit's next audio says it.
					var sent time.Time
					for _, w := range wire {
						if w.Dir == "c" && w.Type == "session.commentary.append" && w.At.After(l.At.Add(-10*time.Millisecond)) {
							sent = w.At
							break
						}
					}
					if !sent.IsZero() {
						var words []string
						for _, e := range evs {
							if e.At.After(sent) && e.Kind == voice.EvAudioDelta && st.ReplyAudioMs < 0 {
								st.ReplyAudioMs = after(e.At)
							}
							if e.At.After(sent) && e.Kind == voice.EvTranscript && e.Role == "assistant" {
								words = append(words, e.Text)
							}
						}
						st.ReplySpoken = strings.Join(words, " | ")
					}
				}
			}
			if pat, ok := sonnetChecks[sc.ID]; ok && st.Reply != "" {
				v := regexp.MustCompile("(?i)" + pat).MatchString(st.Reply)
				st.ReplyOK = &v
			}
			st.TimedOut = st.ReplyMs < 0
		}
		out = append(out, st)
	}
	last := &out[len(out)-1]
	for _, w := range wire {
		if w.Raw != nil && (w.Type == "session.closed" || w.Type == "session.usage.updated") {
			var v struct {
				Usage struct {
					Seconds float64 `json:"seconds"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(w.Raw, &v)
			last.SessionSeconds = max(last.SessionSeconds, v.Usage.Seconds)
		}
	}
	for i := len(ends); i < len(sc.Turns); i++ {
		out = append(out, SonnetTurn{BenchTurn: BenchTurn{Mode: "agent-sonnet", Scenario: sc.ID, Kind: sc.Kind, Turn: i + 1,
			Say: sc.Turns[i].Say, NotReached: true, Route: "-", Expect: sc.Turns[i].Agent}, ReplyMs: -1, ProgressMs: -1, ReplyAudioMs: -1})
	}
	return out
}

func sonnetSummary(turns []SonnetTurn) string {
	var b strings.Builder
	var task, reply, progress, audio, replyAudio []int64
	var routed, routeOK, n, replies, ok, okN, clar, perm, timeouts, bounced int
	secs := 0.0
	for _, t := range turns {
		secs += t.SessionSeconds
		if t.NotReached {
			continue
		}
		n++
		if t.RouteOK {
			routeOK++
		}
		if t.AudioMs >= 0 {
			audio = append(audio, t.AudioMs)
		}
		if t.Route != routeAgent {
			continue
		}
		routed++
		if t.TaskMs >= 0 {
			task = append(task, t.TaskMs)
		}
		if t.ProgressMs >= 0 {
			progress = append(progress, t.ProgressMs)
		}
		if t.ReplyMs >= 0 {
			replies++
			reply = append(reply, t.ReplyMs)
		}
		if t.ReplyAudioMs >= 0 {
			replyAudio = append(replyAudio, t.ReplyAudioMs)
		}
		if t.ReplyOK != nil {
			okN++
			if *t.ReplyOK {
				ok++
			}
		}
		if t.Clarifying {
			clar++
		}
		if t.Permission {
			perm++
		}
		if t.TimedOut {
			timeouts++
		}
		if t.ReplyBounced {
			bounced++
		}
	}
	fmt.Fprintf(&b, "turns %d (route as labelled %d), to the coordinator %d, replies %d (relevant %d/%d), clarifying questions %d, permission questions %d, no reply in %v %d, bounced %d\n",
		n, routeOK, routed, replies, ok, okN, clar, perm, replyWait+replyAfter, timeouts, bounced)
	fmt.Fprintf(&b, "p50/p90 ms: first audio %s/%s, task %s/%s, first progress %s/%s, reply %s/%s, reply audio %s/%s; GPT-Live %.0f s\n",
		pct(audio, .5), pct(audio, .9), pct(task, .5), pct(task, .9), pct(progress, .5), pct(progress, .9),
		pct(reply, .5), pct(reply, .9), pct(replyAudio, .5), pct(replyAudio, .9), secs)
	return b.String()
}
