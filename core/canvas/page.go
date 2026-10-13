package canvas

import (
	"errors"
	"strings"
	"time"
)

// MaxSlugLen is the longest page slug.
const MaxSlugLen = 64

var (
	ErrNotHTML = errors.New("page files must end in .html")
	ErrBadSlug = errors.New("page names must be [a-z0-9-]{1,64}.html")
)

// PageID is a validated page slug. The file is <slug>.html; the URL path is /pages/<slug>.html.
type PageID struct{ slug string }

// PageIDFromSlug validates a slug: [a-z0-9-]{1,64}.
func PageIDFromSlug(slug string) (PageID, error) {
	if len(slug) < 1 || len(slug) > MaxSlugLen {
		return PageID{}, ErrBadSlug
	}
	for i := 0; i < len(slug); i++ {
		b := slug[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
			return PageID{}, ErrBadSlug
		}
	}
	return PageID{slug}, nil
}

// PageIDFromFileName parses "<slug>.html".
func PageIDFromFileName(name string) (PageID, error) {
	slug, ok := strings.CutSuffix(name, ".html")
	if !ok {
		return PageID{}, ErrNotHTML
	}
	return PageIDFromSlug(slug)
}

func (p PageID) Slug() string     { return p.slug }
func (p PageID) String() string   { return p.slug }
func (p PageID) FileName() string { return p.slug + ".html" }

// URLPath is the path the canvas://localhost protocol serves this page at.
func (p PageID) URLPath() string { return "/pages/" + p.slug + ".html" }

// PageRef is a page as the tray and the window title see it.
type PageRef struct {
	ID PageID
	// Title is the <title> text, or the slug when the page has none.
	Title    string
	Modified time.Time
}
