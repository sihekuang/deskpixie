package voice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 13, 15, 4, 0, 0, time.Local)}
}

func TestMemoryAppendsInOrderAndTidiesText(t *testing.T) {
	clk := newClock()
	m := NewMemory("", clk.now)
	m.Add(MemUser, "  what's   the plan\n for today? ")
	m.Add(MemBit, "")
	m.Add(MemBit, "Ship the memory.")
	got := m.Entries()
	if len(got) != 2 || got[0].Role != MemUser || got[0].Text != "what's the plan for today?" ||
		got[1].Role != MemBit || got[1].Text != "Ship the memory." {
		t.Fatalf("%+v", got)
	}
}

func TestMemoryKeepsOnlyTheNewestEntries(t *testing.T) {
	clk := newClock()
	m := NewMemory("", clk.now)
	for i := range MemoryMaxEntries + 5 {
		m.Add(MemUser, fmt.Sprintf("line %d", i))
	}
	got := m.Entries()
	if len(got) != MemoryMaxEntries || got[0].Text != "line 5" {
		t.Fatalf("%d entries, first %q", len(got), got[0].Text)
	}
}

func TestMemoryCutsLongEntries(t *testing.T) {
	m := NewMemory("", newClock().now)
	m.Add(MemUser, strings.Repeat("é", MemoryEntryChars*2))
	if n := len([]rune(m.Entries()[0].Text)); n != MemoryEntryChars {
		t.Fatal(n)
	}
}

func TestMemoryForgetsEntriesOutsideTheWindow(t *testing.T) {
	clk := newClock()
	m := NewMemory("", clk.now)
	m.Add(MemUser, "old")
	clk.advance(MemoryWindow / 2)
	m.Add(MemUser, "newer")
	clk.advance(MemoryWindow/2 + time.Minute)
	got := m.Entries()
	if len(got) != 1 || got[0].Text != "newer" {
		t.Fatalf("%+v", got)
	}
	clk.advance(MemoryWindow)
	if m.Facts() != "" || len(m.Entries()) != 0 {
		t.Fatal("expired entries remembered")
	}
}

func TestMemorySurvivesARestart(t *testing.T) {
	clk := newClock()
	path := filepath.Join(t.TempDir(), "sub", MemoryFile)
	m := NewMemory(path, clk.now)
	m.Add(MemUser, "remind me about the dentist")
	m.Add(MemDelegated, "book the dentist [ref job_0123456789ab]")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	again := NewMemory(path, clk.now)
	got := again.Entries()
	if len(got) != 2 || got[1].Role != MemDelegated || !got[0].At.Equal(m.Entries()[0].At) {
		t.Fatalf("%+v", got)
	}
	// Loading after the window has passed starts empty and removes the file.
	clk.advance(MemoryWindow + time.Second)
	if len(NewMemory(path, clk.now).Entries()) != 0 {
		t.Fatal("expired entries loaded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired file kept")
	}
}

func TestMemoryStartsEmptyFromABadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), MemoryFile)
	os.WriteFile(path, []byte("{nope"), 0o600)
	m := NewMemory(path, newClock().now)
	if len(m.Entries()) != 0 {
		t.Fatal()
	}
	m.Add(MemUser, "hi")
	if len(NewMemory(path, newClock().now).Entries()) != 1 {
		t.Fatal("not rewritten")
	}
}

func TestMemoryClearForgetsEverythingAndDeletesTheFile(t *testing.T) {
	clk := newClock()
	path := filepath.Join(t.TempDir(), MemoryFile)
	m := NewMemory(path, clk.now)
	m.Add(MemUser, "secret")
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 0 || m.Facts() != "" {
		t.Fatal("still remembered")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file kept")
	}
	if len(NewMemory(path, clk.now).Entries()) != 0 {
		t.Fatal("reloaded")
	}
	if err := m.Clear(); err != nil {
		t.Fatal("second clear:", err)
	}
}

