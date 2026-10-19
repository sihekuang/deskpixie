//go:build live

package agentapp_test

// Opt-in live check of #160: Pixie hands the coordinator the user's words as said, and the real
// coordinator works out what they meant (several things, steps said out of order, corrections,
// things taken back). Run with:
//
//	go test -count=1 -tags live -run LiveRawRequest -v -timeout 20m ./app/agentapp/
//
// agentapp.Build runs the real coordinator (SMOKE_COORDINATOR_MODEL, default haiku: if the
// smallest model sorts these out, the app's will) in the Herdr smoke session
// ("deskpixie-smoke"), with workers off. Each utterance goes through the delegate tool word for
// word, as Pixie now sends it, and its spoken Coordinator reply is checked.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

type rawRequestCase struct {
	name, say string
	// context: Pixie's context for the request, if any.
	context string
	// in must all appear in the reply (lowercased), in this order; out must not.
	in, out []string
}

var rawRequestCases = []rawRequestCase{
	{name: "said-last-done-first", in: []string{"banana", "apple"},
		say: "Reply with the word apple. Oh actually, before that, say the word banana."},
	{name: "corrected", in: []string{"lisbon"}, out: []string{"madrid"},
		say: "What's the capital of Spain? Wait, sorry, I meant Portugal."},
	{name: "taken-back", in: []string{"grape"}, out: []string{"cherry"},
		say: "Say the word cherry. Actually, never mind that, just say the word grape."},
	{name: "unrelated", in: []string{"tokyo", "cold"},
		say: "What's the capital of Japan? Also, separately, what's the opposite of hot?"},
	// Pixie did its own part and says so in the context: the coordinator doesn't do it again.
	{name: "done-by-pixie", in: []string{"pear"}, out: []string{"plum"},
		say:     "Say the word pear.",
		context: "The user said: say the word plum, and say the word pear. I already said plum myself."},
	{name: "detail-corrected", in: []string{"thursday"}, out: []string{"tuesday"},
		say: "Write me a one-sentence invitation to lunch on Tuesday. Wait, make that Thursday. Just say the sentence."},
}

// inOrder reports whether every word appears in s, each after the one before.
func inOrder(s string, words []string) bool {
	at := 0
	for _, w := range words {
		i := strings.Index(s[at:], w)
		if i < 0 {
			return false
		}
		at += i + len(w)
	}
	return true
}

func TestLiveRawRequestTheCoordinatorWorksOutWhatWasMeant(t *testing.T) {
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	s := settings.Default().Agent
	s.Session = smoketest.SmokeSession
	s.Port = port
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled = false
	s.Warm = false
	s.KeepPanesOnQuit = false
	model := os.Getenv("SMOKE_COORDINATOR_MODEL")
	if model == "" {
		model = "haiku"
	}
	s.Model = &model
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ,
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)
	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}
	var delegate voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "delegate" {
			delegate = tool
		}
	}
	if delegate == nil {
		t.Fatal("no delegate tool")
	}

	t.Run("answer-to-sort", func(t *testing.T) { liveAnswerToSort(t, a, v, delegate) })
	for _, c := range rawRequestCases {
		t.Run(c.name, func(t *testing.T) {
			before := len(v.all())
			args, _ := json.Marshal(map[string]string{"task": c.say, "context": c.context})
			out := delegate.Invoke(context.Background(), args)
			var res struct {
				JobID string `json:"job_id"`
			}
			if json.Unmarshal(out.Output, &res); res.JobID == "" {
				t.Fatalf("delegate failed: %s", out.Output)
			}
			reply := ""
			for deadline := time.Now().Add(3 * time.Minute); reply == "" && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
				for _, text := range v.all()[before:] {
					if body, ok := strings.CutPrefix(text, "Coordinator reply:"); ok {
						reply = strings.ToLower(body)
					}
				}
			}
			if reply == "" {
				t.Fatalf("no Coordinator reply within 3 minutes; injected %q; tray %+v", v.all()[before:], a.Tray())
			}
			t.Logf("%q → %q", c.say, reply)
			if !inOrder(reply, c.in) {
				t.Errorf("the reply should say %q, in that order", c.in)
			}
			for _, w := range c.out {
				if strings.Contains(reply, w) {
					t.Errorf("the reply says %q, which was taken back", w)
				}
			}
		})
	}
}

// liveAnswerToSort (#161): two requests each end in a question; an answer that says neither job
// goes to the coordinator with both questions, and it applies it to the one it fits.
func liveAnswerToSort(t *testing.T, a *agentapp.Agent, v *liveVoice, delegate voice.Tool) {
	var answer voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "answer_job" {
			answer = tool
		}
	}
	before := len(v.all())
	for _, task := range []string{
		"Ask me whether I'd like tea or coffee, as a question (status question), and wait for my answer.",
		"Ask me which color I like, red or blue, as a question (status question), and wait for my answer.",
	} {
		args, _ := json.Marshal(map[string]string{"task": task})
		delegate.Invoke(context.Background(), args)
	}
	questions := func() (n int) {
		for _, text := range v.all()[before:] {
			if strings.HasPrefix(text, "Coordinator question:") {
				n++
			}
		}
		return n
	}
	for deadline := time.Now().Add(3 * time.Minute); questions() < 2 && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
	}
	if questions() < 2 {
		t.Fatalf("the coordinator didn't ask both; injected %q", v.all()[before:])
	}
	asked := len(v.all())
	out := answer.Invoke(context.Background(), json.RawMessage(`{"answer":"Blue."}`))
	t.Logf("answer_job → %s", out.Output)
	reply := ""
	for deadline := time.Now().Add(3 * time.Minute); reply == "" && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		for _, text := range v.all()[asked:] {
			if strings.HasPrefix(text, "Coordinator reply:") || strings.HasPrefix(text, "Coordinator question:") {
				reply += strings.ToLower(text) + " "
			}
		}
	}
	t.Logf("after the answer: %q", v.all()[asked:])
	if !strings.Contains(reply, "blue") {
		t.Errorf("the coordinator should apply \"Blue\" to the color question: %q", reply)
	}
	if strings.Contains(reply, "which color") || strings.Contains(reply, "red or blue") {
		t.Errorf("the coordinator asked the color question again: %q", reply)
	}
}
