package agent

import (
	"reflect"
	"strings"
	"testing"
)

// Ported from crates/agent-core/src/outbox.rs (5 tests).

func all(string) bool { return true }

func TestReplayJoinsUndeliveredLinesInOrderAndMarksThem(t *testing.T) {
	o := NewOutbox()
	o.Push(reply("First done."), 1)
	o.Push(NewLine(LineUnsolicited, "Notes tidied."), 2)
	notice, records := o.Replay(all)
	if notice == nil || notice.Text != "Agent replies while you were away:\n- Agent reply: First done.\n- Agent says: Notes tidied." {
		t.Fatalf("%+v", notice)
	}
	if len(records) != 2 || len(o.Undelivered()) != 0 {
		t.Fatal("records")
	}
	if n, r := o.Replay(all); n != nil || len(r) != 0 {
		t.Fatal("nothing twice")
	}
}

func hawkQuestion() Line {
	tok := "hawk"
	return Line{Text: "Run git push in focus? Say confirm hawk.", Kind: LineQuestion, Token: &tok}
}

func TestDecidedOrExpiredQuestionsAreDropped(t *testing.T) {
	o := NewOutbox()
	o.Push(hawkQuestion(), 1)
	notice, records := o.Replay(func(t string) bool { return t != "hawk" })
	if notice != nil {
		t.Fatal(notice)
	}
	if len(records) != 1 {
		t.Fatal("still marked delivered")
	}
}

func TestLiveQuestionsAreReplayed(t *testing.T) {
	o := NewOutbox()
	o.Push(hawkQuestion(), 1)
	n, _ := o.Replay(all)
	if n == nil || !strings.Contains(n.Text, "Agent question: Run git push") {
		t.Fatal(n)
	}
}

func TestRestoreKeepsTheLastRecordAndContinuesIds(t *testing.T) {
	o := NewOutbox()
	file := []string{o.Push(reply("a"), 1), o.Push(reply("b"), 2)}
	_, records := o.Replay(all)
	file = append(file, records...)
	file = append(file, "garbage")
	back := RestoreOutbox(file, 3)
	if len(back.Undelivered()) != 0 {
		t.Fatal("undelivered")
	}
	if record := back.Push(reply("c"), 4); !strings.Contains(record, `"id":3`) {
		t.Fatal(record)
	}
}

func TestRestoreDropsOldDeliveredLinesAndStaleQuestions(t *testing.T) {
	o := NewOutbox()
	file := []string{o.Push(reply("old"), 1)}
	_, records := o.Replay(all)
	file = append(file, records...)
	file = append(file, o.Push(reply("kept"), 2))
	tok := "hawk"
	file = append(file, o.Push(Line{Text: "q", Kind: LineQuestion, Token: &tok}, 3))
	back := RestoreOutbox(file, 2+RetentionMs)
	var texts []string
	for _, l := range back.Undelivered() {
		texts = append(texts, l.Text)
	}
	if !reflect.DeepEqual(texts, []string{"kept"}) || len(back.Lines()) != 1 {
		t.Fatal(texts)
	}
}

func TestPushKeepsTheLinesJobAndWorker(t *testing.T) {
	o := NewOutbox()
	l := NewJobLine(LineInfo, "The upload worker says it's done.", job(3))
	l.Worker = "w-upload"
	o.Push(l, T0)
	got := o.Undelivered()[0]
	if got.Job != job(3) || got.Worker != "w-upload" {
		t.Fatalf("%+v", got)
	}
	r := RestoreOutbox(o.Lines(), T0+1).Undelivered()[0]
	if r.Job != job(3) || r.Worker != "w-upload" {
		t.Fatalf("restored %+v", r)
	}
}

func TestAnOldRecordWithoutJobRestores(t *testing.T) {
	o := RestoreOutbox([]string{`{"id":1,"text":"hi","kind":"info","token":null,"at":5,"delivered":false}`}, 10)
	if u := o.Undelivered(); len(u) != 1 || u[0].Job != "" || u[0].Worker != "" {
		t.Fatalf("%+v", u)
	}
}

func TestMarkSeenDeliversTheShownLinesButKeepsWhatKeepSays(t *testing.T) {
	o := NewOutbox()
	o.Push(NewLine(LineReply, "a"), T0)
	o.Push(NewLine(LineQuestion, "b?"), T0)
	o.Push(NewLine(LineReply, "c"), T0)
	recs := o.MarkSeen([]uint64{1, 2, 99}, func(l OutboxLine) bool { return l.Kind == LineQuestion })
	if len(recs) != 1 {
		t.Fatalf("records %v", recs)
	}
	u := o.Undelivered()
	if len(u) != 2 || u[0].Text != "b?" || u[1].Text != "c" {
		t.Fatalf("%+v", u)
	}
	if again := o.MarkSeen([]uint64{1}, func(OutboxLine) bool { return false }); len(again) != 0 {
		t.Fatal("idempotent")
	}
}
