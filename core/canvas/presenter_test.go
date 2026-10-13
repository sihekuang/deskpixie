package canvas

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const goodHead = `<link rel="stylesheet" href="/ds/tokens.css">`

func html(title, body string) string {
	return "<html><head><title>" + title + "</title>" + goodHead + "</head><body>" + body + "</body></html>"
}

func recentCall(slugs ...string) PresenterCall {
	if slugs == nil {
		slugs = []string{}
	}
	return PresenterCall{Kind: CallRecent, Slugs: slugs}
}

func showCall(slug, title string) PresenterCall {
	return PresenterCall{Kind: CallShow, Slug: slug, Title: title}
}

func ms(t0 time.Time, n int64) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }

type rig struct {
	fs  *MemFS
	rec *RecordingPresenter
	ctl *CanvasController
	t0  time.Time
}

func rigWith(settings ControllerSettings, files ...[2]string) *rig {
	fs := NewMemFS()
	for _, f := range files {
		fs.Write(f[0], []byte(f[1]))
	}
	rec := &RecordingPresenter{}
	ctl := NewCanvasController(NewPageStore(fs), rec, settings)
	return &rig{fs: fs, rec: rec, ctl: ctl, t0: time.Now()}
}

func newRig(t *testing.T) *rig {
	r := rigWith(DefaultControllerSettings())
	eq(t, r.rec.Take(), []PresenterCall{recentCall()})
	return r
}

func TestNewSeedsRecentFromTheStoreNewestFirstAndCapped(t *testing.T) {
	var files [][2]string
	for n := 1; n <= 12; n++ {
		files = append(files, [2]string{fmt.Sprintf("p%02d.html", n), html("x", "")})
	}
	r := rigWith(DefaultControllerSettings(), files...)
	var expected []string
	for n := 12; n >= 3; n-- {
		expected = append(expected, fmt.Sprintf("p%02d", n))
	}
	eq(t, r.rec.Take(), []PresenterCall{recentCall(expected...)})
	eq(t, len(r.ctl.Recent()), 10)
}

func TestAnAddedPageIsShownAfterItSettles(t *testing.T) {
	r := newRig(t)
	r.fs.Write("q3.html", []byte(html("Q3 revenue", "")))
	r.ctl.OnEvents([]PageEvent{evAdded("q3")}, r.t0)
	eq(t, len(r.ctl.Tick(ms(r.t0, 100))), 0)
	eq(t, len(r.rec.Take()), 0)
	out := r.ctl.Tick(ms(r.t0, 300))
	if len(out) != 1 || out[0].Kind != OutcomeShown || out[0].Page.Title != "Q3 revenue" {
		t.Fatal(out)
	}
	eq(t, r.rec.Take(), []PresenterCall{recentCall("q3"), showCall("q3", "Q3 revenue")})
	if c := r.ctl.Current(); c == nil || c.Slug() != "q3" {
		t.Fatal(c)
	}
}

func TestRapidRewritesShowOnceAfterTheLastWrite(t *testing.T) {
	r := newRig(t)
	for _, s := range [][2]int64{{1, 0}, {2, 200}, {3, 400}} {
		r.fs.Write("live.html", []byte(html(fmt.Sprintf("draft %d", s[0]), "")))
		r.ctl.OnEvents([]PageEvent{evChanged("live")}, ms(r.t0, s[1]))
		eq(t, len(r.ctl.Tick(ms(r.t0, s[1]+100))), 0)
	}
	eq(t, len(r.ctl.Tick(ms(r.t0, 699))), 0)
	eq(t, len(r.ctl.Tick(ms(r.t0, 700))), 1)
	eq(t, r.rec.Take(), []PresenterCall{recentCall("live"), showCall("live", "draft 3")})
	eq(t, len(r.ctl.Tick(ms(r.t0, 2000))), 0)
}

