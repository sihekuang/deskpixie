package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/chat"
	"github.com/sihekuang/deskpixie/core/voice"
)

// #302: the chat thread in the app, its calls and events.

// sessionLine is a line from session name (labelled label), as Task 4's feed hands it over.
func sessionLine(line uint64, name, label, text string, at int64) chat.Entry {
	return chat.Entry{At: at, Who: chat.Who{Kind: "session", Name: name, Label: label, Color: chat.ColorFor(name)},
		Text: text, Sessions: []string{name}, Lines: []uint64{line}}
}

// request is an open request from the docs session (w1): a permission, or a question with options.
func request(item uint64, token, text string, options ...string) chat.Entry {
	r := &chat.Request{Token: token, Item: item, Kind: "permission", Options: options}
	if len(options) > 0 {
		r.Kind = "question"
	}
	return chat.Entry{ID: fmt.Sprintf("req%d", item), At: time.Now().UnixMilli(), Who: chat.Who{Kind: "session", Name: "w1", Label: "docs"},
		Text: text, Sessions: []string{"w1"}, Lines: []uint64{item}, Request: r}
}

// entries is chat.entries with these args.
func (h *harness) entries(args any) []chat.Entry {
	h.t.Helper()
	var out struct {
		Entries []chat.Entry `json:"entries"`
	}
	if err := json.Unmarshal(h.call("chat.entries", args), &out); err != nil {
		h.t.Fatal(err)
	}
	return out.Entries
}

// entryOf is the entry of a chat.entry event.
func entryOf(t *testing.T, ev event) chat.Entry {
	t.Helper()
	b, _ := json.Marshal(ev["entry"])
	var e chat.Entry
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func outcomeOf(t *testing.T, h *harness, id string) string {
	t.Helper()
	for _, e := range h.entries(map[string]any{"limit": 500}) {
		if e.ID == id {
			if e.Request == nil {
				t.Fatalf("%s has no request", id)
			}
			return e.Request.Outcome
		}
	}
	t.Fatalf("no entry %s", id)
	return ""
}

func TestSendingAddsYourMessage(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	h.call("chat.send", map[string]any{"text": "hello", "attachments": []string{"shot.png"}})
	if !slices.Equal(f.sent, []string{"hello"}) {
		t.Fatal(f.sent)
	}
	got := h.entries(nil)
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	e := got[0]
	if e.Who.Kind != "you" || e.Text != "hello" || !e.Read || e.Spoken || e.At == 0 ||
		!slices.Equal(e.Attachments, []string{"shot.png"}) || len(e.Sessions) != 0 {
		t.Fatalf("your entry = %+v", e)
	}
	evs := h.take()
	if ent := h.kinds(evs, "chat.entry"); len(ent) != 1 || entryOf(t, ent[0]).ID != e.ID {
		t.Fatalf("chat.entry events = %v", ent)
	}
	if b := h.kinds(evs, "chat.badge"); len(b) != 1 || b[0]["unread"] != 0.0 || b[0]["needs"] != 0.0 || b[0]["preview"] != nil {
		t.Fatalf("chat.badge events = %v", b)
	}

	f.chatErr = errNoCoordinator
	if _, err := h.callErr("chat.send", map[string]any{"text": "lost"}); err == nil {
		t.Fatal("a failed send went through")
	}
	if n := len(h.entries(nil)); n != 1 {
		t.Fatalf("a send that failed was added: %d entries", n)
	}
}

func TestSendingToASessionAsksTheCoordinatorToTellIt(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	now := time.Now().UnixMilli()
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs (old)", "started", now-2*(chat.JoinMs+1)))
	f.hooks.ThreadLine(sessionLine(2, "w1", "docs", "done", now-(chat.JoinMs+1)))
	f.hooks.ThreadLine(sessionLine(3, "w2", "api", "busy", now))
	h.call("chat.send", map[string]any{"text": "ship it", "session": "w1"})
	h.call("chat.send", map[string]any{"text": "hi", "session": "w9"}) // nothing from it: its address
	if !slices.Equal(f.sent, []string{"Tell the docs session: ship it", "Tell the w9 session: hi"}) {
		t.Fatal(f.sent)
	}
	got := h.entries(nil)
	if got[1].Who.Kind != "you" || got[1].Text != "ship it" || !slices.Equal(got[1].Sessions, []string{"w1"}) {
		t.Fatalf("your reply = %+v", got[1])
	}
	if got[0].Text != "hi" || !slices.Equal(got[0].Sessions, []string{"w9"}) {
		t.Fatalf("your reply = %+v", got[0])
	}
	// The session's lines keep the label they came with.
	if old := got[4]; old.Text != "started" || old.Who.Label != "docs (old)" {
		t.Fatalf("relabelled: %+v", old)
	}
}

func TestVoiceTranscriptsAreYoursAndPixies(t *testing.T) {
	h := newHarness(t)
	h.take()
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "user", Text: "what time is it"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: "Ten past three."})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: "  "})
	got := h.entries(nil)
	if len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
	pixie, you := got[0], got[1]
	if you.Who.Kind != "you" || !you.Spoken || you.Text != "what time is it" || you.At == 0 {
		t.Fatalf("yours = %+v", you)
	}
	if pixie.Who.Kind != "pixie" || !pixie.Spoken || pixie.Text != "Ten past three." || !pixie.Read { // heard (#302 review)
		t.Fatalf("Pixie's = %+v", pixie)
	}
	if n := len(h.kinds(h.take(), "transcript")); n != 3 {
		t.Fatalf("the transcript events still go to the shell: %d", n)
	}
}

