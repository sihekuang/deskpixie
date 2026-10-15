//go:build live

package voice_test

// GPT-Live delegation benchmark (#70): voice.liveDelegation "backend" (Responses delegation to
// a backend model that runs Bit's tools) against "agent" (client delegation straight to the
// agent). A real voice.Runtime with the openailive adapter talks to the real GPT-Live through a
// local proxy that timestamps the wire; the agent is FAKE (it records the task and replies after
// a fixed delay), so this measures GPT-Live and the routing, not Claude. Run with:
//
//	BENCH_GPTLIVE=1 BENCH_OUT=../docs/parity/gpt-live-delegation-bench.json \
//	  go test -tags live -run TestLiveBenchGPTLiveDelegation -v -timeout 90m ./voice/
//
// BENCH_N (default 3) repetitions per mode, BENCH_MODES (default "backend,agent"), BENCH_ONLY
// (comma-separated scenario ids), BENCH_BACKEND_MODEL (default settings' gpt-6-luna). Each
// scenario is its own session (~12 s, ~$0.01 of GPT-Live time). The key is never printed.
// BENCH_SUMMARY=<json> prints the summary of an earlier run without calling the API.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/openailive"
)

// Route categories.
const (
	routeAgent = "agent" // delegate / jobs / answer_job / recall, or a client delegation
	routeNone  = "none"  // answered without any tool or delegation
	routeAny   = "any"   // either is fine (excluded from precision/recall)
)

func local(tool string) string { return "local:" + tool }

type benchTurn struct {
	Say string
	// Expect per mode: routeAgent, routeNone, routeAny or local:<tool>.
	Backend, Agent string
	// Keywords: each group needs one of its words in the task+context (agent-routed turns).
	Keywords [][]string
	// Answer: a regexp the assistant's words must match (quick answers); "TIME" = the clock.
	Answer string
	Note   string
}

type benchScenario struct {
	ID, Kind string
	Turns    []benchTurn
	// Ends: the conversation should end by itself (a goodbye).
	Ends bool
}

func benchScenarios() []benchScenario {
	agentWork := func(id, say string, kw ...[]string) benchScenario {
		return benchScenario{ID: id, Kind: "agent work", Turns: []benchTurn{{Say: say, Backend: routeAgent, Agent: routeAgent, Keywords: kw}}}
	}
	return []benchScenario{
		{ID: "time", Kind: "quick", Turns: []benchTurn{{Say: "What time is it?", Backend: local("get_time"), Agent: routeNone, Answer: "TIME"}}},
		{ID: "weather", Kind: "quick", Turns: []benchTurn{{Say: "What's the weather in Paris right now?", Backend: local("get_weather"), Agent: routeAgent,
			Keywords: [][]string{{"weather"}, {"paris"}}, Answer: `17|seventeen`, Note: "backend answer from the fake weather tool; agent: the fake agent reply"}}},
		{ID: "timer", Kind: "quick", Turns: []benchTurn{{Say: "Set a timer for ten minutes.", Backend: local("timer"), Agent: routeAgent, Keywords: [][]string{{"timer"}, {"ten", "10"}}}}},
		{ID: "math", Kind: "quick", Turns: []benchTurn{{Say: "What is seventeen times twenty-three?", Backend: local("calculate"), Agent: routeAny, Answer: `391|three (hundred (and )?)?ninety[- ]one`}}},
		{ID: "percent", Kind: "quick", Turns: []benchTurn{{Say: "What's fifteen percent of eighty?", Backend: local("calculate"), Agent: routeAny, Answer: `\b12\b|twelve`}}},
		{ID: "music", Kind: "quick", Turns: []benchTurn{{Say: "Pause the music.", Backend: local("media"), Agent: routeAgent, Keywords: [][]string{{"pause"}, {"music"}}}}},
		agentWork("build", "Check why the build failed.", []string{"build"}, []string{"fail"}),
		agentWork("worker", "Start a worker to fix the flaky login tests in the desk pixie repo.", []string{"worker"}, []string{"login"}, []string{"pixie"}),
		{ID: "jobs", Kind: "agent work", Turns: []benchTurn{{Say: "What's the status of my jobs?", Backend: routeAgent, Agent: routeAgent,
			Keywords: [][]string{{"job"}, {"status"}}, Note: "backend: the jobs tool; agent: a delegated question"}}},
		agentWork("email", "Can you write a short email to Sam saying I'll be late?", []string{"email"}, []string{"sam"}, []string{"late"}),
		agentWork("news", "Look up the latest news about the Mars mission.", []string{"news"}, []string{"mars"}),
		agentWork("calendar", "What's on my calendar tomorrow?", []string{"calendar"}, []string{"tomorrow"}),
		{ID: "flaky", Kind: "multi-turn", Turns: []benchTurn{
			{Say: "The login tests were flaky yesterday.", Backend: routeAny, Agent: routeAny, Note: "a statement: either is fine"},
			{Say: "Can you look into it?", Backend: routeAgent, Agent: routeAgent, Keywords: [][]string{{"login"}, {"flaky", "test"}}},
		}},
		{ID: "repo", Kind: "multi-turn", Turns: []benchTurn{
			{Say: "I'm working on the desk pixie repo today.", Backend: routeAny, Agent: routeAny},
			{Say: "Run its tests and tell me what fails.", Backend: routeAgent, Agent: routeAgent, Keywords: [][]string{{"pixie"}, {"test"}}},
		}},
		{ID: "capital", Kind: "general knowledge", Turns: []benchTurn{{Say: "What is the capital of Australia?", Backend: routeNone, Agent: routeNone, Answer: `(?i)canberra`}}},
		{ID: "austen", Kind: "general knowledge", Turns: []benchTurn{{Say: "Who wrote Pride and Prejudice?", Backend: routeNone, Agent: routeNone, Answer: `(?i)austen`}}},
		{ID: "spider", Kind: "general knowledge", Turns: []benchTurn{{Say: "How many legs does a spider have?", Backend: routeNone, Agent: routeNone, Answer: `(?i)\b8\b|eight`}}},
		{ID: "howareyou", Kind: "small talk", Turns: []benchTurn{{Say: "How are you doing today?", Backend: routeNone, Agent: routeNone}}},
		{ID: "joke", Kind: "small talk", Turns: []benchTurn{{Say: "Tell me a joke.", Backend: routeNone, Agent: routeNone}}},
		{ID: "thanks", Kind: "small talk", Turns: []benchTurn{{Say: "Thanks, that's really helpful.", Backend: routeNone, Agent: routeNone}}},
		{ID: "bye", Kind: "goodbye", Ends: true, Turns: []benchTurn{{Say: "Thanks, that's all. Bye!", Backend: local("end_conversation"), Agent: routeNone,
			Note: "agent mode has no end_conversation: the transcript goodbye check makes the call up"}}},
	}
}