func TestRealPollCadenceWithAStreamingWriterShowsOnce(t *testing.T) {
	r := newRig(t)
	source := NewPollingSource(r.fs)
	shown := 0
	// Controller ticks every 100 ms, the folder is polled every 5th tick; an agent appends
	// to the page every 250 ms for 2 s.
	for n := int64(1); n <= 120; n++ {
		at := n * 50
		if at%250 == 0 && at <= 2000 {
			k := at / 250
			r.fs.Write("live.html", []byte(html("Live", strings.Repeat("row ", int(k)))))
		}
		if at%100 != 0 {
			continue
		}
		now := ms(r.t0, at)
		var out []Outcome
		if at%500 == 0 {
			out = r.ctl.Step(source, now)
		} else {
			out = r.ctl.Tick(now)
		}
		for _, o := range out {
			if o.Kind == OutcomeShown {
				shown++
			}
		}
	}
	eq(t, shown, 1)
	var shows []PresenterCall
	for _, c := range r.rec.Take() {
		if c.Kind == CallShow {
			shows = append(shows, c)
		}
	}
	eq(t, shows, []PresenterCall{showCall("live", "Live")})
	_, bytes, err := NewPageStore(r.fs).Get(mustID("live"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.ctl.seen[mustID("live")]; !ok || got != hashBytes(bytes) {
		t.Fatal("the final content was shown")
	}
}

func TestUnchangedContentIsNotShownTwice(t *testing.T) {
	r := newRig(t)
	r.fs.Write("a.html", []byte(html("A", "")))
	r.ctl.OnEvents([]PageEvent{evAdded("a")}, r.t0)
	r.ctl.Tick(ms(r.t0, 300))
	r.rec.Take()
	r.fs.Write("a.html", []byte(html("A", ""))) // touched: new mtime, same bytes
	r.ctl.OnEvents([]PageEvent{evChanged("a")}, ms(r.t0, 1000))
	eq(t, len(r.ctl.Tick(ms(r.t0, 1300))), 0)
	eq(t, len(r.rec.Take()), 0)
}

func TestChangedContentIsShownAgain(t *testing.T) {
	r := newRig(t)
	r.fs.Write("a.html", []byte(html("A", "v1")))
	r.ctl.OnEvents([]PageEvent{evAdded("a")}, r.t0)
	r.ctl.Tick(ms(r.t0, 300))
	r.rec.Take()
	r.fs.Write("a.html", []byte(html("A", "v2")))
	r.ctl.OnEvents([]PageEvent{evChanged("a")}, ms(r.t0, 1000))
	eq(t, len(r.ctl.Tick(ms(r.t0, 1300))), 1)
	eq(t, r.rec.Take(), []PresenterCall{recentCall("a"), showCall("a", "A")})
}

func TestRecentMovesAPageToTheFrontWithoutDuplicates(t *testing.T) {
	s := DefaultControllerSettings()
	s.RecentLen = 3
	r := rigWith(s, [2]string{"a.html", html("A", "")}, [2]string{"b.html", html("B", "")}, [2]string{"c.html", html("C", "")})
	eq(t, r.rec.Take(), []PresenterCall{recentCall("c", "b", "a")})
	r.fs.Write("a.html", []byte(html("A", "new")))
	r.fs.Write("d.html", []byte(html("D", "")))
	r.ctl.OnEvents([]PageEvent{evChanged("a"), evAdded("d")}, r.t0)
	r.ctl.Tick(ms(r.t0, 300))
	calls := r.rec.Take()
	if !reflect.DeepEqual(calls[len(calls)-1], showCall("d", "D")) {
		t.Errorf("pages settle in name order: %v", calls)
	}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, recentCall("d", "a", "c")) {
			found = true
		}
	}
	if !found {
		t.Error(calls)
	}
}

func TestAnOversizedPageIsRejectedNotShown(t *testing.T) {
	r := newRig(t)
	r.fs.Write("big.html", []byte(strings.Repeat("x", int(MaxPageBytes)+1)))
	r.ctl.OnEvents([]PageEvent{evAdded("big")}, r.t0)
	out := r.ctl.Tick(ms(r.t0, 300))
	eq(t, out, []Outcome{{Kind: OutcomeRejected, ID: mustID("big"), Reason: "page is 5242881 bytes; the limit is 5 MB"}})
	eq(t, len(r.rec.Take()), 0)
}

