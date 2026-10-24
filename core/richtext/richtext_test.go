package richtext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree makes files (and folders, ending in "/") under a temporary folder and returns it.
func tree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// raws joins the spans' source text: it is always the input, so the shell can show a prefix.
func raws(spans []Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Raw)
	}
	return b.String()
}

// only is the spans that aren't plain text.
func only(spans []Span) []Span {
	var out []Span
	for _, s := range spans {
		if s.Kind != KindText {
			out = append(out, s)
		}
	}
	return out
}

func TestRawsAlwaysJoinToTheInput(t *testing.T) {
	root := tree(t, "core/pix/questions.go")
	r := Resolver{Folders: []string{root}}
	for _, in := range []string{
		"",
		"plain words only",
		"**Tests pass.** I fixed `hold.go`, see core/pix/questions.go:152.",
		"- one\n- two with https://example.com/a?b=c).\n\n# Heading\nend",
		"[docs](https://code.claude.com/docs/en/hooks) and dan@example.com, commit a1b2c3d",
		"unclosed **bold and `code",
		"émoji 🎉 then https://ex.com/é",
	} {
		if got := raws(Parse(in, r)); got != in {
			t.Errorf("%q: raws %q", in, got)
		}
	}
}

func TestLinks(t *testing.T) {
	spans := only(Parse("PR: https://github.com/sihekuang/deskpixie/pull/273. Also (www.example.com/x), and http://a.b/c?d=e#f!", Resolver{}))
	want := []struct{ text, url string }{
		{"https://github.com/sihekuang/deskpixie/pull/273", "https://github.com/sihekuang/deskpixie/pull/273"},
		{"www.example.com/x", "https://www.example.com/x"},
		{"http://a.b/c?d=e#f", "http://a.b/c?d=e#f"},
	}
	if len(spans) != len(want) {
		t.Fatalf("%+v", spans)
	}
	for i, w := range want {
		if spans[i].Kind != KindLink || spans[i].Text != w.text || spans[i].URL != w.url {
			t.Errorf("%d: %+v", i, spans[i])
		}
	}
}

