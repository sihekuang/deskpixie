package agentapp

import (
	"reflect"
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

func TestAWorkersLineIsNotYours(t *testing.T) {
	r := rosterWith(agent.RosterRow{Name: "w1", Label: "w1", Status: "idle", Kind: "worker"})
	w := EntryForLine(agent.OutboxLine{ID: 1, Text: "x", Session: "w1"}, r).Who
	if w.Yours || w.Status != "idle" {
		t.Fatalf("%+v", w)
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
	if _, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: "working", Text: "on it"}); ok {
		t.Fatal("a working line made an entry")
	}
}

func TestADoneChatLineIsTheCoordinatorsAnswer(t *testing.T) {
	for _, status := range []string{"done", "failed", "question"} {
		e, ok := EntryForChat(agent.ChatLine{Job: "j1", Status: status, Text: "Finished.", Sessions: []string{"s"}})
		if !ok || e.ID != "jobj1" || e.Who != (chat.Who{Kind: "coordinator"}) || e.Text != "Finished." || !reflect.DeepEqual(e.Sessions, []string{"s"}) {
			t.Fatalf("%s: %+v", status, e)
		}
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
	chatSink{app.AgentHooks{Chat: func(any) {}}}.Chat(agent.ChatLine{Job: "j", Status: "done"})
}

func TestADoneChatLineReachesThreadAnswer(t *testing.T) {
	var got []chat.Entry
	chatSink{app.AgentHooks{Chat: func(any) {}, ThreadAnswer: func(e chat.Entry) { got = append(got, e) }}}.Chat(agent.ChatLine{Job: "j", Status: "working"})
	chatSink{app.AgentHooks{Chat: func(any) {}, ThreadAnswer: func(e chat.Entry) { got = append(got, e) }}}.Chat(agent.ChatLine{Job: "j", Status: "done", Text: "ok"})
	if len(got) != 1 || got[0].ID != "jobj" {
		t.Fatalf("%+v", got)
	}
}
