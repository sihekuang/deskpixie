package canvas

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Area is which root a request resolved into.
type Area int

const (
	// AreaDS is the bundled design system (read-only).
	AreaDS Area = iota
	// AreaPages is the agent-written pages folder.
	AreaPages
)

var (
	// ErrBadPath: malformed or dangerous path (traversal, bad escape, empty segment, dotfile).
	ErrBadPath = errors.New("bad path")
	// ErrNotFound: well-formed but nothing to serve there.
	ErrNotFound = errors.New("not found")
	// ErrEscapes: the file exists but its canonical path is outside the root (symlink escape).
	ErrEscapes = errors.New("escapes root")
)

// Resolved is a request mapped onto a file.
type Resolved struct {
	Area Area
	// File is the canonical path of an existing regular file inside the area's root.
	File string
	// Page is set for AreaPages.
	Page *PageID
}

// Resolver maps canvas://localhost/{ds|pages}/<path> onto files. Only /ds/** and
// /pages/<slug>.html are served. Traversal in any spelling (.., %2e%2e, %2f, %5c), absolute or
// empty segments, dotfiles and symlinks whose canonical path leaves the root are refused.
type Resolver struct{ dsRoot, pagesRoot string }

func NewResolver(dsRoot, pagesRoot string) *Resolver {
	return &Resolver{dsRoot: dsRoot, pagesRoot: pagesRoot}
}

// ResolveFile takes the raw, still percent-encoded URL path, e.g. /ds/tokens.css.
func (r *Resolver) ResolveFile(path string) (Resolved, error) {
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return Resolved{}, ErrBadPath
	}
	var segments []string
	for _, raw := range strings.Split(rest, "/") {
		seg, err := decodeSegment(raw)
		if err != nil {
			return Resolved{}, err
		}
		segments = append(segments, seg)
	}
	var (
		area Area
		root string
	)
	switch segments[0] {
	case "ds":
		area, root = AreaDS, r.dsRoot
	case "pages":
		area, root = AreaPages, r.pagesRoot
	default:
		return Resolved{}, ErrNotFound
	}
	tail := segments[1:]
	if len(tail) == 0 {
		return Resolved{}, ErrNotFound
	}
	var page *PageID
	if area == AreaPages {
		if len(tail) != 1 {
			return Resolved{}, ErrNotFound
		}
		id, err := PageIDFromFileName(tail[0])
		if err != nil {
			return Resolved{}, ErrNotFound
		}
		page = &id
	}
	file := filepath.Join(append([]string{root}, tail...)...)
	if page != nil {
		// A page is a plain file in the folder: not a symlink, and spelled exactly as asked
		// (case-insensitive file systems would otherwise serve Report.html for "report").
		name := page.FileName()
		meta, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			return Resolved{}, ErrNotFound
		}
		if meta.Mode()&os.ModeSymlink != 0 {
			return Resolved{}, ErrEscapes
		}
		if !meta.Mode().IsRegular() {
			return Resolved{}, ErrNotFound
		}
		if !hasExactName(root, name) {
			return Resolved{}, ErrNotFound
		}
	}
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Resolved{}, ErrNotFound
	}
	canonFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		return Resolved{}, ErrNotFound
	}
	if canonFile != canonRoot && !strings.HasPrefix(canonFile, canonRoot+string(filepath.Separator)) {
		return Resolved{}, ErrEscapes
	}
	if info, err := os.Stat(canonFile); err != nil || !info.Mode().IsRegular() {
		return Resolved{}, ErrNotFound
	}
	return Resolved{Area: area, File: canonFile, Page: page}, nil
}

// hasExactName reports whether the directory holds an entry spelled exactly name.
func hasExactName(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// decodeSegment percent-decodes one segment, then refuses anything that could change the path's shape.
func decodeSegment(raw string) (string, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		if raw[i] != '%' {
			out = append(out, raw[i])
			i++
			continue
		}
		if i+3 > len(raw) {
			return "", ErrBadPath
		}
		hi, ok1 := unhex(raw[i+1])
		lo, ok2 := unhex(raw[i+2])
		if !ok1 || !ok2 {
			return "", ErrBadPath
		}
		out = append(out, hi<<4|lo)
		i += 3
	}
	if !utf8.Valid(out) {
		return "", ErrBadPath
	}
	seg := string(out)
	if seg == "" || strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "/\\\x00:") {
		return "", ErrBadPath
	}
	return seg, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// ContentType is the Content-Type for a served file, by extension.
func ContentType(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "html":
		return "text/html; charset=utf-8"
	case "css":
		return "text/css; charset=utf-8"
	case "js", "mjs":
		return "text/javascript; charset=utf-8"
	case "json":
		return "application/json"
	case "md":
		return "text/markdown; charset=utf-8"
	case "txt":
		return "text/plain; charset=utf-8"
	case "svg":
		return "image/svg+xml"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "woff2":
		return "font/woff2"
	case "woff":
		return "font/woff"
	}
	return "application/octet-stream"
}

// Response is the result of Resolve, as the canvas.resolve bridge method returns it. The Swift
// scheme handler reads File (when Status is 200), adds the meta CSP to HTML bodies (see
// WithMetaCSP) and sends Headers.
type Response struct {
	Status      int               `json:"status"`
	ContentType string            `json:"contentType"`
	File        string            `json:"file,omitempty"`
	Headers     map[string]string `json:"headers"`
}

func secured() map[string]string {
	return map[string]string{
		"Content-Security-Policy": CSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
}

// Resolve maps a request path to a Response. Refusals are 404 responses (with the security
// headers) rather than errors, as the Rust protocol handler does; the error is reserved for
// internal failures and is currently always nil. A page over MaxPageBytes is refused too.
func (r *Resolver) Resolve(path string) (Response, error) {
	notFound := Response{Status: 404, ContentType: "text/plain; charset=utf-8", Headers: secured()}
	res, err := r.ResolveFile(path)
	if err != nil {
		return notFound, nil
	}
	if res.Area == AreaPages {
		if info, err := os.Stat(res.File); err != nil || info.Size() > MaxPageBytes {
			return notFound, nil
		}
	}
	return Response{Status: 200, ContentType: ContentType(res.File), File: res.File, Headers: secured()}, nil
}

// WithMetaCSP adds <meta http-equiv="Content-Security-Policy"> (the same policy as the header,
// as a second layer). It goes after a leading doctype so the page does not drop into quirks mode.
func WithMetaCSP(body []byte) []byte {
	meta := []byte(`<meta http-equiv="Content-Security-Policy" content="` + CSP + `">`)
	bom := 0
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		bom = 3
	}
	lead := bom
	for lead < len(body) && isASCIISpace(body[lead]) {
		lead++
	}
	at := 0
	if end := min(lead+9, len(body)); strings.EqualFold(string(body[lead:end]), "<!doctype") {
		for i := lead; i < len(body); i++ {
			if body[i] == '>' {
				at = i + 1
				break
			}
		}
	}
	if at == 0 {
		at = bom
	}
	out := make([]byte, 0, len(body)+len(meta))
	out = append(out, body[:at]...)
	out = append(out, meta...)
	return append(out, body[at:]...)
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}
