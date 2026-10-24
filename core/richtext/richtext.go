// Package richtext finds the parts of a message worth acting on (#277): links, files that
// exist, code, commits and email addresses, plus the bit of markdown sessions write (bold,
// lists, headings). The shell draws the spans and acts on a click; what each span is, and
// whether a path is real, is decided here so every platform shows the same.
package richtext

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Span kinds.
const (
	KindText   = "text"
	KindLink   = "link"
	KindPath   = "path"
	KindCode   = "code"
	KindCommit = "commit"
	KindEmail  = "email"
)

// Span is one piece of a message. Raw is the source it came from: the spans' Raws join to the
// message, so the shell can show a prefix while it is typed out. Text is what is shown (a
// markdown link's label, code without its backticks).
type Span struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Raw  string `json:"raw"`
	Bold bool   `json:"bold,omitempty"`
	// URL is a link's address.
	URL string `json:"url,omitempty"`
	// Path is a file or folder that exists, absolute; Line and Col are where in it (0 = none).
	Path string `json:"path,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
	// Reveal: opening it would launch or run something (an app, a package, a script, an
	// executable), so a click only shows it in the file manager.
	Reveal bool `json:"reveal,omitempty"`
	Line   int  `json:"line,omitempty"`
	Col    int  `json:"col,omitempty"`
	// Open is how a path opens: addresses to try in order (OpenPlan).
	Open []string `json:"open,omitempty"`
}

// Resolver says which paths are real: relative ones against Folders (the sessions' folders,
// first match wins), "~/" against Home, absolute ones as they are.
type Resolver struct {
	Folders []string
	Home    string
}

var (
	heading  = regexp.MustCompile(`^#{1,6}[ \t]+`)
	listItem = regexp.MustCompile(`^([ \t]*)[-*+][ \t]+`)
	// A label stops at the next bracket and both parts are capped, so a line of brackets
	// doesn't scan to its end once per bracket.
	mdLink   = regexp.MustCompile(`^\[([^\[\]\n]{1,300})\]\((https?://[^)\s]{1,1000})\)`)
	webLink  = regexp.MustCompile(`^(?:https?://[^\s/]+|www\.[^\s/]+\.[a-z]{2,})\S*$`)
	email    = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$`)
	commit   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	position = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?$`)
	bareFile = regexp.MustCompile(`^[\w.-]*[\w-]\.[A-Za-z0-9]{1,10}$`)
)

// MaxText is the longest message Parse reads; a longer one (a dump) is left plain.
const MaxText = 16 << 10

// launchers are files whose default app runs or installs them rather than showing them.
var launchers = map[string]bool{
	".app": true, ".pkg": true, ".mpkg": true, ".bundle": true, ".framework": true, ".plugin": true,
	".kext": true, ".prefpane": true, ".saver": true, ".appex": true, ".xpc": true, ".workflow": true,
	".action": true, ".command": true, ".tool": true, ".terminal": true, ".webloc": true, ".inetloc": true,
	".fileloc": true, ".url": true, ".jar": true, ".mobileconfig": true, ".shortcut": true, ".scpt": true,
	".scptd": true, ".applescript": true, ".dmg": true, ".exe": true, ".msi": true, ".bat": true, ".cmd": true,
	".ps1": true, ".lnk": true, ".desktop": true, ".appimage": true, ".deb": true, ".rpm": true,
}

// Parse splits text into spans.
func Parse(text string, r Resolver) []Span {
	if len(text) > MaxText {
		return []Span{{Kind: KindText, Text: text, Raw: text}}
	}
	var out []Span
	lines := strings.SplitAfter(text, "\n")
	for _, line := range lines {
		body, nl := strings.CutSuffix(line, "\n")
		bold := false
		if m := heading.FindString(body); m != "" {
			out = append(out, Span{Kind: KindText, Raw: m})
			body, bold = body[len(m):], true
		} else if m := listItem.FindStringSubmatch(body); m != nil {
			out = append(out, Span{Kind: KindText, Text: m[1] + "• ", Raw: m[0]})
			body = body[len(m[0]):]
		}
		out = append(out, r.inline(body, bold)...)
		if nl {
			out = append(out, Span{Kind: KindText, Text: "\n", Raw: "\n"})
		}
	}
	return merge(out)
}