func TestMarkdownLinkShowsItsText(t *testing.T) {
	spans := only(Parse("Docs: [hooks reference](https://code.claude.com/docs/en/hooks).", Resolver{}))
	if len(spans) != 1 || spans[0].Kind != KindLink || spans[0].Text != "hooks reference" ||
		spans[0].URL != "https://code.claude.com/docs/en/hooks" || spans[0].Raw != "[hooks reference](https://code.claude.com/docs/en/hooks)" {
		t.Fatalf("%+v", spans)
	}
	// Only web links: a markdown link to anything else stays text.
	if got := only(Parse("[x](javascript:alert(1))", Resolver{})); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestPathsThatExist(t *testing.T) {
	root := tree(t, "core/pix/questions.go", "docs/", "README.md")
	r := Resolver{Folders: []string{root}}
	spans := only(Parse("see core/pix/questions.go:152:7, the docs/ folder, README.md and missing/file.go:3.", r))
	if len(spans) != 3 {
		t.Fatalf("%+v", spans)
	}
	q := spans[0]
	if q.Kind != KindPath || q.Text != "core/pix/questions.go:152:7" || q.Path != filepath.Join(root, "core/pix/questions.go") ||
		q.Line != 152 || q.Col != 7 || q.Dir {
		t.Errorf("%+v", q)
	}
	if d := spans[1]; d.Kind != KindPath || d.Path != filepath.Join(root, "docs") || !d.Dir || d.Line != 0 {
		t.Errorf("%+v", d)
	}
	if m := spans[2]; m.Kind != KindPath || m.Text != "README.md" || m.Path != filepath.Join(root, "README.md") {
		t.Errorf("%+v", m)
	}
}

func TestAbsoluteAndHomePaths(t *testing.T) {
	home := tree(t, "notes/todo.txt")
	abs := tree(t, "a.go")
	r := Resolver{Home: home}
	spans := only(Parse("~/notes/todo.txt:4 and "+filepath.Join(abs, "a.go")+" but not ~/nope.txt", r))
	if len(spans) != 2 || spans[0].Path != filepath.Join(home, "notes/todo.txt") || spans[0].Line != 4 ||
		spans[1].Path != filepath.Join(abs, "a.go") {
		t.Fatalf("%+v", spans)
	}
}

func TestNoFoldersMeansOnlyAbsolutePaths(t *testing.T) {
	// A relative path with no session folder to resolve it against is left as text.
	if got := only(Parse("core/pix/questions.go", Resolver{})); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestTheFirstFolderThatHasItWins(t *testing.T) {
	a := tree(t, "x/")
	b := tree(t, "x/main.go")
	spans := only(Parse("x/main.go", Resolver{Folders: []string{a, b}}))
	if len(spans) != 1 || spans[0].Path != filepath.Join(b, "x/main.go") {
		t.Fatalf("%+v", spans)
	}
}

func TestCodeSpans(t *testing.T) {
	root := tree(t, "hold.go")
	spans := only(Parse("I fixed `hold.go` and `go test ./...`, see `https://ex.com` and `a1b2c3d`.", Resolver{Folders: []string{root}}))
	if len(spans) != 4 {
		t.Fatalf("%+v", spans)
	}
	// Code that names a file that exists is that file; a link, a link; a commit, a commit.
	if spans[0].Kind != KindPath || spans[0].Text != "hold.go" || spans[0].Raw != "`hold.go`" {
		t.Errorf("%+v", spans[0])
	}
	if spans[1].Kind != KindCode || spans[1].Text != "go test ./..." {
		t.Errorf("%+v", spans[1])
	}
	if spans[2].Kind != KindLink || spans[2].URL != "https://ex.com" {
		t.Errorf("%+v", spans[2])
	}
	if spans[3].Kind != KindCommit || spans[3].Text != "a1b2c3d" {
		t.Errorf("%+v", spans[3])
	}
}

func TestCommitsAndEmails(t *testing.T) {
	spans := only(Parse("Commit a1b2c3d (full 26f9d1e0c3a5b7d9e1f2a3b4c5d6e7f8a9b0c1d2) by dan@example.com; not deadbeef, 1234567 or abc1234g.", Resolver{}))
	if len(spans) != 3 {
		t.Fatalf("%+v", spans)
	}
	if spans[0].Kind != KindCommit || spans[0].Text != "a1b2c3d" ||
		spans[1].Kind != KindCommit || len(spans[1].Text) != 40 ||
		spans[2].Kind != KindEmail || spans[2].Text != "dan@example.com" {
		t.Errorf("%+v", spans)
	}
}

func TestBoldListsAndHeadings(t *testing.T) {
	spans := Parse("# Done\n- **Tests pass.** All `12`\n* two\nplain - not a list", Resolver{})
	var shown strings.Builder
	for _, s := range spans {
		shown.WriteString(s.Text)
	}
	if got := shown.String(); got != "Done\n• Tests pass. All 12\n• two\nplain - not a list" {
		t.Fatalf("%q", got)
	}
	bold := map[string]bool{}
	for _, s := range spans {
		if s.Bold {
			bold[s.Text] = true
		}
	}
	if !bold["Done"] || !bold["Tests pass."] || bold["• "] || bold[" All "] {
		t.Fatalf("%+v", spans)
	}
}

func TestUnclosedMarkersStayText(t *testing.T) {
	spans := Parse("a **b and `c", Resolver{})
	if len(only(spans)) != 0 || len(spans) != 1 || spans[0].Text != "a **b and `c" || spans[0].Bold {
		t.Fatalf("%+v", spans)
	}
}

func TestAdjacentTextIsMerged(t *testing.T) {
	spans := Parse("just some words, nothing more.", Resolver{})
	if len(spans) != 1 {
		t.Fatalf("%+v", spans)
	}
}

// Review of #277: a click must never launch an app or run something. App bundles and other
// packages, launcher files and executables are shown in Finder instead of opened.
func TestThingsThatWouldRunAreOnlyShown(t *testing.T) {
	root := tree(t, "Tool.app/Contents/", "Setup.pkg", "go.command", "site.webloc", "notes.txt", "docs/")
	if err := os.WriteFile(filepath.Join(root, "run"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	r := Resolver{Folders: []string{root}}
	want := map[string]bool{"Tool.app": true, "Setup.pkg": true, "go.command": true, "site.webloc": true,
		"run/": false, "notes.txt": false, "docs/": false}
	for word, reveal := range want {
		spans := only(Parse(word, r))
		if word == "run/" {
			// an executable with no dot is only found with a slash; ./run names it
			spans = only(Parse("./run", r))
			reveal = true
		}
		if len(spans) != 1 || spans[0].Kind != KindPath || spans[0].Reveal != reveal {
			t.Errorf("%s: %+v", word, spans)
		}
	}
}

// Review of #277: a line of brackets used to take seconds (each `[` scanned to the line's end).
func TestALineOfBracketsIsQuick(t *testing.T) {
	in := strings.Repeat("[", 50_000) + "](https://x.com)"
	done := make(chan []Span)
	go func() { done <- Parse(in, Resolver{}) }()
	select {
	case spans := <-done:
		if raws(spans) != in {
			t.Fatal("raws must join to the input")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("too slow")
	}
}

func TestAHugeMessageIsLeftPlain(t *testing.T) {
	in := strings.Repeat("see https://x.com ", MaxText/10)
	spans := Parse(in, Resolver{})
	if len(spans) != 1 || spans[0].Kind != KindText || spans[0].Raw != in {
		t.Fatalf("%d spans", len(spans))
	}
}