func TestLintWarningsAreReportedButThePageIsStillShown(t *testing.T) {
	r := newRig(t)
	r.fs.Write("net.html", []byte(html("Net", "<script>fetch('/x')</script>")))
	r.ctl.OnEvents([]PageEvent{evAdded("net")}, r.t0)
	out := r.ctl.Tick(ms(r.t0, 300))
	if len(out) != 2 || out[0].Kind != OutcomeLint || out[0].ID != mustID("net") || out[0].Warnings[0].Rule != RuleNetworkAPI || out[1].Kind != OutcomeShown {
		t.Fatal(out)
	}
	calls := r.rec.Take()
	eq(t, calls[len(calls)-1], showCall("net", "Net"))
}

func TestAutoOpenOffUpdatesRecentOnly(t *testing.T) {
	s := DefaultControllerSettings()
	s.AutoOpen = false
	r := rigWith(s)
	r.rec.Take()
	r.fs.Write("a.html", []byte(html("A", "")))
	r.ctl.OnEvents([]PageEvent{evAdded("a")}, r.t0)
	out := r.ctl.Tick(ms(r.t0, 300))
	if len(out) != 1 || out[0].Kind != OutcomeUpdated || out[0].Page.ID != mustID("a") {
		t.Fatal(out)
	}
	eq(t, r.rec.Take(), []PresenterCall{recentCall("a")})
	if r.ctl.Current() != nil {
		t.Error("current should be unset")
	}
}

func TestOpenLastShowsTheNewestPageOrTheEmptyState(t *testing.T) {
	r := newRig(t)
	if err := r.ctl.OpenLast(); err != nil {
		t.Fatal(err)
	}
	eq(t, r.rec.Take(), []PresenterCall{{Kind: CallShowEmpty}})
	r = rigWith(DefaultControllerSettings(), [2]string{"a.html", html("A", "")}, [2]string{"b.html", html("B", "")})
	r.rec.Take()
	if err := r.ctl.OpenLast(); err != nil {
		t.Fatal(err)
	}
	eq(t, r.rec.Take(), []PresenterCall{showCall("b", "B")})
	if c := r.ctl.Current(); c == nil || c.Slug() != "b" {
		t.Fatal(c)
	}
}

func TestOpeningADeletedPageErrorsAndDropsItFromRecent(t *testing.T) {
	r := rigWith(DefaultControllerSettings(), [2]string{"a.html", html("A", "")}, [2]string{"b.html", html("B", "")})
	r.rec.Take()
	_ = r.fs.Remove("b.html")
	err := r.ctl.Open(mustID("b"))
	if err == nil || err.Error() != "b: unreadable: not found" {
		t.Fatal(err)
	}
	eq(t, r.rec.Take(), []PresenterCall{recentCall("a")})
}

func TestStepPullsEventsFromTheSource(t *testing.T) {
	r := newRig(t)
	r.fs.Write("a.html", []byte(html("A", "")))
	source := NewScriptedSource([][]PageEvent{{evAdded("a")}})
	eq(t, len(r.ctl.Step(source, r.t0)), 0)
	eq(t, len(r.ctl.Step(source, ms(r.t0, 500))), 1)
	eq(t, r.rec.Take(), []PresenterCall{recentCall("a"), showCall("a", "A")})
}

func TestAutoOpenCanBeSwitchedOffWhileRunning(t *testing.T) {
	r := newRig(t)
	r.ctl.SetAutoOpen(false)
	r.rec.Take()
	r.fs.Write("a.html", []byte(html("A", "")))
	r.ctl.OnEvents([]PageEvent{evAdded("a")}, r.t0)
	out := r.ctl.Tick(ms(r.t0, 300))
	if len(out) != 1 || out[0].Kind != OutcomeUpdated || out[0].Page.ID != mustID("a") {
		t.Fatal(out)
	}
	eq(t, r.rec.Take(), []PresenterCall{recentCall("a")})
	r.ctl.SetAutoOpen(true)
	r.fs.Write("b.html", []byte(html("B", "")))
	r.ctl.OnEvents([]PageEvent{evAdded("b")}, r.t0)
	out = r.ctl.Tick(ms(r.t0, 600))
	if len(out) != 1 || out[0].Kind != OutcomeShown || out[0].Page.ID != mustID("b") {
		t.Fatal(out)
	}
}
