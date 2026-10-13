package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// `<spoken ref="job_…" status="…">…</spoken>` blocks in an agent's reply (port of spoken.rs).
// Every block is returned; text is cleaned of markup and capped at 2 sentences / 35 words.
// The input is untrusted coordinator text: it is size-bounded and every scan is linear.

const (
	// SpokenMaxSentences: at most this many sentences are spoken per block.
	SpokenMaxSentences = 2
	// SpokenMaxWords: at most this many words are spoken per block.
	SpokenMaxWords = 35
	// MaxParseBytes: ParseSpoken looks at no more than this many bytes of a message.
	MaxParseBytes = 64 * 1024
	// MaxFallbackBytes: SpokenFallback looks at no more than this many bytes of a message.
	MaxFallbackBytes = 16 * 1024
)

// SpokenStatus is a block's status attribute. SpokenNone: missing or unknown.
type SpokenStatus int

const (
	SpokenNone SpokenStatus = iota
	SpokenWorking
	SpokenDone
	SpokenFailed
	SpokenQuestion
)

func (s SpokenStatus) String() string {
	switch s {
	case SpokenWorking:
		return "working"
	case SpokenDone:
		return "done"
	case SpokenFailed:
		return "failed"
	case SpokenQuestion:
		return "question"
	}
	return "none"
}

func parseSpokenStatus(s string) SpokenStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "working":
		return SpokenWorking
	case "done":
		return SpokenDone
	case "failed":
		return SpokenFailed
	case "question":
		return SpokenQuestion
	}
	return SpokenNone
}

// Spoken is one block.
type Spoken struct {
	// Reference is the ref attribute as written (matched against open jobs by the caller); nil
	// when missing.
	Reference *string
	Status    SpokenStatus
	// Text is cleaned and capped; never empty.
	Text string
}

// truncateBytes cuts s to at most max bytes, on a rune boundary.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// ParseSpoken returns every <spoken> block, in order. Blocks whose text is empty after
// cleaning are skipped. An unclosed block runs to the end of the message; an unterminated
// `<spoken` tag is ignored.
func ParseSpoken(message string) []Spoken {
	var out []Spoken
	rest := truncateBytes(message, MaxParseBytes)
	for {
		start, ok := findOpenTag(rest)
		if !ok {
			break
		}
		after := rest[start+len("<spoken"):]
		gt, ok := tagEnd(after)
		if !ok {
			break
		}
		attrs := parseAttrs(after[:gt])
		bodyAndRest := after[gt+1:]
		var body, next string
		if end := strings.Index(bodyAndRest, "</spoken>"); end >= 0 {
			body, next = bodyAndRest[:end], bodyAndRest[end+len("</spoken>"):]
		} else {
			body, next = bodyAndRest, ""
		}
		if text := Cap(Clean(body)); text != "" {
			s := Spoken{Text: text}
			if ref, ok := attr(attrs, "ref"); ok {
				s.Reference = &ref
			}
			if st, ok := attr(attrs, "status"); ok {
				s.Status = parseSpokenStatus(st)
			}
			out = append(out, s)
		}
		rest = next
	}
	return out
}

// SpokenFallback is what to say when a reply has no <spoken> block: its first sentences,
// cleaned and capped.
func SpokenFallback(message string) string {
	return Cap(Clean(truncateBytes(message, MaxFallbackBytes)))
}

// findOpenTag finds `<spoken` followed by whitespace or `>` (so `<spokenword>` doesn't count).
func findOpenTag(s string) (int, bool) {
	from := 0
	for {
		i := strings.Index(s[from:], "<spoken")
		if i < 0 {
			return 0, false
		}
		at := from + i
		c, size := utf8.DecodeRuneInString(s[at+len("<spoken"):])
		if size > 0 && (c == '>' || unicode.IsSpace(c)) {
			return at, true
		}
		from = at + 1
	}
}

// tagEnd is the index of the `>` that ends a tag, ignoring any `>` inside quotes.
func tagEnd(s string) (int, bool) {
	var quote rune
	for i, c := range s {
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i, true
		}
	}
	return 0, false
}

type attrPair struct{ name, value string }

