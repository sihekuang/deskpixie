package canvas

import (
	"errors"
	"strings"
	"testing"
)

func TestValidSlugsAreAccepted(t *testing.T) {
	for _, slug := range []string{"a", "0", "q3-report", "2026-10-10-db-options", strings.Repeat("x", 64)} {
		id, err := PageIDFromSlug(slug)
		if err != nil || id.Slug() != slug {
			t.Errorf("%q: %v", slug, err)
		}
	}
}

func TestInvalidSlugsAreRejected(t *testing.T) {
	for _, slug := range []string{"", strings.Repeat("x", 65), "Foo", "a_b", "a.b", "a b", "../x", "a/b", "ü", "café"} {
		if _, err := PageIDFromSlug(slug); !errors.Is(err, ErrBadSlug) {
			t.Errorf("%q: %v", slug, err)
		}
	}
}

func TestFileNamesNeedTheHtmlSuffix(t *testing.T) {
	if id, err := PageIDFromFileName("report.html"); err != nil || id.Slug() != "report" {
		t.Fatal(err)
	}
	for _, name := range []string{"report.htm", "report", "report.HTML", "report.html.bak"} {
		if _, err := PageIDFromFileName(name); !errors.Is(err, ErrNotHTML) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := PageIDFromFileName(".html"); !errors.Is(err, ErrBadSlug) {
		t.Error(".html")
	}
	if _, err := PageIDFromFileName("Report.html"); !errors.Is(err, ErrBadSlug) {
		t.Error("Report.html")
	}
}

func TestFileNameAndUrlPath(t *testing.T) {
	id, _ := PageIDFromSlug("q3-report")
	if id.FileName() != "q3-report.html" || id.URLPath() != "/pages/q3-report.html" || id.String() != "q3-report" {
		t.Fatal(id.FileName(), id.URLPath(), id.String())
	}
}
