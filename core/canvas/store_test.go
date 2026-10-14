package canvas

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func storeWith(files ...[2]string) (*MemFS, *PageStore) {
	fs := NewMemFS()
	for _, f := range files {
		fs.Write(f[0], []byte(f[1]))
	}
	return fs, NewPageStore(fs)
}

func slugs(pages []PageRef) []string {
	out := []string{}
	for _, p := range pages {
		out = append(out, p.ID.Slug())
	}
	return out
}

func mustScan(t *testing.T, s *PageStore) Scan {
	t.Helper()
	scan, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

func TestScanListsValidPagesNewestFirst(t *testing.T) {
	_, s := storeWith([2]string{"a.html", ""}, [2]string{"b.html", ""}, [2]string{"c.html", ""})
	if got := slugs(mustScan(t, s).Pages); !reflect.DeepEqual(got, []string{"c", "b", "a"}) {
		t.Fatal(got)
	}
}

func TestScanReportsInvalidNamesAndSkipsDotfiles(t *testing.T) {
	_, s := storeWith([2]string{"Bad.html", ""}, [2]string{"notes.txt", ""}, [2]string{".DS_Store", ""}, [2]string{"ok.html", ""})
	scan := mustScan(t, s)
	if !reflect.DeepEqual(slugs(scan.Pages), []string{"ok"}) || !reflect.DeepEqual(scan.Invalid, []string{"Bad.html", "notes.txt"}) {
		t.Fatal(scan)
	}
}

func TestTitleComesFromTheTitleTagElseTheSlug(t *testing.T) {
	_, s := storeWith(
		[2]string{"q3.html", "<html><head><TITLE> Q3 &amp; Q4\n   revenue </TITLE></head></html>"},
		[2]string{"plain.html", "<p>no title here</p>"},
		[2]string{"blank.html", "<title>   </title>"},
	)
	got := map[string]string{}
	for _, p := range mustScan(t, s).Pages {
		got[p.ID.Slug()] = p.Title
	}
	want := map[string]string{"blank": "blank", "plain": "plain", "q3": "Q3 & Q4 revenue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if v, ok := ExtractTitle(`<title data-x="1">A &lt;b&gt; &quot;c&quot;</title>`); !ok || v != `A <b> "c"` {
		t.Fatal(v)
	}
}

func TestTitlesAreExactCappedAndNotFromScripts(t *testing.T) {
	if v, _ := ExtractTitle("<titlex>no</titlex><title>yes</title>"); v != "yes" {
		t.Error(v)
	}
	if v, _ := ExtractTitle("<script>var t = '<title>fake</title>';</script><title>real</title>"); v != "real" {
		t.Error(v)
	}
	if _, ok := ExtractTitle("<script>'<title>fake</title>'</script>"); ok {
		t.Error("title inside script")
	}
	long := "<title>" + strings.Repeat("é", 500) + "</title>"
	if v, _ := ExtractTitle(long); len([]rune(v)) != 120 {
		t.Error(len([]rune(v)))
	}
}

func TestTitleScanIsLinearOnPathologicalInput(t *testing.T) {
	html := strings.Repeat("<script>x</script><title", 20000) + "<title>ok</title>"
	start := time.Now()
	if v, _ := ExtractTitle(html); v != "ok" {
		t.Error(v)
	}
	if time.Since(start) > time.Second {
		t.Error(time.Since(start))
	}
}

func TestScanReadsOnlyTheHeadOfEachPage(t *testing.T) {
	fs, s := storeWith()
	fs.Write("big.html", []byte(strings.Repeat("x", 4*1024*1024)))
	mustScan(t, s)
	if fs.BytesRead() > 64*1024+1 {
		t.Fatal(fs.BytesRead())
	}
}

func TestGetReadsAtMostTheLimitPlusOne(t *testing.T) {
	fs, s := storeWith()
	fs.Write("huge.html", []byte(strings.Repeat("x", int(MaxPageBytes)*2)))
	id, _ := PageIDFromSlug("huge")
	if _, _, err := s.Get(id); err == nil {
		t.Error("expected error")
	}
	if fs.BytesRead() != 0 {
		t.Error(fs.BytesRead())
	}
}

func TestGetReturnsThePageAndItsBytes(t *testing.T) {
	_, s := storeWith([2]string{"report.html", "<title>Report</title><p>hi</p>"})
	id, _ := PageIDFromSlug("report")
	page, b, err := s.Get(id)
	if err != nil || page.Title != "Report" || string(b) != "<title>Report</title><p>hi</p>" {
		t.Fatal(page, string(b), err)
	}
}

func TestGetRejectsOversizedPage(t *testing.T) {
	fs, s := storeWith()
	fs.Write("big.html", []byte(strings.Repeat("x", int(MaxPageBytes)+1)))
	fs.Write("edge.html", []byte(strings.Repeat("x", int(MaxPageBytes))))
	bigID, _ := PageIDFromSlug("big")
	_, _, err := s.Get(bigID)
	le, ok := err.(*LoadError)
	if !ok || !le.TooLarge || le.Size != MaxPageBytes+1 || err.Error() != "page is 5242881 bytes; the limit is 5 MB" {
		t.Fatalf("%#v", err)
	}
	edgeID, _ := PageIDFromSlug("edge")
	if _, _, err := s.Get(edgeID); err != nil {
		t.Error(err)
	}
	found := false
	for _, p := range mustScan(t, s).Pages {
		if p.ID.Slug() == "big" && p.Title == "big" {
			found = true
		}
	}
	if !found {
		t.Error("listed, titled by slug")
	}
}

func TestGetMissingPageIsAnIoError(t *testing.T) {
	_, s := storeWith()
	id, _ := PageIDFromSlug("gone")
	_, _, err := s.Get(id)
	if le, ok := err.(*LoadError); !ok || le.TooLarge || le.Msg != "not found" {
		t.Fatalf("%#v", err)
	}
}

func TestPruneKeepsTheNewestPages(t *testing.T) {
	fs, s := storeWith([2]string{"p1.html", ""}, [2]string{"p2.html", ""}, [2]string{"Keep Me.txt", ""},
		[2]string{"p3.html", ""}, [2]string{"p4.html", ""}, [2]string{"p5.html", ""})
	removed, err := s.Prune(3)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, id := range removed {
		names = append(names, id.String())
	}
	if len(names) != 2 || !((names[0] == "p1" && names[1] == "p2") || (names[0] == "p2" && names[1] == "p1")) {
		t.Fatal(names)
	}
	if got := fs.Names(); !reflect.DeepEqual(got, []string{"Keep Me.txt", "p3.html", "p4.html", "p5.html"}) {
		t.Fatal(got)
	}
}

func TestPruneWithFewPagesRemovesNothing(t *testing.T) {
	fs, s := storeWith([2]string{"a.html", ""}, [2]string{"b.html", ""})
	if removed, _ := s.Prune(50); len(removed) != 0 || len(fs.Names()) != 2 {
		t.Fatal(removed)
	}
}