// inline reads one line's code, bold and markdown links; the rest goes to words.
func (r Resolver) inline(s string, bold bool) []Span {
	var out []Span
	plain := 0 // start of the text not yet read as words
	flush := func(end int) {
		out = append(out, r.words(s[plain:end], bold)...)
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			if end := strings.IndexByte(s[i+1:], '`'); end > 0 {
				flush(i)
				content := s[i+1 : i+1+end]
				sp := r.code(content)
				sp.Raw, sp.Bold = s[i:i+end+2], bold
				out = append(out, sp)
				i += end + 2
				plain = i
				continue
			}
		case strings.HasPrefix(s[i:], "**"):
			if end := strings.Index(s[i+2:], "**"); end > 0 {
				flush(i)
				out = append(out, Span{Kind: KindText, Raw: "**", Bold: true})
				out = append(out, r.inline(s[i+2:i+2+end], true)...)
				out = append(out, Span{Kind: KindText, Raw: "**", Bold: true})
				i += end + 4
				plain = i
				continue
			}
		case s[i] == '[':
			if m := mdLink.FindStringSubmatch(s[i:]); m != nil {
				flush(i)
				out = append(out, Span{Kind: KindLink, Text: m[1], Raw: m[0], URL: m[2], Bold: bold})
				i += len(m[0])
				plain = i
				continue
			}
		}
		i++
	}
	flush(len(s))
	return out
}

// code is a code span: a link, commit or file it names on its own, else code.
func (r Resolver) code(content string) Span {
	if !strings.ContainsFunc(content, unicode.IsSpace) {
		if sp, ok := r.word(content); ok && sp.Kind != KindEmail {
			return sp
		}
	}
	return Span{Kind: KindCode, Text: content}
}

// words finds links, emails, commits and paths among plain words.
func (r Resolver) words(s string, bold bool) []Span {
	var out []Span
	text := func(t string) {
		if t != "" {
			out = append(out, Span{Kind: KindText, Text: t, Raw: t, Bold: bold})
		}
	}
	for len(s) > 0 {
		start := strings.IndexFunc(s, func(c rune) bool { return !unicode.IsSpace(c) })
		if start < 0 {
			text(s)
			break
		}
		text(s[:start])
		s = s[start:]
		end := strings.IndexFunc(s, unicode.IsSpace)
		if end < 0 {
			end = len(s)
		}
		tok := s[:end]
		s = s[end:]
		core := strings.TrimLeft(tok, `([{<"'`)
		lead := tok[:len(tok)-len(core)]
		core = strings.TrimRight(core, `.,;:!?)]}>"'`)
		trail := tok[len(lead)+len(core):]
		sp, ok := r.word(core)
		if !ok {
			text(tok)
			continue
		}
		text(lead)
		sp.Text, sp.Raw, sp.Bold = core, core, bold
		out = append(out, sp)
		text(trail)
	}
	return out
}

// word is what one word is, if it is anything.
func (r Resolver) word(w string) (Span, bool) {
	switch {
	case w == "":
		return Span{}, false
	case webLink.MatchString(w):
		url := w
		if strings.HasPrefix(w, "www.") {
			url = "https://" + w
		}
		return Span{Kind: KindLink, Text: w, URL: url}, true
	case email.MatchString(w):
		return Span{Kind: KindEmail, Text: w}, true
	case commit.MatchString(w) && strings.ContainsAny(w, "0123456789") && strings.ContainsAny(w, "abcdef"):
		return Span{Kind: KindCommit, Text: w}, true
	}
	if sp, ok := r.path(w); ok {
		sp.Text = w
		return sp, true
	}
	return Span{}, false
}

// path is w as a file or folder that exists, with the line and column after it, if any.
func (r Resolver) path(w string) (Span, bool) {
	p, line, col := w, 0, 0
	if m := position.FindStringSubmatch(w); m != nil {
		p = m[1]
		line, _ = strconv.Atoi(m[2])
		col, _ = strconv.Atoi(m[3])
	}
	var candidates []string
	switch {
	case strings.HasPrefix(p, "~/"):
		if r.Home != "" {
			candidates = []string{filepath.Join(r.Home, p[2:])}
		}
	case filepath.IsAbs(p):
		candidates = []string{p}
	case strings.Contains(p, "/") || bareFile.MatchString(p):
		for _, f := range r.Folders {
			candidates = append(candidates, filepath.Join(f, p))
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil {
			reveal := launchers[strings.ToLower(filepath.Ext(filepath.Clean(c)))] ||
				!info.IsDir() && info.Mode()&0o111 != 0
			return Span{Kind: KindPath, Path: filepath.Clean(c), Dir: info.IsDir(), Reveal: reveal, Line: line, Col: col}, true
		}
	}
	return Span{}, false
}

// merge joins neighbouring plain text of the same weight.
func merge(spans []Span) []Span {
	var out []Span
	for _, s := range spans {
		if n := len(out); n > 0 && s.Kind == KindText && out[n-1].Kind == KindText && out[n-1].Bold == s.Bold {
			out[n-1].Text += s.Text
			out[n-1].Raw += s.Raw
			continue
		}
		out = append(out, s)
	}
	return out
}