func TestAnUpdatesListSentTwiceAddsEachRequestOnce(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	list := []chat.Entry{request(1, "t1", "run make")}
	f.hooks.ThreadRequests(list)
	f.hooks.ThreadRequests(list)
	if n := len(h.entries(nil)); n != 1 {
		t.Fatalf("entries = %d", n)
	}
	evs := h.take()
	if n := len(h.kinds(evs, "chat.entry")); n != 1 {
		t.Fatalf("chat.entry events = %d", n)
	}
	needs := h.kinds(evs, "chat.needs")
	if len(needs) != 1 || len(needs[0]["entries"].([]any)) != 1 {
		t.Fatalf("chat.needs events = %v", needs)
	}

	// Answered, the same list again leaves it answered.
	h.call("agent.decide", map[string]any{"token": "t1", "allow": true})
	f.hooks.ThreadRequests(list)
	if got := outcomeOf(t, h, "req1"); got != "You allowed run make" {
		t.Fatal(got)
	}
	var out struct {
		Entries []chat.Entry `json:"entries"`
	}
	if err := json.Unmarshal(h.call("chat.needs", nil), &out); err != nil || out.Entries == nil || len(out.Entries) != 0 {
		t.Fatalf("chat.needs = %+v, %v", out, err)
	}
}