func TestMemoryFactsAreACompactTranscript(t *testing.T) {
	clk := newClock()
	m := NewMemory("", clk.now)
	if m.Facts() != "" {
		t.Fatal("empty memory adds facts")
	}
	m.Add(MemUser, "Summarize the release notes")
	m.Add(MemDelegated, "Summarize the release notes [ref job_aa]")
	m.Add(MemCoordinator, "reply: [ref job_aa] Three fixes and one feature.")
	m.Add(MemBit, "Three fixes and a new feature.")
	f := m.Facts()
	for _, want := range []string{
		"Recent conversations",
		"Don't recite it",
		"- [3:04 PM] User: Summarize the release notes",
		"You handed to the agent: Summarize the release notes [ref job_aa]",
		"Agent: reply: [ref job_aa] Three fixes and one feature.",
		"You: Three fixes and a new feature.",
	} {
		if !strings.Contains(f, want) {
			t.Fatalf("missing %q in\n%s", want, f)
		}
	}
	if strings.Index(f, "User:") > strings.Index(f, "You: Three") {
		t.Fatal("not oldest first")
	}
}

func TestMemoryFactsShowTheWeekdayForEarlierDays(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 13, 22, 30, 0, 0, time.Local)}
	m := NewMemory("", clk.now)
	m.Add(MemUser, "late question")
	clk.t = time.Date(2026, 10, 14, 1, 0, 0, 0, time.Local) // after midnight, inside the window
	if f := m.Facts(); !strings.Contains(f, "[Tue 10:30 PM] User: late question") {
		t.Fatal(f)
	}
}

func TestMemoryFactsKeepTheNewestWithinTheBudget(t *testing.T) {
	m := NewMemory("", newClock().now)
	for i := range MemoryMaxEntries {
		m.Add(MemUser, fmt.Sprintf("entry %02d %s", i, strings.Repeat("x", 200)))
	}
	f := m.Facts()
	if n := len([]rune(f)); n > MemoryBudgetChars+400 {
		t.Fatalf("%d characters", n)
	}
	if !strings.Contains(f, fmt.Sprintf("entry %02d", MemoryMaxEntries-1)) || strings.Contains(f, "entry 00") {
		t.Fatal("trimmed the wrong end")
	}
}

func TestForgetToolClearsTheMemory(t *testing.T) {
	m := NewMemory("", newClock().now)
	m.Add(MemUser, "secret")
	called := false
	out := ForgetTool{Memory: m, OnForget: func() { called = true }}.Invoke(context.Background(), nil)
	if len(m.Entries()) != 0 || !called || !strings.Contains(string(out.Output), `"ok":true`) || out.EndConversation {
		t.Fatalf("%s %v", out.Output, called)
	}
}

func TestEachConnectCarriesTheMemoryAndDeliveredNoticesAreReported(t *testing.T) {
	mem := NewMemory("", time.Now)
	mem.Add(MemUser, "my cat is called Pixel")
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	var injMu sync.Mutex
	var injected []string
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "k", MouthLevels: 4, Facts: mem,
		Conversation: DefaultConversationConfig(),
		OnInject: func(text string) {
			injMu.Lock()
			injected = append(injected, text)
			injMu.Unlock()
		},
	}, ui.emit)
	t.Cleanup(r.Close)

	signals := r.Subscribe()
	r.Inject(Notice{Text: "Agent reply: bounced"}) // no conversation: not given to the model
	select {
	case s := <-signals:
		if s.Kind != SigClosed || len(s.Undelivered) != 1 {
			t.Fatalf("%+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not bounced")
	}
	r.Unsubscribe(signals)
	startListening(t, r, m, ui)
	if got := m.connects()[0].cfg.Instructions; !strings.HasPrefix(got, "be Bit\n\nRecent conversations") ||
		!strings.Contains(got, "User: my cat is called Pixel") {
		t.Fatal(got)
	}
	r.Inject(Notice{Text: "Agent reply: done"})
	eventually(t, "inject", func() bool { return len(m.sess.commands(CmdInject)) == 1 })
	eventually(t, "reported", func() bool {
		injMu.Lock()
		defer injMu.Unlock()
		return slices.Equal(injected, []string{"Agent reply: done"})
	})

	// A later connect (a reconnect, or after a restart) sees what was added since.
	r.Hotkey()
	eventually(t, "closed", func() bool { return !r.IsOpen() && ui.has(UIEvent{Kind: "mic", Text: "stop"}) })
	mem.Add(MemBit, "Pixel is a great name.")
	r.Hotkey()
	eventually(t, "reconnect", func() bool { return len(m.connects()) == 2 })
	if got := m.connects()[1].cfg.Instructions; !strings.Contains(got, "You: Pixel is a great name.") {
		t.Fatal(got)
	}
}
