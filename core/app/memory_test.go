package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// captureModel records each connect's instructions and refuses the connection.
type captureModel struct {
	mu   sync.Mutex
	cfgs []voice.SessionConfig
}

func (m *captureModel) Name() string { return settings.ProviderOpenAI }
func (m *captureModel) Connect(_ context.Context, cfg voice.SessionConfig, _ string) (voice.Session, error) {
	m.mu.Lock()
	m.cfgs = append(m.cfgs, cfg)
	m.mu.Unlock()
	return nil, errors.New("offline")
}
func (m *captureModel) instructions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.cfgs {
		out = append(out, c.Instructions)
	}
	return out
}

func withCapture(m *captureModel) harnessOpt {
	return func(d *Deps, _ *harness) {
		d.Providers = map[string]ModelFactory{settings.ProviderOpenAI: func() voice.Model { return m }}
		d.Secrets = voice.NewEnvSecretsWithLookup(nil, func(string) (string, bool) { return "sk-test", true })
	}
}

// withRemembered leaves a memory from an earlier run in the app support folder.
func withRemembered(lines ...string) harnessOpt {
	return func(d *Deps, _ *harness) {
		mem := voice.NewMemory(voice.MemoryPath(d.Paths.AppSupport), nil)
		for _, l := range lines {
			mem.Add(voice.MemUser, l)
		}
	}
}

func connectOnce(t *testing.T, h *harness, m *captureModel) string {
	t.Helper()
	h.app.Hotkey()
	deadline := time.Now().Add(3 * time.Second)
	for len(m.instructions()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m.instructions()[0]
}

func TestASessionStartsWithTheMemoryOfAnEarlierRun(t *testing.T) {
	m := &captureModel{}
	h := newHarness(t, withCapture(m), withRemembered("the launch is on Friday"))
	got := connectOnce(t, h, m)
	if !strings.Contains(got, "Recent conversations") || !strings.Contains(got, "User: the launch is on Friday") ||
		!strings.Contains(got, "Local facts:") {
		t.Fatal(got)
	}
	if h.app.memory == nil {
		t.Fatal("memory off")
	}
}

func TestWithTheSettingOffNoMemoryIsGivenOrKept(t *testing.T) {
	m := &captureModel{}
	h := newHarness(t, withCapture(m), withRemembered("the launch is on Friday"),
		withSettings(func(c *settings.Config) { c.Voice.ShortTermMemory = false }))
	got := connectOnce(t, h, m)
	if strings.Contains(got, "Recent conversations (") || strings.Contains(got, "Friday") {
		t.Fatal(got)
	}
	if h.app.memory != nil {
		t.Fatal("recording while off")
	}
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "user", Text: "not kept"}) // no panic
	if err := h.app.ForgetMemory(); err != nil {
		t.Fatal(err)
	}
}

func TestTheMemorySettingDefaultsOnInTheVoiceSection(t *testing.T) {
	if !settings.Default().Voice.ShortTermMemory {
		t.Fatal("default off")
	}
	f := settings.FieldByPath("voice.shortTermMemory")
	if f == nil || f.Section != settings.SectionVoice || f.Kind.Type != settings.KindBool || f.Label == "" {
		t.Fatalf("%+v", f)
	}
}

func TestTranscriptsAreRememberedAndForgetClearsThem(t *testing.T) {
	h := newHarness(t, withCapture(&captureModel{}))
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "user", Text: "what's on today?"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: "Just the demo."})
	got := h.app.memory.mem.Entries()
	if len(got) != 2 || got[0].Role != voice.MemUser || got[1].Role != voice.MemBit {
		t.Fatalf("%+v", got)
	}
	h.action("memory:forget")
	if len(h.app.memory.mem.Entries()) != 0 {
		t.Fatal("not forgotten")
	}
}

func TestForgetRecentMutesTheRestOfTheConversation(t *testing.T) {
	h := newHarness(t, withCapture(&captureModel{}))
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "user", Text: "secret plan"})
	h.app.memory.forgetTool().Invoke(context.Background(), nil)
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: "Forgotten."})
	if n := len(h.app.memory.mem.Entries()); n != 0 {
		t.Fatalf("%d entries", n)
	}
}

type stubTool struct {
	name string
	out  string
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "" }
func (s stubTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) Invoke(context.Context, json.RawMessage) voice.ToolOutcome {
	return voice.ToolOutcome{Output: json.RawMessage(s.out), EndAfterReply: true}
}

func TestDelegationsAndAgentLinesAreRemembered(t *testing.T) {
	rec := &memoryRecorder{mem: voice.NewMemory("", nil)}
	d := rec.wrap(stubTool{"delegate", `{"job_id":"job_0123456789ab","status":"delegated"}`})
	out := d.Invoke(context.Background(), json.RawMessage(`{"task":"Draft the release notes","context":"for v2.3"}`))
	if !out.EndAfterReply || d.Name() != "delegate" {
		t.Fatal("the outcome must pass through unchanged")
	}
	rec.wrap(stubTool{"answer_job", `{"error":"no job is waiting"}`}).
		Invoke(context.Background(), json.RawMessage(`{"answer":"yes, ship it"}`))
	if _, wrapped := rec.wrap(stubTool{"jobs", `{}`}).(recordedTool); wrapped {
		t.Fatal("jobs is not a delegation")
	}
	rec.notice("Agent progress: reading files")
	rec.notice("Timer done: tea (5 minutes).")
	rec.notice("Agent reply: [ref job_0123456789ab] Notes drafted.")
	rec.notice("Agent replies while you were away:\n- one\n- two")
	got := rec.mem.Entries()
	want := []voice.MemoryEntry{
		{Role: voice.MemDelegated, Text: "Draft the release notes (context: for v2.3) [ref job_0123456789ab]"},
		{Role: voice.MemDelegated, Text: "answer to the agent's question: yes, ship it — failed: no job is waiting"},
		{Role: voice.MemCoordinator, Text: "reply: [ref job_0123456789ab] Notes drafted."},
		{Role: voice.MemCoordinator, Text: "replies while you were away: - one - two"},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Fatalf("%d: %+v", i, got[i])
		}
	}
	var nilRec *memoryRecorder
	if _, wrapped := nilRec.wrap(stubTool{"delegate", `{}`}).(recordedTool); wrapped {
		t.Fatal("wrapped with the memory off")
	}
}
