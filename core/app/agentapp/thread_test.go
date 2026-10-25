package agentapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/chat"
)

func rosterWith(rows ...agent.RosterRow) agent.RosterView {
	v := agent.EmptyRoster()
	for _, r := range rows {
		if r.Kind == "session" {
			v.Yours = append(v.Yours, r)
		} else {
			v.Agents = append(v.Agents, r)
		}
	}
	return v
}

func TestTheJoinWindowsAgree(t *testing.T) {
	if chat.JoinMs != int64(agent.ReplyJoinMs) {
		t.Fatalf("chat.JoinMs %d != agent.ReplyJoinMs %d", chat.JoinMs, agent.ReplyJoinMs)
	}
}

func TestASessionLineIsLabelledFromTheRoster(t *testing.T) {
	r := rosterWith(agent.RosterRow{Name: "api-claude", Label: "claude in api", Status: "working", Kind: "session"})
	l := agent.OutboxLine{ID: 7, Text: "long text", Brief: "short news", At: 1234, Session: "api-claude", Title: "api"}
	e := EntryForLine(l, r)
	want := chat.Who{Kind: "session", Name: "api-claude", Label: "claude in api", Yours: true, Status: "working", Color: chat.ColorFor("api-claude")}
	if e.Who != want || e.Text != "short news" || e.At != 1234 ||
		!reflect.DeepEqual(e.Lines, []uint64{7}) || !reflect.DeepEqual(e.Sessions, []string{"api-claude"}) {
		t.Fatalf("%+v", e)
	}
	l.Brief = ""
	if got := EntryForLine(l, r).Text; got != "long text" {
		t.Fatalf("text %q", got)
	}
}

func TestASessionNotInTheRosterFallsBackToItsTitleThenItsAddress(t *testing.T) {
	l := agent.OutboxLine{ID: 1, Text: "x", Session: "s-1", Title: "the title"}
	w := EntryForLine(l, agent.EmptyRoster()).Who
	if w.Label != "the title" || w.Yours || w.Status != "" || w.Name != "s-1" {
		t.Fatalf("%+v", w)
	}
	l.Title = ""
	if got := EntryForLine(l, agent.EmptyRoster()).Who.Label; got != "s-1" {
		t.Fatalf("label %q", got)
	}
}

// Worker lines and requests carry Worker, not Session (#302 review).
func TestAWorkersLineIsNotYours(t *testing.T) {
	r := rosterWith(agent.RosterRow{Name: "w-upload", Label: "upload worker", Status: "idle", Kind: "worker"})
	w := EntryForLine(agent.OutboxLine{ID: 1, Text: "x", Job: "j1", Worker: "w-upload"}, r).Who
	want := chat.Who{Kind: "session", Name: "w-upload", Label: "upload worker", Status: "idle", Color: chat.ColorFor("w-upload")}
	if w != want {
		t.Fatalf("%+v", w)
	}
	pane := rosterWith(agent.RosterRow{Name: "p1", Label: "a pane", Status: "working", Kind: "pane"})
	if w := EntryForLine(agent.OutboxLine{ID: 2, Text: "y", Worker: "p1"}, pane).Who; w.Kind != "session" || w.Label != "a pane" || w.Yours {
		t.Fatalf("pane %+v", w)
	}
}

func TestAWorkersRequestIsTheWorkers(t *testing.T) {
	r := rosterWith(agent.RosterRow{Name: "w-upload", Label: "upload worker", Status: "waiting", Kind: "worker"})
	es := RequestEntries([]agent.UpdateItem{{ID: 5, Text: "Run rm?", Token: "t5", Job: "j1", Worker: "w-upload"}}, r)
	if w := es[0].Who; w.Kind != "session" || w.Name != "w-upload" || w.Label != "upload worker" || w.Yours {
		t.Fatalf("%+v", w)
	}
	if es[0].Request.Job != "j1" {
		t.Fatalf("%+v", es[0].Request)
	}
}

func TestTwoSessionsWithOneLabelStayApart(t *testing.T) {
	r := rosterWith(
		agent.RosterRow{Name: "a-1", Label: "claude in api", Kind: "session"},
		agent.RosterRow{Name: "a-2", Label: "claude in api", Kind: "session"})
	a := EntryForLine(agent.OutboxLine{ID: 1, Text: "x", Session: "a-1"}, r)
	b := EntryForLine(agent.OutboxLine{ID: 2, Text: "y", Session: "a-2"}, r)
	if a.Who.Label != b.Who.Label || a.Who.Name == b.Who.Name || a.Who.Name != "a-1" {
		t.Fatalf("%+v %+v", a.Who, b.Who)
	}
}