func TestAllowingRecordsYouAllowed(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadRequests([]chat.Entry{
		request(1, "t1", "run make"),
		request(2, "t2", "delete build/"),
		request(3, "t3", "Which branch?", "main", "dev"),
		request(4, "t4", "Name it?", "Type something"),
	})
	h.take()
	h.call("agent.decide", map[string]any{"token": "t1", "allow": true})
	evs := h.take()
	if ent := h.kinds(evs, "chat.entry"); len(ent) != 1 || entryOf(t, ent[0]).Request.Outcome != "You allowed run make" {
		t.Fatalf("chat.entry = %v", ent)
	}
	if needs := h.kinds(evs, "chat.needs"); len(needs) != 1 || len(needs[0]["entries"].([]any)) != 3 {
		t.Fatalf("chat.needs = %v", needs)
	}
	if b := h.kinds(evs, "chat.badge"); len(b) != 1 || b[0]["needs"] != 3.0 {
		t.Fatalf("chat.badge = %v", b)
	}
	h.call("agent.decide", map[string]any{"token": "t2", "allow": false})
	h.call("agent.choose", map[string]any{"token": "t3", "option": 2})
	h.call("agent.typedAnswer", map[string]any{"token": "t4", "text": "pixie"})
	h.call("agent.decide", map[string]any{"token": "nope", "allow": true}) // no such request: nothing recorded
	for id, want := range map[string]string{"req1": "You allowed run make", "req2": "You denied delete build/",
		"req3": "You answered: dev", "req4": "You answered: pixie"} {
		if got := outcomeOf(t, h, id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if !slices.Equal(f.decided, []string{"t1:true", "t2:false", "t3:option 2", "t4:typed pixie", "nope:true"}) {
		t.Fatal(f.decided)
	}
}

func TestARequestAnsweredElsewhereIsRecordedSo(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadRequests([]chat.Entry{request(1, "t1", "run make"), request(2, "t2", "push?"), request(3, "t3", "tag?")})
	h.call("agent.decide", map[string]any{"token": "t3", "allow": true})
	f.hooks.ThreadRequests([]chat.Entry{request(2, "t2", "push?")})
	if got := outcomeOf(t, h, "req1"); got != "Answered elsewhere" {
		t.Fatal(got)
	}
	if got := outcomeOf(t, h, "req2"); got != "" {
		t.Fatal("still open:", got)
	}
	f.hooks.ThreadRequests(nil)
	if got := outcomeOf(t, h, "req2"); got != "Answered elsewhere" {
		t.Fatal(got)
	}
	if got := outcomeOf(t, h, "req3"); got != "You allowed tag?" {
		t.Fatal("an answered request kept its outcome:", got)
	}
}

func TestReadingMarksTheLinesShown(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadLine(sessionLine(7, "w1", "docs", "done", 0))
	f.hooks.ThreadLine(sessionLine(8, "w2", "api", "done too", 0))
	newest := h.entries(nil)[0].ID
	h.take()
	h.call("chat.read", map[string]any{"upTo": newest})
	if !slices.Equal(f.shownReplies(), []uint64{7, 8}) {
		t.Fatal(f.shownReplies())
	}
	if b := h.kinds(h.take(), "chat.badge"); len(b) != 1 || b[0]["unread"] != 0.0 {
		t.Fatalf("chat.badge = %v", b)
	}
	h.call("chat.read", map[string]any{"upTo": newest})
	h.call("chat.read", map[string]any{"upTo": "nope"})
	if n := len(f.shown); n != 1 {
		t.Fatalf("RepliesShown called %d times", n)
	}
}

func TestTheBadgeCountsNeedsAndUnread(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	badge := func() event {
		t.Helper()
		b := h.kinds(h.take(), "chat.badge")
		if len(b) != 1 {
			t.Fatalf("chat.badge events = %v", b)
		}
		return b[0]
	}
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "done", 0))
	b := badge()
	p, _ := b["preview"].(map[string]any)
	if b["needs"] != 0.0 || b["unread"] != 1.0 || p == nil || p["text"] != "done" || p["id"] == "" ||
		p["who"].(map[string]any)["label"] != "docs" {
		t.Fatalf("badge = %v", b)
	}
	f.hooks.ThreadRequests([]chat.Entry{request(2, "t2", "push?")})
	b = badge()
	if p, _ := b["preview"].(map[string]any); b["needs"] != 1.0 || b["unread"] != 2.0 || p == nil || p["id"] != "req2" {
		t.Fatalf("badge = %v", b)
	}
	h.call("chat.send", map[string]any{"text": "ok"})
	if b = badge(); b["preview"] != nil || b["unread"] != 2.0 {
		t.Fatalf("your own message: %v", b)
	}
	f.hooks.ThreadAnswer(chat.Entry{ID: "job1", Who: chat.Who{Kind: "coordinator"}, Text: "Half"})
	if b = badge(); b["preview"] == nil || b["unread"] != 3.0 {
		t.Fatalf("a new answer: %v", b)
	}
	f.hooks.ThreadAnswer(chat.Entry{ID: "job1", Who: chat.Who{Kind: "coordinator"}, Text: "Half and the rest"})
	if b = badge(); b["preview"] != nil {
		t.Fatalf("an answer that grew is not new: %v", b)
	}
	h.call("agent.decide", map[string]any{"token": "t2", "allow": true})
	if b = badge(); b["preview"] != nil || b["needs"] != 0.0 {
		t.Fatalf("an outcome: %v", b)
	}
}