// ---------- wire proxy ----------

type wireEvent struct {
	At     time.Time       `json:"-"`
	Dir    string          `json:"dir"` // "s" server, "c" client
	Type   string          `json:"type"`
	Target string          `json:"target,omitempty"`
	Nested string          `json:"nested,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

type wireTap struct {
	mu     sync.Mutex
	events []wireEvent
}

func (w *wireTap) add(e wireEvent) {
	w.mu.Lock()
	w.events = append(w.events, e)
	w.mu.Unlock()
}

func (w *wireTap) snapshot() []wireEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

func (w *wireTap) reset() {
	w.mu.Lock()
	w.events = nil
	w.mu.Unlock()
}

// liveProxy forwards the adapter's WebSocket to GPT-Live unchanged (the Authorization header
// passes through in memory, never logged) and timestamps every non-audio message.
func liveProxy(t *testing.T, tap *wireTap) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up, _, err := websocket.Dial(r.Context(), openailive.DefaultURL,
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {r.Header.Get("Authorization")}}})
		if err != nil {
			http.Error(w, "upstream dial failed", http.StatusBadGateway)
			return
		}
		defer up.CloseNow()
		up.SetReadLimit(16 << 20)
		down, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer down.CloseNow()
		down.SetReadLimit(16 << 20)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		record := func(dir string, b []byte) {
			var m struct {
				Type       string `json:"type"`
				Delegation struct {
					Target string `json:"target"`
				} `json:"delegation"`
				Event struct {
					Type string `json:"type"`
				} `json:"event"`
			}
			_ = json.Unmarshal(b, &m)
			if strings.HasSuffix(m.Type, "audio.append") || strings.HasSuffix(m.Type, "audio.delta") || strings.HasSuffix(m.Type, "transcript.delta") {
				return
			}
			e := wireEvent{At: time.Now(), Dir: dir, Type: m.Type, Target: m.Delegation.Target, Nested: m.Event.Type}
			if m.Type == "session.closed" || m.Type == "error" || m.Type == "session.usage.updated" ||
				(m.Type == "response.event" && (m.Event.Type == "response.completed" || m.Event.Type == "response.failed" || m.Event.Type == "response.done")) ||
				(dir == "c" && strings.HasPrefix(m.Type, "session.") && strings.HasSuffix(m.Type, ".append")) {
				e.Raw = append(json.RawMessage(nil), b...)
			}
			tap.add(e)
		}
		go func() {
			defer cancel()
			for {
				typ, b, err := up.Read(ctx)
				if err != nil {
					return
				}
				record("s", b)
				if down.Write(ctx, typ, b) != nil {
					return
				}
			}
		}()
		for {
			typ, b, err := down.Read(ctx)
			if err != nil {
				return
			}
			record("c", b)
			if up.Write(ctx, typ, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/live/sessions"
}

// ---------- fake agent and tools ----------

type benchCall struct {
	At   time.Time
	Tool string
	Args map[string]any
}

type benchTools struct {
	mu    sync.Mutex
	calls []benchCall
	rt    func() *voice.Runtime
	seq   int
}

func (b *benchTools) record(tool string, raw json.RawMessage) {
	var a map[string]any
	_ = json.Unmarshal(raw, &a)
	b.mu.Lock()
	b.calls = append(b.calls, benchCall{At: time.Now(), Tool: tool, Args: a})
	b.mu.Unlock()
}

func (b *benchTools) snapshot() []benchCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

// benchTool records every call and answers with fn's outcome.
type benchTool struct {
	inner voice.Tool // name, description and parameters (the real tool's)
	b     *benchTools
	fn    func(json.RawMessage) voice.ToolOutcome
}

func (t benchTool) Name() string                { return t.inner.Name() }
func (t benchTool) Description() string         { return t.inner.Description() }
func (t benchTool) Parameters() json.RawMessage { return t.inner.Parameters() }
func (t benchTool) Invoke(ctx context.Context, raw json.RawMessage) voice.ToolOutcome {
	t.b.record(t.inner.Name(), raw)
	if t.fn == nil {
		return t.inner.Invoke(ctx, raw)
	}
	return t.fn(raw)
}

const fakeReplyDelay = 3 * time.Second

func benchRegistry(b *benchTools) *voice.ToolRegistry {
	reg := voice.NewToolRegistry()
	om := tools.NewOpenMeteo()
	ok := func(v any) voice.ToolOutcome { j, _ := json.Marshal(v); return voice.ToolOutcome{Output: j} }
	reg.Register(benchTool{inner: voice.EndConversation{}, b: b})
	reg.Register(benchTool{inner: app.AdaptTool(tools.GetTime{Clock: tools.SystemClock{}, Places: om}), b: b})
	reg.Register(benchTool{inner: app.AdaptTool(tools.Calculate{}), b: b})
	reg.Register(benchTool{inner: app.AdaptTool(tools.GetWeather{Places: om, Sky: om}), b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"place": "Paris, France", "temperature_c": 17, "conditions": "light rain", "wind_kmh": 12})
	}})
	reg.Register(benchTool{inner: app.AdaptTool(tools.TimerTool{}), b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"ok": true, "timer": "10 minutes", "ends_in": "10 minutes"})
	}})
	reg.Register(benchTool{inner: app.AdaptTool(tools.Media{}), b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"ok": true, "state": "paused"})
	}})
	// The fake agent: a job id at once, an "Agent reply:" after fakeReplyDelay.
	reg.Register(benchTool{inner: agent.Delegate{}, b: b, fn: func(raw json.RawMessage) voice.ToolOutcome {
		var a struct{ Task string }
		_ = json.Unmarshal(raw, &a)
		reply := "finished: " + a.Task
		if strings.Contains(strings.ToLower(a.Task), "weather") {
			reply = "it is 17 degrees with light rain in Paris."
		}
		b.mu.Lock()
		b.seq++
		id := fmt.Sprintf("job_bench%02d", b.seq)
		rt := b.rt()
		b.mu.Unlock()
		go func() {
			time.Sleep(fakeReplyDelay)
			if rt != nil {
				rt.Inject(voice.Notice{Text: "Agent reply: " + reply})
			}
		}()
		o := ok(map[string]any{"job_id": id, "status": "delegated"})
		o.EndAfterReply = !strings.HasPrefix(a.Task, "Check:")
		return o
	}})
	reg.Register(benchTool{inner: agent.Jobs{}, b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"jobs": []map[string]any{{"job_id": "job_bench00", "status": "working", "task": "check the build", "age_secs": 120}}})
	}})
	reg.Register(benchTool{inner: agent.AnswerJob{}, b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"error": "No job is waiting for an answer."})
	}})
	reg.Register(benchTool{inner: agent.Recall{}, b: b, fn: func(json.RawMessage) voice.ToolOutcome {
		return ok(map[string]any{"job_id": "job_recall", "status": "delegated"})
	}})
	return reg
}

// ---------- the rig ----------

type benchEv struct {
	At   time.Time
	Kind voice.EventKind
	Tool string
	Role string
	Text string
}

type benchRig struct {
	rt     *voice.Runtime
	mu     sync.Mutex
	evs    []benchEv
	mic    []int16
	endAt  time.Time
	played int64
	stop   chan struct{}
	done   chan struct{}
}

type benchModel struct {
	voice.Model
	r *benchRig
}

func (m benchModel) Connect(ctx context.Context, cfg voice.SessionConfig, key string) (voice.Session, error) {
	sess, err := m.Model.Connect(ctx, cfg, key)
	if err != nil {
		return nil, err
	}
	out := make(chan voice.Event, 512)
	go func() {
		defer close(out)
		for ev := range sess.Events() {
			m.r.mu.Lock()
			m.r.evs = append(m.r.evs, benchEv{At: time.Now(), Kind: ev.Kind, Tool: ev.ToolName, Role: ev.Role, Text: ev.Text + ev.Message})
			m.r.mu.Unlock()
			out <- ev
		}
	}()
	return benchSession{Session: sess, events: out}, nil
}

type benchSession struct {
	voice.Session
	events chan voice.Event
}

func (s benchSession) Events() <-chan voice.Event { return s.events }

func (r *benchRig) loop() {
	defer close(r.done)
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	out := make([]int16, audio.FrameSamples)
	for {
		select {
		case <-r.stop:
			return
		case <-tick.C:
		}
		frame := make([]int16, audio.FrameSamples)
		r.mu.Lock()
		n := copy(frame, r.mic)
		r.mic = r.mic[n:]
		if n > 0 && len(r.mic) == 0 {
			r.endAt = time.Now()
		}
		r.mu.Unlock()
		r.rt.PushMic(frame, time.Now())
		got, _ := r.rt.PullPlayback(out)
		r.mu.Lock()
		r.played += int64(got)
		played := r.played
		r.mu.Unlock()
		var rms float32
		if got > 0 {
			rms = voice.RMS(out[:got])
		}
		r.rt.ReportPlayback(played, 0, rms)
	}
}

// say queues pcm and returns once it has all been sent: the end of the user's audio.
func (r *benchRig) say(pcm []int16) time.Time {
	r.mu.Lock()
	r.endAt = time.Time{}
	r.mic = append(r.mic, pcm...)
	r.mu.Unlock()
	for {
		time.Sleep(10 * time.Millisecond)
		r.mu.Lock()
		end := r.endAt
		r.mu.Unlock()
		if !end.IsZero() {
			return end
		}
	}
}

func (r *benchRig) lastAudio() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.evs) - 1; i >= 0; i-- {
		if r.evs[i].Kind == voice.EvAudioDelta {
			return r.evs[i].At
		}
	}
	return time.Time{}
}

func (r *benchRig) events() []benchEv {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.evs)
}

// ---------- results ----------

// BenchTurn is one measured turn (the raw JSON).
type BenchTurn struct {
	Mode     string `json:"mode"`
	Rep      int    `json:"rep"`
	Scenario string `json:"scenario"`
	Kind     string `json:"kind"`
	Turn     int    `json:"turn"`
	Say      string `json:"say"`
	Expect   string `json:"expect"`
	Route    string `json:"route"`
	RouteOK  bool   `json:"route_ok"`
	// Tools: tool calls the model made this turn (EvToolCall names), and the tools the fakes saw.
	ToolCalls []string `json:"tool_calls"`
	Invoked   []string `json:"invoked"`
	// Latencies from the end of the user's audio, ms (-1 = didn't happen).
	DelegationMs int64    `json:"delegation_created_ms"` // first session.delegation.created
	ToolCallMs   int64    `json:"tool_call_ms"`          // first EvToolCall
	TaskMs       int64    `json:"task_ms"`               // the fake agent got the task (delegate / answer_job / jobs)
	AudioMs      int64    `json:"first_audio_ms"`        // first assistant audio
	Task         string   `json:"task,omitempty"`
	Context      string   `json:"context,omitempty"`
	TaskOK       *bool    `json:"task_ok,omitempty"`
	User         string   `json:"user"`
	Assistant    string   `json:"assistant"`
	AnswerOK     *bool    `json:"answer_ok,omitempty"`
	Errors       []string `json:"errors,omitempty"`
	// Session-level (on the scenario's last turn).
	Ended          *bool          `json:"ended_by_itself,omitempty"`
	ClosedMs       int64          `json:"closed_ms,omitempty"` // when it ended by itself, from the end of this turn's audio
	NotReached     bool           `json:"not_reached,omitempty"`
	SessionSeconds float64        `json:"session_seconds,omitempty"`
	BackendUsage   map[string]int `json:"backend_usage,omitempty"`
	Note           string         `json:"note,omitempty"`
}

var agentTools = map[string]bool{voice.DelegateTool: true, "jobs": true, voice.AnswerJobTool: true, "recall": true}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func TestLiveBenchGPTLiveDelegation(t *testing.T) {
	if path := os.Getenv("BENCH_SUMMARY"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var turns []BenchTurn
		if err := json.Unmarshal(b, &turns); err != nil {
			t.Fatal(err)
		}
		fmt.Println(benchSummary(turns))
		return
	}
	if os.Getenv("BENCH_GPTLIVE") == "" {
		t.Skip("set BENCH_GPTLIVE=1")
	}
	secrets := liveSecrets(t)
	key, ok := secrets.APIKey(openailive.Name)
	if !ok {
		t.Skip("no OPENAI_API_KEY")
	}
	_ = key
	n := 3
	if v, err := strconv.Atoi(os.Getenv("BENCH_N")); err == nil && v > 0 {
		n = v
	}
	modes := []string{settings.LiveDelegationBackend, settings.LiveDelegationAgent}
	if v := os.Getenv("BENCH_MODES"); v != "" {
		modes = strings.Split(v, ",")
	}
	backendModel := settings.Default().Voice.LiveBackendModel
	if v := os.Getenv("BENCH_BACKEND_MODEL"); v != "" {
		backendModel = v
	}
	var only map[string]bool
	if v := os.Getenv("BENCH_ONLY"); v != "" {
		only = map[string]bool{}
		for _, id := range strings.Split(v, ",") {
			only[id] = true
		}
	}
	scenarios := benchScenarios()
	speech := map[string][]int16{}
	for _, s := range scenarios {
		for _, tn := range s.Turns {
			if speech[tn.Say] == nil {
				speech[tn.Say] = liveSpeech(t, tn.Say)
			}
		}
	}
	instructions := app.Instructions(voice.Instructions(""), agent.DelegationPersona)
	tap := &wireTap{}
	url := liveProxy(t, tap)
	var all []BenchTurn
	out := os.Getenv("BENCH_OUT")
	save := func() {
		if out == "" {
			return
		}
		b, _ := json.MarshalIndent(all, "", " ")
		_ = os.WriteFile(out, b, 0o644)
	}
	for rep := 1; rep <= n; rep++ {
		for _, mode := range modes {
			for _, sc := range scenarios {
				if only != nil && !only[sc.ID] {
					continue
				}
				turns := runBenchScenario(t, url, tap, secrets, mode, backendModel, instructions, sc, speech)
				for i := range turns {
					turns[i].Rep = rep
				}
				all = append(all, turns...)
				save()
				last := turns[len(turns)-1]
				t.Logf("%s rep %d %-9s route=%v/%v task=%q audio=%d ms assistant=%q", mode, rep, sc.ID, last.Route, last.Expect, last.Task, last.AudioMs, last.Assistant)
			}
		}
	}
	fmt.Println(benchSummary(all))
}

func runBenchScenario(t *testing.T, url string, tap *wireTap, secrets voice.SecretSource, mode, backendModel, instructions string,
	sc benchScenario, speech map[string][]int16) []BenchTurn {
	tap.reset()
	bt := &benchTools{}
	r := &benchRig{stop: make(chan struct{}), done: make(chan struct{})}
	bt.rt = func() *voice.Runtime { return r.rt }
	cfg := voice.SessionConfig{Model: openailive.DefaultModel, Voice: openailive.DefaultVoice, Instructions: instructions,
		BackendModel: backendModel, ClientDelegation: mode == settings.LiveDelegationAgent}
	conv := voice.DefaultConversationConfig()
	conv.HandOff, conv.CheckClaims = true, true
	r.rt = voice.NewRuntime(benchModel{Model: openailive.New(url), r: r}, nil, voice.RuntimeConfig{
		Session: cfg, Secrets: secrets, Tools: benchRegistry(bt), MouthLevels: 4, Conversation: conv,
		Facts: tools.LocalFacts{Clock: tools.SystemClock{}, Home: "Paris, France"},
	}, nil)
	go r.loop()
	defer func() {
		close(r.stop)
		<-r.done
		r.rt.Close()
	}()
	r.rt.Hotkey()
	deadline := time.Now().Add(15 * time.Second)
	for !r.rt.IsOpen() {
		if time.Now().After(deadline) {
			t.Errorf("%s %s: never opened", mode, sc.ID)
			return []BenchTurn{{Mode: mode, Scenario: sc.ID, Errors: []string{"never opened"}}}
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	type turnMark struct{ start, end time.Time }
	var marks []turnMark
	var closedAt time.Time
	for i, tn := range sc.Turns {
		start := time.Now()
		end := r.say(speech[tn.Say])
		marks = append(marks, turnMark{start, end})
		wait := 10 * time.Second
		if i < len(sc.Turns)-1 {
			wait = 7 * time.Second
		}
		// Wait for the turn to settle: the conversation ended, or the wait passed and Bit has
		// been quiet for 1.5 s (at most 20 s).
		for r.rt.IsOpen() && time.Since(end) < 20*time.Second {
			if time.Since(end) >= wait && time.Since(r.lastAudio()) >= 1500*time.Millisecond {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !r.rt.IsOpen() {
			closedAt = time.Now()
			break
		}
	}
	endedByItself := !r.rt.IsOpen()
	if r.rt.IsOpen() {
		r.rt.Hotkey()
	}
	// Let session.closed (final usage) arrive.
	closeBy := time.Now().Add(3 * time.Second)
	for time.Now().Before(closeBy) {
		if slices.ContainsFunc(tap.snapshot(), func(e wireEvent) bool { return e.Type == "session.closed" }) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	evs, wire, calls := r.events(), tap.snapshot(), bt.snapshot()
	if os.Getenv("BENCH_DEBUG") != "" {
		t0 := marks[0].end
		for _, w := range wire {
			t.Logf("wire %+6d %s %s %s %s %.200s", ms(w.At.Sub(t0)), w.Dir, w.Type, w.Target, w.Nested, w.Raw)
		}
		for _, e := range evs {
			if e.Kind != voice.EvAudioDelta {
				t.Logf("ev %+6d kind=%d %s %s %.120s", ms(e.At.Sub(t0)), e.Kind, e.Tool, e.Role, e.Text)
			}
		}
	}

	var turns []BenchTurn
	for i, m := range marks {
		tn := sc.Turns[i]
		next := time.Now().Add(time.Hour)
		if i+1 < len(marks) {
			next = marks[i+1].start
		}
		in := func(at time.Time) bool { return !at.Before(m.start) && at.Before(next) }
		after := func(at time.Time) int64 {
			if at.IsZero() {
				return -1
			}
			return ms(at.Sub(m.end))
		}
		bt := BenchTurn{Mode: mode, Scenario: sc.ID, Kind: sc.Kind, Turn: i + 1, Say: tn.Say, Note: tn.Note,
			DelegationMs: -1, ToolCallMs: -1, TaskMs: -1, AudioMs: -1}
		bt.Expect = tn.Backend
		if mode == settings.LiveDelegationAgent {
			bt.Expect = tn.Agent
		}
		var user, said []string
		var firstCall, firstAudio time.Time
		for _, e := range evs {
			if !in(e.At) {
				continue
			}
			switch e.Kind {
			case voice.EvToolCall:
				bt.ToolCalls = append(bt.ToolCalls, e.Tool)
				if firstCall.IsZero() {
					firstCall = e.At
				}
			case voice.EvAudioDelta:
				if firstAudio.IsZero() && !e.At.Before(m.end) {
					firstAudio = e.At
				}
			case voice.EvTranscript:
				if e.Role == "user" {
					user = append(user, e.Text)
				} else {
					said = append(said, e.Text)
				}
			case voice.EvError:
				bt.Errors = append(bt.Errors, e.Text)
			}
		}
		bt.User, bt.Assistant = strings.Join(user, " | "), strings.Join(said, " | ")
		bt.ToolCallMs, bt.AudioMs = after(firstCall), after(firstAudio)
		for _, w := range wire {
			if in(w.At) && w.Dir == "s" && w.Type == "session.delegation.created" && bt.DelegationMs < 0 {
				bt.DelegationMs = after(w.At)
			}
			if in(w.At) && w.Dir == "s" && w.Type == "error" {
				bt.Errors = append(bt.Errors, string(w.Raw))
			}
		}
		toAgent := false
		var localTools []string
		for _, c := range calls {
			if !in(c.At) {
				continue
			}
			bt.Invoked = append(bt.Invoked, c.Tool)
			if agentTools[c.Tool] {
				toAgent = true
				if bt.TaskMs < 0 {
					bt.TaskMs = after(c.At)
				}
				if bt.Task == "" {
					if c.Tool == "jobs" {
						a, _ := c.Args["action"].(string)
						c.Args = map[string]any{"task": "jobs " + a}
					}
					for _, k := range []string{"task", "answer", "question"} {
						if s, ok := c.Args[k].(string); ok && s != "" {
							bt.Task = s
							break
						}
					}
					bt.Context, _ = c.Args["context"].(string)
				}
			} else {
				localTools = append(localTools, c.Tool)
			}
		}
		switch {
		case toAgent:
			bt.Route = routeAgent
		case len(localTools) > 0:
			bt.Route = local(localTools[0])
		default:
			bt.Route = routeNone
		}
		switch {
		case bt.Expect == routeAny:
			bt.RouteOK = true
		case strings.HasPrefix(bt.Expect, "local:"):
			bt.RouteOK = slices.Contains(localTools, strings.TrimPrefix(bt.Expect, "local:")) && !toAgent
		default:
			bt.RouteOK = bt.Route == bt.Expect
		}
		if sc.Ends && mode == settings.LiveDelegationAgent && bt.Route == local(voice.EndConversationTool) {
			bt.Route, bt.RouteOK = routeNone, true // the made-up goodbye call is not a routing decision
		}
		if toAgent && len(tn.Keywords) > 0 && !strings.HasPrefix(bt.Task, "jobs ") {
			hay := strings.ToLower(bt.Task + " " + bt.Context)
			ok := true
			for _, g := range tn.Keywords {
				if !slices.ContainsFunc(g, func(k string) bool { return strings.Contains(hay, k) }) {
					ok = false
				}
			}
			bt.TaskOK = &ok
		}
		if tn.Answer != "" {
			pattern := tn.Answer
			if pattern == "TIME" {
				pattern = timePattern(m.end)
			}
			ok := regexp.MustCompile("(?i)" + pattern).MatchString(bt.Assistant)
			bt.AnswerOK = &ok
		}
		turns = append(turns, bt)
	}
	last := &turns[len(turns)-1]
	last.Ended = &endedByItself
	if endedByItself && !closedAt.IsZero() {
		last.ClosedMs = ms(closedAt.Sub(marks[len(marks)-1].end))
	}
	for i := len(marks); i < len(sc.Turns); i++ {
		tn := sc.Turns[i]
		nr := BenchTurn{Mode: mode, Scenario: sc.ID, Kind: sc.Kind, Turn: i + 1, Say: tn.Say, NotReached: true, Route: "-",
			Expect: tn.Backend, DelegationMs: -1, ToolCallMs: -1, TaskMs: -1, AudioMs: -1,
			Note: "not reached: the conversation ended after the turn before"}
		if mode == settings.LiveDelegationAgent {
			nr.Expect = tn.Agent
		}
		turns = append(turns, nr)
	}
	last.BackendUsage = map[string]int{}
	for _, w := range wire {
		if w.Raw == nil {
			continue
		}
		var v struct {
			Usage struct {
				Seconds float64 `json:"seconds"`
			} `json:"usage"`
			Event struct {
				Response struct {
					Usage map[string]any `json:"usage"`
				} `json:"response"`
			} `json:"event"`
		}
		_ = json.Unmarshal(w.Raw, &v)
		if w.Type == "session.closed" || w.Type == "session.usage.updated" {
			last.SessionSeconds = math.Max(last.SessionSeconds, v.Usage.Seconds)
		}
		for k, val := range v.Event.Response.Usage {
			if f, ok := val.(float64); ok {
				last.BackendUsage[k] += int(f)
			}
		}
	}
	return turns
}

// timePattern matches the clock at t, ±2 minutes: "9:16" or "nine sixteen".
func timePattern(t time.Time) string {
	var alts []string
	for d := -2; d <= 2; d++ {
		x := t.Add(time.Duration(d) * time.Minute)
		alts = append(alts, x.Format("3:04"))
		h := numberWords(x.Hour()%12 + 12*btoi(x.Hour()%12 == 0))
		switch m := x.Minute(); {
		case m == 0:
			alts = append(alts, h+` o.?clock`)
		case m < 10:
			alts = append(alts, h+` (oh|o) `+numberWords(m))
		default:
			alts = append(alts, h+` `+strings.ReplaceAll(numberWords(m), "-", "[- ]"))
		}
	}
	return `\b(` + strings.Join(alts, "|") + `)\b`
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func numberWords(n int) string {
	ones := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty"}
	if n < 20 {
		return ones[n]
	}
	if n%10 == 0 {
		return tens[n/10]
	}
	return tens[n/10] + "-" + ones[n%10]
}

// ---------- summary ----------

func pct(xs []int64, p float64) string {
	if len(xs) == 0 {
		return "–"
	}
	s := slices.Clone(xs)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return strconv.FormatInt(s[max(i, 0)], 10)
}

func benchSummary(turns []BenchTurn) string {
	var b strings.Builder
	modes := []string{settings.LiveDelegationBackend, settings.LiveDelegationAgent}
	fmt.Fprintf(&b, "| metric | %s | %s |\n|---|---|---|\n", modes[0], modes[1])
	type agg struct {
		n, routeOK, tp, fp, fn, taskOK, taskN, ansOK, ansN, errs, notReached, byeEnded, byeN int
		deleg, call, task, audio, audioNone, audioAgent                                      []int64
		secs                                                                                 float64
		usage                                                                                map[string]int
		sessions                                                                             int
	}
	aggs := map[string]*agg{}
	for _, m := range modes {
		aggs[m] = &agg{usage: map[string]int{}}
	}
	for _, t := range turns {
		a, ok := aggs[t.Mode]
		if !ok {
			continue
		}
		if t.NotReached {
			a.notReached++
			continue
		}
		a.n++
		if t.RouteOK {
			a.routeOK++
		}
		if t.Expect != routeAny {
			exp, got := t.Expect == routeAgent, t.Route == routeAgent
			switch {
			case exp && got:
				a.tp++
			case !exp && got:
				a.fp++
			case exp && !got:
				a.fn++
			}
		}
		if t.TaskOK != nil {
			a.taskN++
			if *t.TaskOK {
				a.taskOK++
			}
		}
		if t.AnswerOK != nil {
			a.ansN++
			if *t.AnswerOK {
				a.ansOK++
			}
		}
		if len(t.Errors) > 0 {
			a.errs++
		}
		if t.DelegationMs >= 0 {
			a.deleg = append(a.deleg, t.DelegationMs)
		}
		if t.ToolCallMs >= 0 {
			a.call = append(a.call, t.ToolCallMs)
		}
		if t.TaskMs >= 0 {
			a.task = append(a.task, t.TaskMs)
		}
		if t.AudioMs >= 0 {
			a.audio = append(a.audio, t.AudioMs)
			if t.Route == routeAgent {
				a.audioAgent = append(a.audioAgent, t.AudioMs)
			} else if t.Route == routeNone {
				a.audioNone = append(a.audioNone, t.AudioMs)
			}
		}
		if t.Scenario == "bye" {
			a.byeN++
			if t.Ended != nil && *t.Ended && t.ClosedMs > 0 && t.ClosedMs < 5000 {
				a.byeEnded++
			}
		}
		if t.Ended != nil {
			a.sessions++
			a.secs += t.SessionSeconds
			for k, v := range t.BackendUsage {
				a.usage[k] += v
			}
		}
	}
	row := func(name string, f func(a *agg) string) {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, f(aggs[modes[0]]), f(aggs[modes[1]]))
	}
	ratio := func(x, n int) string {
		if n == 0 {
			return "–"
		}
		return fmt.Sprintf("%d/%d (%.0f%%)", x, n, 100*float64(x)/float64(n))
	}
	row("turns (not reached)", func(a *agg) string { return fmt.Sprintf("%d (%d)", a.n, a.notReached) })
	row("route as labelled", func(a *agg) string { return ratio(a.routeOK, a.n) })
	row("to-agent precision", func(a *agg) string { return ratio(a.tp, a.tp+a.fp) })
	row("to-agent recall", func(a *agg) string { return ratio(a.tp, a.tp+a.fn) })
	row("task keywords ok", func(a *agg) string { return ratio(a.taskOK, a.taskN) })
	row("quick/GK answer ok", func(a *agg) string { return ratio(a.ansOK, a.ansN) })
	row("delegation.created ms p50/p90", func(a *agg) string { return pct(a.deleg, .5) + " / " + pct(a.deleg, .9) })
	row("tool call ms p50/p90", func(a *agg) string { return pct(a.call, .5) + " / " + pct(a.call, .9) })
	row("task at agent ms p50/p90", func(a *agg) string { return pct(a.task, .5) + " / " + pct(a.task, .9) })
	row("first audio ms p50/p90 (all)", func(a *agg) string { return pct(a.audio, .5) + " / " + pct(a.audio, .9) })
	row("first audio ms p50 (no tool / to agent)", func(a *agg) string { return pct(a.audioNone, .5) + " / " + pct(a.audioAgent, .5) })
	row("goodbye ends the session < 5 s", func(a *agg) string { return ratio(a.byeEnded, a.byeN) })
	row("turns with errors", func(a *agg) string { return strconv.Itoa(a.errs) })
	row("GPT-Live seconds billed (sessions)", func(a *agg) string { return fmt.Sprintf("%.0f (%d)", a.secs, a.sessions) })
	row("backend tokens in/out", func(a *agg) string {
		return fmt.Sprintf("%d / %d", a.usage["input_tokens"], a.usage["output_tokens"])
	})

	fmt.Fprintf(&b, "\n| scenario | turn | expect (backend / agent) | backend routes | agent routes | backend task | agent task |\n|---|---|---|---|---|---|---|\n")
	type key struct {
		sc   string
		turn int
	}
	var order []key
	byKey := map[key]map[string][]BenchTurn{}
	for _, t := range turns {
		k := key{t.Scenario, t.Turn}
		if byKey[k] == nil {
			byKey[k] = map[string][]BenchTurn{}
			order = append(order, k)
		}
		byKey[k][t.Mode] = append(byKey[k][t.Mode], t)
	}
	routes := func(ts []BenchTurn) string {
		var out []string
		for _, t := range ts {
			s := t.Route
			if t.NotReached {
				s = "(not reached)"
			} else if !t.RouteOK {
				s += "✗"
			}
			out = append(out, s)
		}
		return strings.Join(out, ", ")
	}
	task := func(ts []BenchTurn) string {
		for _, t := range ts {
			if t.Task != "" {
				ok := ""
				if t.TaskOK != nil && !*t.TaskOK {
					ok = " ✗"
				}
				return strings.ReplaceAll(t.Task, "|", "/") + ok
			}
		}
		return ""
	}
	for _, k := range order {
		m := byKey[k]
		var exp []string
		for _, mode := range modes {
			if len(m[mode]) > 0 {
				exp = append(exp, m[mode][0].Expect)
			} else {
				exp = append(exp, "–")
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s |\n", k.sc, k.turn, strings.Join(exp, " / "),
			routes(m[modes[0]]), routes(m[modes[1]]), task(m[modes[0]]), task(m[modes[1]]))
	}
	return b.String()
}