// parseAttrs is a quote-aware attribute tokenizer: name="value" / name='value' pairs separated
// by whitespace. A quoted value is opaque, so it can never yield a nested attribute.
func parseAttrs(s string) []attrPair {
	var out []attrPair
	rest := s
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			break
		}
		nameEnd := strings.IndexFunc(rest, func(c rune) bool { return unicode.IsSpace(c) || c == '=' })
		if nameEnd < 0 {
			nameEnd = len(rest)
		}
		name := rest[:nameEnd]
		rest = strings.TrimLeftFunc(rest[nameEnd:], unicode.IsSpace)
		afterEq, ok := strings.CutPrefix(rest, "=")
		if !ok {
			if nameEnd == 0 {
				// Defensive: always make progress.
				_, size := utf8.DecodeRuneInString(rest)
				rest = rest[size:]
			}
			continue
		}
		rest = strings.TrimLeftFunc(afterEq, unicode.IsSpace)
		if q, _ := utf8.DecodeRuneInString(rest); rest != "" && (q == '"' || q == '\'') {
			value := rest[1:]
			end := strings.IndexRune(value, q)
			if end < 0 {
				break
			}
			if name != "" {
				out = append(out, attrPair{name, strings.TrimSpace(value[:end])})
			}
			rest = value[end+1:]
		} else {
			// Unquoted value: not supported; skip it.
			end := strings.IndexFunc(rest, unicode.IsSpace)
			if end < 0 {
				end = len(rest)
			}
			rest = rest[end:]
		}
	}
	return out
}

func attr(attrs []attrPair, name string) (string, bool) {
	for _, a := range attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

// stripFences replaces every ``` fenced block with a space in one linear pass. An unclosed
// fence drops the rest.
func stripFences(text string) string {
	var out strings.Builder
	rest := text
	for {
		open := strings.Index(rest, "```")
		if open < 0 {
			break
		}
		out.WriteString(rest[:open])
		out.WriteByte(' ')
		after := rest[open+3:]
		close := strings.Index(after, "```")
		if close < 0 {
			return out.String()
		}
		rest = after[close+3:]
	}
	out.WriteString(rest)
	return out.String()
}

// isFormatChar: zero-width, bidi-control and BOM characters, invisible to the eye and hostile
// to speech and logs.
func isFormatChar(c rune) bool {
	return c >= '\u200B' && c <= '\u200F' || c >= '\u202A' && c <= '\u202E' || c >= '\u2066' && c <= '\u2069' || c == '\uFEFF'
}

// isControl is Rust's char::is_control (general category Cc).
func isControl(c rune) bool { return unicode.Is(unicode.Cc, c) }

// Clean makes speakable text: no code fences, tags, markdown markers, code ticks, control or
// format characters; whitespace collapsed. A `<` only starts a tag before a letter, `/` or `!`,
// and only if the tag closes; otherwise it is kept as text.
func Clean(text string) string {
	text = stripFences(text)
	var out strings.Builder
	skipUntil := 0
	noMoreGt := false
	for i, c := range text {
		if i < skipUntil {
			continue
		}
		switch {
		case c == '<' && !noMoreGt && startsTag(text[i+1:]):
			if gt := strings.IndexByte(text[i:], '>'); gt >= 0 {
				skipUntil = i + gt + 1
			} else {
				noMoreGt = true
				out.WriteRune(c)
			}
		case c == '`' || c == '*' || c == '#' || c == '~':
		case c == '_' || c == '|':
			out.WriteByte(' ')
		case isFormatChar(c):
		case isControl(c):
			out.WriteByte(' ')
		default:
			out.WriteRune(c)
		}
	}
	var words []string
	for _, w := range strings.Fields(out.String()) {
		if strings.Trim(w, "->=") != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

func startsTag(rest string) bool {
	n, size := utf8.DecodeRuneInString(rest)
	return size > 0 && (n < utf8.RuneSelf && unicode.IsLetter(n) || n == '/' || n == '!')
}

// Cap keeps at most 2 sentences and 35 words, cut at a sentence boundary. A first sentence
// longer than 35 words is cut at the word limit and ends with "…".
func Cap(text string) string {
	var out []string
	words := 0
	for k, s := range splitSentences(text) {
		if k >= SpokenMaxSentences {
			break
		}
		n := len(strings.Fields(s))
		if words+n > SpokenMaxWords {
			if len(out) == 0 {
				cut := strings.Fields(s)[:SpokenMaxWords]
				return strings.TrimRight(strings.Join(cut, " "), ",;:.") + "…"
			}
			break
		}
		words += n
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// splitSentences: sentences end at `.`, `!` or `?` followed by whitespace (or the end).
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i, c := range text {
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		end := i + utf8.RuneLen(c)
		if next, size := utf8.DecodeRuneInString(text[end:]); size == 0 || unicode.IsSpace(next) {
			if s := strings.TrimSpace(text[start:end]); s != "" {
				out = append(out, s)
			}
			start = end
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}