func TestChatEntriesPagesBack(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	if got := h.call("chat.entries", nil); string(got) != `{"entries":[]}` {
		t.Fatal(string(got))
	}
	now := time.Now().UnixMilli()
	for i, text := range []string{"one", "two", "three"} {
		f.hooks.ThreadLine(sessionLine(uint64(i+1), "w1", "docs", text, now+int64(i)*(chat.JoinMs+1)))
	}
	first := h.entries(map[string]any{"limit": 2})
	if len(first) != 2 || first[0].Text != "three" || first[1].Text != "two" {
		t.Fatalf("first page = %+v", first)
	}
	rest := h.entries(map[string]any{"before": first[1].ID, "limit": 2})
	if len(rest) != 1 || rest[0].Text != "one" {
		t.Fatalf("next page = %+v", rest)
	}
	if all := h.entries(nil); len(all) != 3 {
		t.Fatalf("default page = %d", len(all))
	}
}

func TestChatKeepDaysIsLive(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	now := time.Now().UnixMilli()
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "old", now-3*24*60*60*1000))
	f.hooks.ThreadLine(sessionLine(2, "w2", "api", "new", 0))
	if s := h.set("chatKeepDays", 2); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatal(string(s.SetError))
	}
	got := h.entries(nil)
	if len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestTheThreadLivesInTheAppDataFolder(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.call("chat.send", map[string]any{"text": "remember me"})
	path := filepath.Join(h.dir, "chat", "thread.jsonl")
	if !strings.Contains(read(t, path), "remember me") {
		t.Fatal("not in", path)
	}
	h.app.Close()
	before := read(t, path)
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "after quit", 0))
	if read(t, path) != before {
		t.Fatal("the thread was written after the app closed")
	}
}