func TestALineWithNoSessionIsTheCoordinators(t *testing.T) {
	e := EntryForLine(agent.OutboxLine{ID: 3, Text: "I tidied the notes."}, agent.EmptyRoster())
	if e.Who != (chat.Who{Kind: "coordinator"}) || e.Text != "I tidied the notes." || len(e.Sessions) != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestRequestsAreKeyedByTheirItem(t *testing.T) {
	items := []agent.UpdateItem{{ID: 4, Text: "Run it?", Token: "abc", Session: "s"}, {ID: 9, Text: "Which?", Options: []string{"a"}}}
	a := RequestEntries(items, agent.EmptyRoster())
	b := RequestEntries(items, agent.EmptyRoster())
	if len(a) != 2 || a[0].ID != "req4" || a[1].ID != "req9" || !reflect.DeepEqual(a, b) {
		t.Fatalf("%+v", a)
	}
	if a[0].Request.Item != 4 || a[0].Request.Token != "abc" {
		t.Fatalf("%+v", a[0].Request)
	}
	if got := RequestEntries(nil, agent.EmptyRoster()); got == nil || len(got) != 0 {
		t.Fatalf("empty list: %#v", got)
	}
}

func TestAPermissionAndAMenuMapToTheirKinds(t *testing.T) {
	items := []agent.UpdateItem{
		{ID: 1, Text: "long", Brief: "Run rm?", Token: "t1", Session: "s"},
		{ID: 2, Text: "Pick one", Token: "t2", Options: []string{"Yes", "No"}, Typed: true},
		{ID: 3, Text: "Shall I push?", Asking: true, Session: "s"},
	}
	es := RequestEntries(items, agent.EmptyRoster())
	if r := es[0].Request; r.Kind != "permission" || r.Token != "t1" || es[0].Text != "Run rm?" {
		t.Fatalf("%+v", es[0])
	}
	if r := es[1].Request; r.Kind != "question" || r.Token != "t2" || !r.Typed || !reflect.DeepEqual(r.Options, []string{"Yes", "No"}) {
		t.Fatalf("%+v", es[1])
	}
	if r := es[2].Request; r.Kind != "question" || r.Token != "" || es[2].Who.Kind != "session" {
		t.Fatalf("%+v", es[2])
	}
}

func TestAWorkingChatLineIsNoEntry(t *testing.T) {
	if _, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: "working", Text: "on it"}, "id", agent.EmptyRoster()); ok {
		t.Fatal("a working line made an entry")
	}
}

// #302 review: a question is in the thread as a request already (the updates list holds it).
func TestAQuestionChatLineIsNoEntry(t *testing.T) {
	if _, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: "question", Text: "Which repo?", Asking: true}, "id", agent.EmptyRoster()); ok {
		t.Fatal("a question made an entry")
	}
	// Another line while the job waits on it (the status is the job's) is an entry.
	e, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: "question", Text: "Waiting at a prompt in its terminal."}, "id", agent.EmptyRoster())
	if !ok || e.Text != "Waiting at a prompt in its terminal." {
		t.Fatalf("an info line on a waiting job was dropped: %+v %v", e, ok)
	}
}

func TestADoneChatLineIsTheCoordinatorsAnswer(t *testing.T) {
	for _, status := range []string{"done", "failed"} {
		e, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: status, Text: "Finished.", Sessions: []string{"s"}}, "jobj1.7", agent.EmptyRoster())
		if !ok || e.ID != "jobj1.7" || e.Who != (chat.Who{Kind: "coordinator"}) || e.Text != "Finished." || !reflect.DeepEqual(e.Sessions, []string{"s"}) {
			t.Fatalf("%s: %+v", status, e)
		}
	}
}

// #302 review: a session's own answer in the box is the session's, not the coordinator's.
func TestASessionsChatLineIsTheSessions(t *testing.T) {
	r := rosterWith(agent.RosterRow{Name: "api-claude", Label: "claude in api", Status: "idle", Kind: "session"})
	e, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: "done", Text: "Fixed.", Sessions: []string{"api-claude"}, Session: "api-claude"}, "x", r)
	if !ok || e.Who.Kind != "session" || e.Who.Name != "api-claude" || e.Who.Label != "claude in api" || !e.Who.Yours {
		t.Fatalf("%+v", e)
	}
}

// answers feeds lines through the chat sink and returns the entries the thread got.
func answers(f *threadFeed, lines ...agent.ChatLine) {
	for _, l := range lines {
		chatSink{hooks: app.AgentHooks{Chat: func(any) {}, ThreadAnswer: f.hooks.ThreadAnswer}, feed: f}.Chat(l)
	}
}

// #302 review round 2: info lines on a job waiting on its question reach the thread; the
// question itself doesn't (it is a request).
func TestAWaitingJobsInfoLineReachesTheThreadButItsQuestionDoesnt(t *testing.T) {
	var got []chat.Entry
	f := newThreadFeed(app.AgentHooks{ThreadAnswer: func(e chat.Entry) { got = append(got, e) }})
	answers(f, agent.ChatLine{Job: "j1", Status: "question", Text: "Which repo?", Asking: true},
		agent.ChatLine{Job: "j1", Status: "question", Text: "The upload worker is waiting at a prompt in its terminal."})
	if len(got) != 1 || got[0].Text != "The upload worker is waiting at a prompt in its terminal." {
		t.Fatalf("%+v", got)
	}
}

