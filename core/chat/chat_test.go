package chat

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func coordinator(at int64, text string, line uint64) Entry {
	return Entry{At: at, Who: Who{Kind: "coordinator"}, Text: text, Lines: []uint64{line}}
}

func TestLinesFromOneSenderWithinFiveSecondsJoin(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(coordinator(1_000, "a", 1))
	got := s.Add(coordinator(5_000, "b", 2))
	if got.Text != "a\nb" || !slices.Equal(got.Lines, []uint64{1, 2}) {
		t.Fatalf("joined entry = %+v", got)
	}
	if p := s.Page("", 10); len(p) != 1 || p[0].ID != got.ID {
		t.Fatalf("page = %+v", p)
	}
}

func TestLinesFromAnotherSenderOrLaterDontJoin(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(coordinator(1_000, "a", 1))
	s.Add(Entry{At: 2_000, Who: Who{Kind: "session", Name: "w1"}, Text: "s"})
	s.Add(coordinator(3_000, "b", 2))
	s.Add(coordinator(9_001, "c", 3)) // 6 s after b
	if n := len(s.Page("", 10)); n != 4 {
		t.Fatalf("entries = %d, want 4", n)
	}
}

func TestRequestsNeverJoin(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	req := func(at int64) Entry {
		e := coordinator(at, "ok?", 0)
		e.Request = &Request{Kind: "question"}
		return e
	}
	s.Add(coordinator(1_000, "a", 1))
	s.Add(req(2_000))
	s.Add(req(3_000))
	s.Add(coordinator(4_000, "b", 2))
	if n := len(s.Page("", 10)); n != 4 {
		t.Fatalf("entries = %d, want 4", n)
	}
}

func TestAddWithAnExistingIDReplaces(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(Entry{ID: "x", At: 1, Who: Who{Kind: "pixie"}, Text: "one"})
	s.Add(Entry{ID: "y", At: 100_000, Who: Who{Kind: "pixie"}, Text: "two"})
	s.Add(Entry{ID: "x", At: 200_000, Who: Who{Kind: "pixie"}, Text: "uno"})
	p := s.Page("", 10)
	if len(p) != 2 || p[1].ID != "x" || p[1].Text != "uno" || p[0].ID != "y" {
		t.Fatalf("page = %+v", p)
	}
}

func TestPageIsNewestFirstAndPagesBack(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	for i := 0; i < 120; i++ {
		s.Add(Entry{At: int64(i) * 10_000, Who: Who{Kind: "pixie"}, Text: fmt.Sprint(i)})
	}
	ids := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	first := s.Page("", 50)
	if len(first) != 50 || first[0].ID != "e119" || first[49].ID != "e70" {
		t.Fatalf("first page = %v", ids(first))
	}
	second := s.Page("e70", 50)
	if len(second) != 50 || second[0].ID != "e69" || second[49].ID != "e20" {
		t.Fatalf("second page = %v", ids(second))
	}
	if got := s.Page("e0", 50); len(got) != 0 {
		t.Fatalf("before the first = %v", ids(got))
	}
	if got := s.Page("nope", 50); got != nil {
		t.Fatalf("unknown before = %v", ids(got))
	}
}

func TestNeedsAreOpenRequestsOldestFirst(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	for i, id := range []string{"a", "b", "c"} {
		s.Add(Entry{ID: id, At: int64(i), Who: Who{Kind: "session", Name: id}, Request: &Request{Kind: "permission"}})
	}
	s.Add(Entry{ID: "plain", At: 9, Who: Who{Kind: "pixie"}, Text: "hi"})
	if n := s.Needs(); len(n) != 3 || n[0].ID != "a" || n[2].ID != "c" {
		t.Fatalf("needs = %+v", n)
	}
	s.Update("b", func(e *Entry) { e.Request.Outcome = "allowed" })
	n := s.Needs()
	if len(n) != 2 || n[0].ID != "a" || n[1].ID != "c" {
		t.Fatalf("needs after outcome = %+v", n)
	}
}