func TestAThreadThatCantOpenIsInMemory(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	inTheWay := func(d *Deps, h *harness) {
		if err := os.WriteFile(filepath.Join(d.Paths.AppSupport, "chat"), []byte("a file"), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	h := newHarness(t, inTheWay, withAgent(f))
	if !strings.Contains(logs.String(), "chat:") {
		t.Fatalf("nothing logged: %q", logs.String())
	}
	h.call("chat.send", map[string]any{"text": "still here"})
	if got := h.entries(nil); len(got) != 1 || got[0].Text != "still here" {
		t.Fatalf("entries = %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "chat")); string(b) != "a file" {
		t.Fatal("the file in the way was touched")
	}
}

func TestClearingEmptiesTheThread(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "done", 0))
	f.hooks.ThreadRequests([]chat.Entry{request(2, "t2", "push?")})
	h.take()
	if got := h.call("chat.clear", nil); string(got) != `{}` {
		t.Fatal(string(got))
	}
	if n := len(h.entries(nil)); n != 0 {
		t.Fatalf("entries = %d", n)
	}
	evs := h.take()
	if needs := h.kinds(evs, "chat.needs"); len(needs) != 1 || len(needs[0]["entries"].([]any)) != 0 {
		t.Fatalf("chat.needs = %v", needs)
	}
	if b := h.kinds(evs, "chat.badge"); len(b) != 1 || b[0]["needs"] != 0.0 || b[0]["unread"] != 0.0 {
		t.Fatalf("chat.badge = %v", b)
	}
}

// jobQuestion is job's own question, as the updates list holds it (no token).
func jobQuestion(item uint64, job, text string) chat.Entry {
	return chat.Entry{ID: fmt.Sprintf("req%d", item), At: time.Now().UnixMilli(), Who: chat.Who{Kind: "coordinator"}, Text: text,
		Lines: []uint64{item}, Request: &chat.Request{Item: item, Kind: "question", Job: job}}
}

// #302 review (C1): send, question, answer, done are four entries, in order.
func TestOneTypedJobKeepsEveryTurn(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	var out struct{ Job string }
	json.Unmarshal(h.call("chat.send", map[string]any{"text": "fix the flaky test"}), &out)
	f.hooks.ThreadRequests([]chat.Entry{jobQuestion(1, out.Job, "Which repo?")})
	f.asking = out.Job // the real agent answers the waiting question with the same job
	var again struct{ Job string }
	json.Unmarshal(h.call("chat.send", map[string]any{"text": "deskpixie"}), &again)
	if again.Job != out.Job {
		t.Fatal("the answer goes to the waiting job:", again.Job)
	}
	f.hooks.ThreadRequests(nil)
	f.hooks.ThreadAnswer(chat.Entry{ID: "job" + out.Job + ".2", Who: chat.Who{Kind: "coordinator"}, Text: "Fixed it."})
	got := h.entries(nil)
	slices.Reverse(got)
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	if !slices.Equal(texts, []string{"fix the flaky test", "Which repo?", "deskpixie", "Fixed it."}) {
		t.Fatalf("entries %q", texts)
	}
	if got[0].ID == got[2].ID {
		t.Fatal("your answer replaced your first message")
	}
	if o := got[1].Request.Outcome; o != "You answered: deskpixie" {
		t.Fatalf("the question's outcome = %q", o)
	}
}

// #302 review (I4): a reply to a session is never taken for the answer to a waiting question.
func TestASessionReplyIsNeverTakenForAWaitingAnswer(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadRequests([]chat.Entry{jobQuestion(1, "job_000000000009", "Which repo?")})
	f.asking = "job_000000000009"
	f.hooks.ThreadLine(sessionLine(2, "w1", "docs", "done", 0))
	h.call("chat.send", map[string]any{"text": "ship it", "session": "w1"})
	if f.delegated != 1 || f.asking == "" || !slices.Equal(f.sent, []string{"Tell the docs session: ship it"}) {
		t.Fatalf("delegated %d, still waiting %q, sent %q", f.delegated, f.asking, f.sent)
	}
	if o := outcomeOf(t, h, "req1"); o != "" {
		t.Fatal("the waiting question was answered:", o)
	}
}

// #302 review (I5): an answer sent in the box is recorded on the question it answered: the
// job's own, or the session's whose question the box shows, even after the agent's new list
// called it answered elsewhere.
func TestAnAnswerSentInTheBoxIsRecordedOnItsQuestion(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	asks := request(2, "", "Also fix the logout page?")
	asks.Request.Kind, asks.Request.Token = "question", ""
	f.hooks.ThreadRequests([]chat.Entry{jobQuestion(1, "job_000000000007", "Which repo?"), asks})
	f.asking, f.askingSession = "job_000000000007", "w1"
	h.call("chat.send", map[string]any{"text": " yes please "})
	if o := outcomeOf(t, h, "req2"); o != "You answered: yes please" {
		t.Fatalf("the session's question = %q", o)
	}
	if o := outcomeOf(t, h, "req1"); o != "" {
		t.Fatal("the job's question was answered too:", o)
	}
	f.hooks.ThreadRequests([]chat.Entry{jobQuestion(1, "job_000000000007", "Which repo?")})
	f.hooks.ThreadRequests(nil) // the list moves on before the send's answer is recorded
	f.asking = "job_000000000007"
	h.call("chat.send", map[string]any{"text": "deskpixie"})
	if o := outcomeOf(t, h, "req1"); o != "You answered: deskpixie" {
		t.Fatalf("the job's question = %q", o)
	}
	h.call("chat.send", map[string]any{"text": "and another thing"}) // a new job: answers nothing
	if o := outcomeOf(t, h, "req2"); o != "You answered: yes please" {
		t.Fatal(o)
	}
}

// #302 review (I5, M3): Chat about this is an answer too; a blank typed answer is none.
func TestChatAboutIsRecordedAndABlankTypedAnswerIsNot(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadRequests([]chat.Entry{request(1, "t1", "Pick one", "a", "b"), request(2, "t2", "Name it?", "Type something")})
	h.call("agent.chatAbout", map[string]any{"token": "t1"})
	h.call("agent.typedAnswer", map[string]any{"token": "t2", "text": "   "})
	if o := outcomeOf(t, h, "req1"); o != "You chose to chat about this" {
		t.Fatal(o)
	}
	if o := outcomeOf(t, h, "req2"); o != "" {
		t.Fatal("a blank answer was recorded:", o)
	}
	if !slices.Equal(f.decided, []string{"t1:chat", "t2:typed    "}) {
		t.Fatal(f.decided)
	}
}

// #302 review (I6): the window and the badge read it at launch.
func TestTheBadgeCanBeReadAtLaunch(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	if got := string(h.call("chat.badge", nil)); got != `{"needs":0,"unread":0}` {
		t.Fatal(got)
	}
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "done", 0))
	f.hooks.ThreadRequests([]chat.Entry{request(2, "t2", "push?")})
	if got := string(h.call("chat.badge", nil)); got != `{"needs":1,"unread":2}` {
		t.Fatal(got)
	}
}