// #302 review (C1): one typed job with several turns keeps each turn.
func TestEachTurnOfATypedJobIsAnEntryOfItsOwn(t *testing.T) {
	var got []chat.Entry
	f := newThreadFeed(app.AgentHooks{ThreadAnswer: func(e chat.Entry) { got = append(got, e) }})
	answers(f, agent.ChatLine{Job: "j1", Status: "working", Text: "On it."},
		agent.ChatLine{Job: "j1", Status: "done", Text: "Two are running."},
		agent.ChatLine{Job: "j1", Status: "done", Text: "Two are running. One is stuck."}) // grows: the same turn
	f.sent("j1")
	answers(f, agent.ChatLine{Job: "j1", Status: "done", Text: "Restarted it."}) // after a send: a new turn
	answers(f, agent.ChatLine{Job: "j1", Status: "question", Text: "Push it?", Asking: true},
		agent.ChatLine{Job: "j1", Status: "done", Text: "Pushed."}) // after a question: a new turn
	answers(f, agent.ChatLine{Job: "j1", Status: "done", Text: "Something else."}) // says something new
	answers(f, agent.ChatLine{Job: "j1", Status: "done", Text: "Fixed.", Session: "api-claude"},
		agent.ChatLine{Job: "j1", Status: "done", Text: "Fixed. Also pushed.", Session: "api-claude"}) // the session's turn grows
	var ids, texts []string
	for _, e := range got {
		ids, texts = append(ids, e.ID), append(texts, e.Text)
	}
	wantTexts := []string{"Two are running.", "Two are running. One is stuck.", "Restarted it.", "Pushed.", "Something else.", "Fixed.", "Fixed. Also pushed."}
	if !reflect.DeepEqual(texts, wantTexts) {
		t.Fatalf("texts %q", texts)
	}
	if ids[0] != ids[1] || ids[5] != ids[6] {
		t.Fatalf("a growing line got a new id: %q", ids)
	}
	distinct := map[string]bool{ids[1]: true, ids[2]: true, ids[3]: true, ids[4]: true, ids[5]: true}
	if len(distinct) != 5 {
		t.Fatalf("turns share ids: %q", ids)
	}
	if got[5].Who.Kind != "session" {
		t.Fatalf("the session's turn: %+v", got[5].Who)
	}
	other := newThreadFeed(app.AgentHooks{ThreadAnswer: func(chat.Entry) {}})
	if other.seq <= 0 {
		t.Fatal("turn numbers start from the clock")
	}
}

func TestDeliveredLinesReachTheThread(t *testing.T) {
	var got [][]uint64
	f := newThreadFeed(app.AgentHooks{ThreadDelivered: func(ids []uint64) { got = append(got, ids) }})
	lineSink{f}.Delivered([]uint64{3, 4})
	lineSink{f}.Delivered(nil)
	lineSink{newThreadFeed(app.AgentHooks{})}.Delivered([]uint64{1}) // unset hook: nothing
	lineSink{}.Delivered([]uint64{1})                                // nil feed: nothing
	if !reflect.DeepEqual(got, [][]uint64{{3, 4}}) {
		t.Fatal(got)
	}
}

func TestTheFeedUsesTheLastRosterAndSkipsUnsetHooks(t *testing.T) {
	var lines []chat.Entry
	var reqs [][]chat.Entry
	f := newThreadFeed(app.AgentHooks{
		ThreadLine:     func(e chat.Entry) { lines = append(lines, e) },
		ThreadRequests: func(es []chat.Entry) { reqs = append(reqs, es) },
	})
	sink := rosterSink{feed: f}
	sink.Roster(rosterWith(agent.RosterRow{Name: "s", Label: "claude in api", Kind: "session"}))
	lineSink{f}.Line(agent.OutboxLine{ID: 1, Text: "x", Session: "s"})
	updatesSink{feed: f}.Updates([]agent.UpdateItem{{ID: 2, Text: "?", Session: "s"}})
	updatesSink{feed: f}.Updates([]agent.UpdateItem{})
	if len(lines) != 1 || lines[0].Who.Label != "claude in api" || !lines[0].Who.Yours {
		t.Fatalf("%+v", lines)
	}
	if len(reqs) != 2 || len(reqs[0]) != 1 || reqs[0][0].Who.Label != "claude in api" || reqs[1] == nil || len(reqs[1]) != 0 {
		t.Fatalf("%#v", reqs)
	}
	// Unset hooks and a nil feed do nothing.
	lineSink{newThreadFeed(app.AgentHooks{})}.Line(agent.OutboxLine{ID: 1})
	updatesSink{}.Updates(nil)
	rosterSink{}.Roster(agent.EmptyRoster())
	chatSink{hooks: app.AgentHooks{Chat: func(any) {}}}.Chat(agent.ChatLine{Job: "j", Status: "done"})
}

func TestADoneChatLineReachesThreadAnswer(t *testing.T) {
	var got []chat.Entry
	f := newThreadFeed(app.AgentHooks{ThreadAnswer: func(e chat.Entry) { got = append(got, e) }})
	answers(f, agent.ChatLine{Job: "j", Status: "working"}, agent.ChatLine{Job: "j", Status: "done", Text: "ok"})
	if len(got) != 1 || !strings.HasPrefix(got[0].ID, "jobj.") {
		t.Fatalf("%+v", got)
	}
}