func TestMarkReadReturnsNewlyReadLines(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(Entry{At: 0, Who: Who{Kind: "you"}, Text: "hey"})
	s.Add(Entry{At: 100_000, Who: Who{Kind: "session", Name: "w"}, Text: "a", Lines: []uint64{7}})
	s.Add(Entry{At: 200_000, Who: Who{Kind: "session", Name: "w"}, Text: "b", Lines: []uint64{8}})
	if u := s.Unread(); u != 2 {
		t.Fatalf("unread = %d, want 2", u)
	}
	if got := s.MarkRead("e1"); !slices.Equal(got, []uint64{7}) {
		t.Fatalf("first mark = %v", got)
	}
	if got := s.MarkRead("e2"); !slices.Equal(got, []uint64{8}) {
		t.Fatalf("second mark = %v", got)
	}
	if got := s.MarkRead("e2"); len(got) != 0 {
		t.Fatalf("repeat mark = %v", got)
	}
	if got := s.MarkRead("nope"); got != nil {
		t.Fatalf("unknown mark = %v", got)
	}
	if u := s.Unread(); u != 0 {
		t.Fatalf("unread = %d, want 0", u)
	}
}

func TestPageReturnsCopies(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(Entry{Who: Who{Kind: "pixie"}, Text: "a", Lines: []uint64{1}, Request: &Request{Options: []string{"y"}}})
	p := s.Page("", 1)
	p[0].Text = "changed"
	p[0].Lines[0] = 99
	p[0].Request.Options[0] = "n"
	got := s.Page("", 1)[0]
	if got.Text != "a" || got.Lines[0] != 1 || got.Request.Options[0] != "y" {
		t.Fatalf("store changed through a copy: %+v", got)
	}
}

func TestColorForIsStable(t *testing.T) {
	for _, name := range []string{"", "w1", "herdr:main:2", "ü"} {
		c := ColorFor(name)
		if c < 0 || c > 5 || c != ColorFor(name) {
			t.Fatalf("ColorFor(%q) = %d", name, c)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				e := s.Add(Entry{At: int64(i) * 10_000, Who: Who{Kind: "pixie"}, Text: "x", Lines: []uint64{1}})
				s.Update(e.ID, func(e *Entry) { e.Spoken = true })
				s.Page("", 5)
				s.Needs()
				s.MarkRead(e.ID)
				s.Unread()
			}
		}()
	}
	wg.Wait()
}

func job(at int64, id, text string) Entry {
	return Entry{ID: id, At: at, Who: Who{Kind: "coordinator"}, Text: text}
}

func TestEntryWithAnIDIsNotJoinedOntoALine(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(coordinator(1_000, "line", 1))
	s.Add(job(2_000, "job1", "answer"))
	s.Add(job(3_000, "job1", "answer, longer"))
	p := s.Page("", 10)
	if len(p) != 2 || p[0].ID != "job1" || p[0].Text != "answer, longer" || p[1].Text != "line" {
		t.Fatalf("page = %+v", p)
	}
}

func TestLineIsNotJoinedOntoAnEntryWithAnID(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(job(1_000, "job1", "answer"))
	s.Add(coordinator(2_000, "line", 5))
	s.Add(job(3_000, "job1", "answer, longer"))
	p := s.Page("", 10)
	if len(p) != 2 || p[0].Text != "line" || !slices.Equal(p[0].Lines, []uint64{5}) || p[1].Text != "answer, longer" {
		t.Fatalf("page = %+v", p)
	}
}

func TestMarkReadDoesNotRepeatLinesAfterAJoinOrReAdd(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(coordinator(1_000, "a", 1))
	if got := s.MarkRead("e0"); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("first mark = %v", got)
	}
	s.Add(coordinator(2_000, "b", 2)) // joins e0, which is unread again
	if u := s.Unread(); u != 1 {
		t.Fatalf("unread = %d, want 1", u)
	}
	if got := s.MarkRead("e0"); !slices.Equal(got, []uint64{2}) {
		t.Fatalf("second mark = %v", got)
	}
	s.Add(Entry{ID: "x", At: 50_000, Who: Who{Kind: "session", Name: "w"}, Lines: []uint64{3}})
	s.MarkRead("x")
	s.Add(Entry{ID: "x", At: 50_000, Who: Who{Kind: "session", Name: "w"}, Lines: []uint64{3, 4}})
	if u := s.Unread(); u != 1 {
		t.Fatalf("unread after re-add = %d, want 1", u)
	}
	if got := s.MarkRead("x"); !slices.Equal(got, []uint64{4}) {
		t.Fatalf("mark after re-add = %v", got)
	}
}

func TestGetIsTheEntryWithThatIDAsACopy(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(Entry{ID: "req1", Who: Who{Kind: "session", Name: "w1"}, Text: "ok?", Request: &Request{Item: 1, Kind: "permission"}})
	got, ok := s.Get("req1")
	if !ok || got.Text != "ok?" || got.Request == nil {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	got.Request.Outcome = "changed"
	if again, _ := s.Get("req1"); again.Request.Outcome != "" {
		t.Fatal("Get shares memory with the store")
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("an unknown id was found")
	}
}
