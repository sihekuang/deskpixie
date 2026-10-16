package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The gist of a pix session's turn (#114, spec §4): one sentence of its final reply, by text
// rules (no model call): the outcome agents usually end with, or the question they end on.

// MaxGistRunes caps the gist.
const MaxGistRunes = 120

var (
	gistFence     = regexp.MustCompile("(?s)```.*?(?:```|$)")
	gistTable     = regexp.MustCompile(`(?m)^[ \t]*\|.*$`)
	gistHeading   = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	gistListItem  = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+.*$`)
	gistMarker    = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d+[.)])[ \t]+`)
	gistEmphasis  = regexp.MustCompile("\\*\\*|__|\\*|`")
	gistParagraph = regexp.MustCompile(`\n[ \t]*\n`)
	gistSentence  = regexp.MustCompile(`[.!?](?:\s|$)`)
)

// SessionGist is one sentence of reply, and whether it asks you something ("" when nothing
// readable is left, e.g. a code-only reply).
func SessionGist(reply string) (string, bool) {
	text := gistFence.ReplaceAllString(reply, "")
	text = gistTable.ReplaceAllString(text, "")
	text = gistHeading.ReplaceAllString(text, "")
	text = gistListItem.ReplaceAllStringFunc(text, func(item string) string {
		item = strings.TrimSpace(gistMarker.ReplaceAllString(item, ""))
		if r, _ := utf8.DecodeLastRuneInString(item); item != "" && !strings.ContainsRune(".!?:;", r) {
			item += "."
		}
		return item
	})
	text = gistEmphasis.ReplaceAllString(text, "")
	var paras []string
	for _, p := range gistParagraph.Split(text, -1) {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paras = append(paras, p)
		}
	}
	if len(paras) == 0 {
		return "", false
	}
	last := sentences(paras[len(paras)-1])
	if strings.HasSuffix(paras[len(paras)-1], "?") {
		return capGist(last[len(last)-1]), true
	}
	gist := last[0]
	if len(strings.Fields(gist)) < 3 {
		switch {
		case len(last) > 1:
			gist = last[1]
		case len(paras) > 1:
			gist = sentences(paras[len(paras)-2])[0]
		}
	}
	return capGist(gist), false
}

// sentences splits a non-empty paragraph after each . ! or ? that ends a sentence (never empty).
func sentences(p string) []string {
	var out []string
	start := 0
	for _, m := range gistSentence.FindAllStringIndex(p, -1) {
		if s := strings.TrimSpace(p[start : m[0]+1]); s != "" {
			out = append(out, s)
		}
		start = m[1]
	}
	if s := strings.TrimSpace(p[start:]); s != "" || len(out) == 0 {
		out = append(out, s)
	}
	return out
}

// capGist cuts s to MaxGistRunes on a word boundary, ending in "…".
func capGist(s string) string {
	if utf8.RuneCountInString(s) <= MaxGistRunes {
		return s
	}
	cut := string([]rune(s)[:MaxGistRunes-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// SessionTurnLine is what Pixie says of a finished turn: done with its gist, asking its
// question, or just finished when the reply had nothing readable.
func SessionTurnLine(label, gist string, asking bool) string {
	switch {
	case gist == "":
		return label + " finished."
	case asking:
		return label + " is asking: " + lowerFirst(gist)
	}
	if r, _ := utf8.DecodeLastRuneInString(gist); !strings.ContainsRune(".!?…", r) {
		gist += "."
	}
	return label + " is done: " + lowerFirst(gist)
}

// lowerFirst lowercases the first letter, unless the word is "I" (or "I'm", …) or an acronym.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	next, _ := utf8.DecodeRuneInString(s[n:])
	if unicode.IsUpper(next) || (r == 'I' && (next == ' ' || next == '\'' || next == '’' || n == len(s))) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}
