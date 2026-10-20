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
	if q, ok := listQuestion(text); ok {
		return capGist(q), true
	}
	raw := text
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
		q := last[len(last)-1]
		if options := optionsBefore(raw); len(options) > 1 && whichQuestion(q) {
			q = strings.TrimSuffix(q, "?") + ": " + strings.Join(options[:len(options)-1], ", ") + " or " + options[len(options)-1] + "?"
		}
		return capGist(q), true
	}
	// A question to you with one more sentence after it ("Should I run both? It takes ten
	// minutes.") still asks.
	for i := len(last) - 1; i >= 0 && i >= len(last)-2; i-- {
		if strings.HasSuffix(last[i], "?") && asksYou(last[i]) {
			return capGist(strings.Join(last[i:], " ")), true
		}
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

// asksYou: a question put to you ("should I…?", "how do you want…?"), not one the agent asks
// itself ("why did it fail?").
func asksYou(q string) bool {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' })
	if len(words) == 0 || !questionWords[words[0]] {
		return false
	}
	for _, w := range words {
		switch w {
		case "you", "i", "me", "we", "i'd", "i'll":
			return true
		}
	}
	return false
}

// whichQuestion: a short pick ("Which do you want?") that leaves its options to a list above.
func whichQuestion(q string) bool {
	words := strings.Fields(strings.ToLower(q))
	return len(words) <= 5 && len(words) > 0 && words[0] == "which"
}

var gistBoldLead = regexp.MustCompile(`^\*\*(.+?)\*\*`)

// optionsBefore: the options of the nearest list in the paragraphs before the last one, each
// by its bold lead or its first sentence, at most six words.
func optionsBefore(text string) []string {
	paras := gistParagraph.Split(strings.TrimSpace(text), -1)
	for i := len(paras) - 2; i >= 0 && i >= len(paras)-4; i-- {
		var out []string
		for _, line := range strings.Split(paras[i], "\n") {
			if !gistListItem.MatchString(line) {
				continue
			}
			item := strings.TrimSpace(gistMarker.ReplaceAllString(line, ""))
			if m := gistBoldLead.FindStringSubmatch(item); m != nil {
				item = m[1]
			} else {
				item = sentences(strings.Join(strings.Fields(gistEmphasis.ReplaceAllString(item, "")), " "))[0]
			}
			words := strings.Fields(strings.TrimRight(item, ".;,:?!"))
			if len(words) > 6 {
				words = words[:6]
			}
			if len(words) > 0 {
				first, size := utf8.DecodeRuneInString(words[0])
				words[0] = string(unicode.ToLower(first)) + words[0][size:]
				out = append(out, strings.Join(words, " "))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// questionWords open a lead-in that asks, when it ends in ":" ("Would you like me to:").
var questionWords = map[string]bool{"would": true, "should": true, "shall": true, "do": true, "does": true,
	"want": true, "which": true, "what": true, "can": true, "could": true, "may": true, "how": true}

// listQuestion: a reply that ends asking you to pick from a list ("Would you like me to:" then
// "- commit", "- push") is one question: "Would you like me to commit or push?".
func listQuestion(text string) (string, bool) {
	paras := gistParagraph.Split(strings.TrimSpace(text), -1)
	var lead, items []string
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		switch {
		case gistListItem.MatchString(line):
			item := gistEmphasis.ReplaceAllString(gistMarker.ReplaceAllString(line, ""), "")
			items = append(items, strings.TrimRight(strings.Join(strings.Fields(item), " "), ".;,:?!"))
		case len(items) > 0 && strings.TrimSpace(line) != "":
			return "", false // words after the list: not a list question
		default:
			lead = append(lead, line)
		}
	}
	if len(items) == 0 {
		return "", false
	}
	if strings.TrimSpace(strings.Join(lead, "")) == "" && len(paras) > 1 {
		lead = []string{paras[len(paras)-2]}
	}
	in := strings.Join(strings.Fields(gistEmphasis.ReplaceAllString(strings.Join(lead, " "), "")), " ")
	first := strings.ToLower(strings.Trim(strings.SplitN(in, " ", 2)[0], ".,:;"))
	var q string
	switch {
	case strings.HasSuffix(in, "?"):
		q = strings.TrimSuffix(in, "?") + ": "
	case strings.HasSuffix(in, ":") && questionWords[first]:
		q = strings.TrimSuffix(in, ":") + " "
	default:
		return "", false
	}
	if ss := sentences(q); len(ss) > 1 { // only the question's own sentence leads in
		q = ss[len(ss)-1] + " "
	}
	last := len(items) - 1
	if last == 0 {
		return q + items[0] + "?", true
	}
	return q + strings.Join(items[:last], ", ") + " or " + items[last] + "?", true
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

// OutdatedRetellMs: a session's older pix is told again only after this long, across app runs.
const OutdatedRetellMs Millis = 6 * 60 * 60 * 1000

// OutdatedLine tells that session label runs an older pix than the app's, and how to update it
// (#191): restart it with pix, resuming its conversation.
func OutdatedLine(label, tool string) string {
	resume := "pix " + tool + " --resume"
	if tool == "codex" {
		resume = "pix codex resume"
	}
	return label + " runs an older pix than the app: restart it with " + resume + " to update it."
}

// SessionTurnBrief is the bubble's short form of a session's turn (#197): its gist alone, the
// session's label on the title line.
func SessionTurnBrief(gist string) string {
	if gist == "" {
		return "Finished."
	}
	if r, _ := utf8.DecodeLastRuneInString(gist); !strings.ContainsRune(".!?…", r) {
		gist += "."
	}
	return upperFirst(gist)
}

// OutdatedBrief is the bubble's short form of OutdatedLine (#197).
func OutdatedBrief(tool string) string {
	if tool == "codex" {
		return "Older pix: restart with pix codex resume."
	}
	return "Older pix: restart with pix " + tool + " --resume."
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

// upperFirst capitalizes the first letter.
func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