// #302 review (R12): when entries go, the window reloads.
func TestClearingOrKeepingFewerDaysResetsTheWindow(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	now := time.Now().UnixMilli()
	f.hooks.ThreadLine(sessionLine(1, "w1", "docs", "old", now-3*24*60*60*1000))
	f.hooks.ThreadLine(sessionLine(2, "w2", "api", "new", 0))
	h.take()
	h.set("chatKeepDays", 5) // drops nothing
	if n := len(h.kinds(h.take(), "chat.reset")); n != 0 {
		t.Fatal("reset with nothing dropped")
	}
	h.set("chatKeepDays", 2)
	evs := h.take()
	if n := len(h.kinds(evs, "chat.reset")); n != 1 {
		t.Fatalf("chat.reset events = %d", n)
	}
	if b := h.kinds(evs, "chat.badge"); len(b) != 1 || b[0]["unread"] != 1.0 {
		t.Fatalf("chat.badge = %v", b)
	}
	h.call("chat.clear", nil)
	if n := len(h.kinds(h.take(), "chat.reset")); n != 1 {
		t.Fatalf("chat.reset after clear = %d", n)
	}
}

// #302 review (I2): a request whose line id came back for a new request (from another run) is
// a new request: open again, unread, its line handed out again.
func TestARequestWithAReusedIDIsANewRequest(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	old := request(4, "t1", "run make")
	old.At = time.Now().UnixMilli() - 60_000
	f.hooks.ThreadRequests([]chat.Entry{old})
	h.call("agent.decide", map[string]any{"token": "t1", "allow": true})
	h.call("chat.read", map[string]any{"upTo": "req4"})
	h.take()
	fresh := request(4, "t9", "push to main")
	f.hooks.ThreadRequests([]chat.Entry{fresh})
	got := h.entries(nil)
	if len(got) != 1 || got[0].Text != "push to main" || got[0].Request.Outcome != "" || got[0].Read {
		t.Fatalf("entries = %+v", got)
	}
	if b := h.kinds(h.take(), "chat.badge"); len(b) != 1 || b[0]["preview"] == nil || b[0]["needs"] != 1.0 {
		t.Fatalf("chat.badge = %v", b)
	}
	h.call("chat.read", map[string]any{"upTo": "req4"})
	if !slices.Equal(f.shownReplies(), []uint64{4, 4}) {
		t.Fatal("the new request's line was not handed out again:", f.shownReplies())
	}
	f.hooks.ThreadRequests([]chat.Entry{fresh}) // the same one again: left alone
	if n := len(h.kinds(h.take(), "chat.entry")); n != 0 {
		t.Fatal("renewed twice")
	}
}

// #302 review (I3): what you heard (or saw in the bubble) is read in the thread.
func TestLinesHeardElsewhereAreRead(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	f.hooks.ThreadLine(sessionLine(7, "w1", "docs", "done", 0))
	f.hooks.ThreadLine(sessionLine(8, "w2", "api", "done too", 0))
	h.take()
	f.hooks.ThreadDelivered([]uint64{7})
	evs := h.take()
	if ent := h.kinds(evs, "chat.entry"); len(ent) != 1 || !entryOf(t, ent[0]).Read || entryOf(t, ent[0]).Text != "done" {
		t.Fatalf("chat.entry = %v", ent)
	}
	if b := h.kinds(evs, "chat.badge"); len(b) != 1 || b[0]["unread"] != 1.0 {
		t.Fatalf("chat.badge = %v", b)
	}
	h.call("chat.read", map[string]any{"upTo": h.entries(nil)[0].ID})
	if !slices.Equal(f.shownReplies(), []uint64{8}) {
		t.Fatal("a heard line was shown again:", f.shownReplies())
	}
}

