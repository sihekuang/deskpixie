package canvas

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxPageBytes: pages larger than this are never shown (spec section 5).
const MaxPageBytes = 5 * 1024 * 1024

// Only the start of a page is read to find its title.
const titleScanBytes = 64 * 1024

// Longest title kept, in characters.
const maxTitleChars = 120

// Scan is what is in the pages folder right now.
type Scan struct {
	// Pages are the valid pages, newest first (ties: slug ascending).
	Pages []PageRef
	// Invalid are file names that are not valid page names (sorted). Dotfiles are skipped silently.
	Invalid []string
}

// LoadError is why a page could not be loaded.
type LoadError struct {
	TooLarge bool
	Size     int64  // set when TooLarge
	Msg      string // set otherwise
}

func (e *LoadError) Error() string {
	if e.TooLarge {
		return fmt.Sprintf("page is %d bytes; the limit is 5 MB", e.Size)
	}
	return "unreadable: " + e.Msg
}

// PageStore lists pages newest first with their titles, loads one page with the size cap,
// and prunes old pages. All file access goes through PageFS.
type PageStore struct{ fs PageFS }

func NewPageStore(fs PageFS) *PageStore { return &PageStore{fs: fs} }

func (s *PageStore) Scan() (Scan, error) {
	entries, err := s.fs.List()
	if err != nil {
		return Scan{}, err
	}
	var scan Scan
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		id, err := PageIDFromFileName(e.Name)
		if err != nil {
			scan.Invalid = append(scan.Invalid, e.Name)
			continue
		}
		title := id.Slug()
		if b, err := s.fs.ReadLimited(e.Name, titleScanBytes); err == nil {
			if len(b) > titleScanBytes {
				b = b[:titleScanBytes]
			}
			if t, ok := ExtractTitle(lossy(b)); ok {
				title = t
			}
		}
		scan.Pages = append(scan.Pages, PageRef{ID: id, Title: title, Modified: e.Modified})
	}
	sort.SliceStable(scan.Pages, func(i, j int) bool {
		a, b := scan.Pages[i], scan.Pages[j]
		if !a.Modified.Equal(b.Modified) {
			return a.Modified.After(b.Modified)
		}
		return a.ID.slug < b.ID.slug
	})
	sort.Strings(scan.Invalid)
	return scan, nil
}

// Stamp is the (modified, length) of a page file; OK is false when it cannot be listed.
type Stamp struct {
	ModNano int64
	Len     int64
	OK      bool
}

// Stamp returns the current stamp of a page's file.
func (s *PageStore) Stamp(id PageID) Stamp {
	name := id.FileName()
	entries, err := s.fs.List()
	if err != nil {
		return Stamp{}
	}
	for _, e := range entries {
		if e.Name == name {
			return Stamp{ModNano: e.Modified.UnixNano(), Len: e.Len, OK: true}
		}
	}
	return Stamp{}
}

// Get loads one page: its current PageRef and bytes. Refuses pages over MaxPageBytes.
func (s *PageStore) Get(id PageID) (PageRef, []byte, error) {
	name := id.FileName()
	entries, err := s.fs.List()
	if err != nil {
		return PageRef{}, nil, &LoadError{Msg: err.Error()}
	}
	var entry *FileEntry
	for i := range entries {
		if entries[i].Name == name {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return PageRef{}, nil, &LoadError{Msg: "not found"}
	}
	if entry.Len > MaxPageBytes {
		return PageRef{}, nil, &LoadError{TooLarge: true, Size: entry.Len}
	}
	b, err := s.fs.ReadLimited(name, MaxPageBytes)
	if err != nil {
		return PageRef{}, nil, &LoadError{Msg: err.Error()}
	}
	if int64(len(b)) > MaxPageBytes {
		return PageRef{}, nil, &LoadError{TooLarge: true, Size: int64(len(b))}
	}
	title, ok := ExtractTitle(lossy(b))
	if !ok {
		title = id.Slug()
	}
	return PageRef{ID: id, Title: title, Modified: entry.Modified}, b, nil
}

// Prune deletes all but the newest keep pages (startup only). Invalid names are left alone.
// It returns the removed pages; a page that cannot be deleted is skipped.
func (s *PageStore) Prune(keep int) ([]PageID, error) {
	scan, err := s.Scan()
	if err != nil {
		return nil, err
	}
	var removed []PageID
	for i, p := range scan.Pages {
		if i < keep {
			continue
		}
		if s.fs.Remove(p.ID.FileName()) == nil {
			removed = append(removed, p.ID)
		}
	}
	return removed, nil
}

func lossy(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// ExtractTitle returns the text of the first <title> element (entities decoded, whitespace
// collapsed), and false if it is missing or blank. Titles inside <script> do not count.
func ExtractTitle(html string) (string, bool) {
	lower := asciiLower(html)
	scripts := scriptRanges(lower)
	from, nextScript := 0, 0
	open := -1
	for {
		i := strings.Index(lower[from:], "<title")
		if i < 0 {
			return "", false
		}
		at := from + i
		from = at + len("<title")
		// Candidates come in increasing order and scripts is sorted, so one moving index is enough.
		for nextScript < len(scripts) && scripts[nextScript][1] <= at {
			nextScript++
		}
		inScript := nextScript < len(scripts) && at >= scripts[nextScript][0] && at < scripts[nextScript][1]
		exact := false
		if from < len(lower) {
			switch lower[from] {
			case '>', '/', ' ', '\t', '\n', '\r':
				exact = true
			}
		}
		if exact && !inScript {
			open = at
			break
		}
	}
	gt := strings.IndexByte(lower[open:], '>')
	if gt < 0 {
		return "", false
	}
	start := open + gt + 1
	e := strings.Index(lower[start:], "</title")
	if e < 0 {
		return "", false
	}
	text := decodeEntities(html[start : start+e])
	title := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(title) > maxTitleChars {
		title = string([]rune(title)[:maxTitleChars])
	}
	return title, title != ""
}

// scriptRanges are the byte ranges of <script> element bodies (to the closing tag, or the end).
func scriptRanges(lower string) [][2]int {
	var out [][2]int
	from := 0
	for {
		i := strings.Index(lower[from:], "<script")
		if i < 0 {
			return out
		}
		open := from + i
		end := len(lower)
		if e := strings.Index(lower[open:], "</script"); e >= 0 {
			end = open + e
		}
		out = append(out, [2]int{open, end})
		from = max(end, open+1)
	}
}

func decodeEntities(s string) string {
	for _, p := range [][2]string{
		{"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&apos;", "'"},
		{"&nbsp;", " "}, {"&mdash;", "—"}, {"&ndash;", "–"}, {"&amp;", "&"},
	} {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	return s
}
