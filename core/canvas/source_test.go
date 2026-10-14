package canvas

import (
	"reflect"
	"testing"
)

func evAdded(slug string) PageEvent   { return PageEvent{Kind: Added, ID: mustID(slug)} }
func evChanged(slug string) PageEvent { return PageEvent{Kind: Changed, ID: mustID(slug)} }

func mustID(slug string) PageID {
	id, err := PageIDFromSlug(slug)
	if err != nil {
		panic(err)
	}
	return id
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestExistingPagesAreNotReportedAtStart(t *testing.T) {
	fs := NewMemFS()
	fs.Write("old.html", []byte("x"))
	src := NewPollingSource(fs)
	eq(t, len(src.Poll()), 0)
}

func TestANewPageIsAddedOnce(t *testing.T) {
	fs := NewMemFS()
	src := NewPollingSource(fs)
	fs.Write("b.html", []byte("x"))
	fs.Write("a.html", []byte("x"))
	eq(t, src.Poll(), []PageEvent{evAdded("a"), evAdded("b")})
	eq(t, len(src.Poll()), 0)
}

func TestARewriteIsChanged(t *testing.T) {
	fs := NewMemFS()
	fs.Write("a.html", []byte("v1"))
	src := NewPollingSource(fs)
	fs.Write("a.html", []byte("v2"))
	eq(t, src.Poll(), []PageEvent{evChanged("a")})
}

func TestSameMtimeButNewLengthIsChanged(t *testing.T) {
	fs := NewMemFS()
	fs.WriteAt("a.html", []byte("short"), 10)
	src := NewPollingSource(fs)
	fs.WriteAt("a.html", []byte("much longer"), 10)
	eq(t, src.Poll(), []PageEvent{evChanged("a")})
}

func TestInvalidNamesAreReportedOnceAndDotfilesNever(t *testing.T) {
	fs := NewMemFS()
	fs.Write("Old Name.html", []byte("x"))
	fs.Write(".DS_Store", []byte("x"))
	src := NewPollingSource(fs)
	eq(t, src.TakeInvalid(), []string{"Old Name.html"})
	fs.Write("notes.txt", []byte("x"))
	fs.Write("Old Name.html", []byte("y"))
	eq(t, len(src.Poll()), 0)
	eq(t, src.TakeInvalid(), []string{"notes.txt"})
	src.Poll()
	eq(t, len(src.TakeInvalid()), 0)
}

func TestDeletedThenRecreatedIsAddedAgain(t *testing.T) {
	fs := NewMemFS()
	fs.Write("a.html", []byte("x"))
	src := NewPollingSource(fs)
	_ = fs.Remove("a.html")
	eq(t, len(src.Poll()), 0)
	fs.Write("a.html", []byte("x"))
	eq(t, src.Poll(), []PageEvent{evAdded("a")})
}

func TestListErrorsAreReportedOnceUntilRecovered(t *testing.T) {
	fs := NewMemFS()
	src := NewPollingSource(fs)
	fs.SetListError(true)
	eq(t, len(src.Poll()), 0)
	if e, ok := src.TakeError(); !ok || e != "pages folder missing" {
		t.Fatal(e, ok)
	}
	src.Poll()
	if _, ok := src.TakeError(); ok {
		t.Fatal("reported twice")
	}
	fs.SetListError(false)
	fs.Write("a.html", []byte("x"))
	eq(t, src.Poll(), []PageEvent{evAdded("a")})
	fs.SetListError(true)
	src.Poll()
	if _, ok := src.TakeError(); !ok {
		t.Fatal("a new failure is reported again")
	}
}