// liveOf is a chat.live event's who, job and text.
func liveOf(ev event) string {
	job, _ := ev["job"].(string)
	return fmt.Sprintf("%v/%s/%v", ev["who"], job, ev["text"])
}

// #302 Task 8b: live lines come from heard, saying and a typed job's steps; each clears before
// its final entry (on a transcript, the job's answer, the mic going off, or a reset); none is
// stored.
func TestLiveLinesComeAndGo(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	order := func(evs []event) []string {
		var out []string
		for _, ev := range evs {
			switch ev["kind"] {
			case "chat.live":
				out = append(out, "live "+liveOf(ev))
			case "chat.entry":
				out = append(out, "entry "+entryOf(t, ev).Text)
			}
		}
		return out
	}
	expect := func(what string, want ...string) {
		t.Helper()
		if got := order(h.take()); !slices.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q", what, got, want)
		}
	}

	// Yours: the caption grows, then your transcript clears it before its entry.
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "what"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "what time"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "what time"}) // the same: nothing new
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "user", Text: "what time is it"})
	expect("yours", "live you//what", "live you//what time", "live you//", "entry what time is it")

	// Pixie's, the same way; a blank transcript still clears.
	h.app.onVoiceEvent(voice.UIEvent{Kind: "saying", Text: "Ten"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: "Ten past three."})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "saying", Text: "And"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "transcript", Role: "assistant", Text: " "})
	expect("Pixie's", "live pixie//Ten", "live pixie//", "entry Ten past three.", "live pixie//And", "live pixie//")

	// The mic going off clears what was cut off.
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "never mind"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "saying", Text: "Sure"})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "mic", Text: "stop"})
	expect("mic off", "live you//never mind", "live pixie//Sure", "live you//", "live pixie//")

	// The coordinator's step, by job, cleared by the job's answer (the feed clears first).
	f.hooks.ThreadLive("j1", "Reading the logs.")
	f.hooks.ThreadLive("j2", "Looking.")
	f.hooks.ThreadLive("j1", "")
	f.hooks.ThreadAnswer(chat.Entry{ID: "jobj1.1", Who: chat.Who{Kind: "coordinator"}, Text: "Found it."})
	expect("coordinator", "live coordinator/j1/Reading the logs.", "live coordinator/j2/Looking.",
		"live coordinator/j1/", "entry Found it.")

	// A reset clears every line still live.
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "hey"})
	h.take()
	h.call("chat.clear", nil)
	evs := h.take()
	if r := h.kinds(evs, "chat.reset"); len(r) != 1 {
		t.Fatal("no reset")
	}
	if got := order(evs); !slices.Equal(got, []string{"live coordinator/j2/", "live you//"}) {
		t.Fatalf("reset: %q", got)
	}
	h.app.onVoiceEvent(voice.UIEvent{Kind: "heard", Text: "again"}) // live again after a reset
	if got := order(h.take()); !slices.Equal(got, []string{"live you//again"}) {
		t.Fatalf("after reset: %q", got)
	}

	// Nothing live is stored: only the finished words are entries, and live events alone
	// don't touch the badge.
	h.app.onVoiceEvent(voice.UIEvent{Kind: "saying", Text: "Hmm"})
	if n := len(h.kinds(h.take(), "chat.badge")); n != 0 {
		t.Fatalf("a live line sent %d badges", n)
	}
	if n := len(h.entries(nil)); n != 0 {
		t.Fatalf("entries after clear = %d", n)
	}
	f.hooks.ThreadAnswer(chat.Entry{ID: "jobj2.1", Who: chat.Who{Kind: "coordinator"}, Text: "Done."})
	for _, e := range h.entries(nil) {
		if e.Text != "Done." {
			t.Fatalf("a live line was stored: %+v", e)
		}
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "chat", "thread.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("Done.")) || (bytes.Contains(b, []byte("Hmm")) || bytes.Contains(b, []byte("again")) || bytes.Contains(b, []byte("Looking."))) {
		t.Fatalf("a live line is in thread.jsonl: %s", b)
	}
}
