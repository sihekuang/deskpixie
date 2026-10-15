// Package describe turns what an agent session is given into its description: a few plain
// words saying what it is doing, never a fragment of a message the session received (#89).
package describe

import (
	"strings"
	"unicode/utf8"
)

// Max is a description's length in runes.
const Max = 60

// MinWords is the fewest words of a prompt that describe a task.
const MinWords = 3

// notYours start a prompt that you didn't type: another session's message or a subagent's
// report (Claude Code wraps both as "Another Claude session sent a message: <agent-message …>"),
// a background task's notice, a slash command's echo, local command output.
var notYours = []string{
	"Another Claude session sent a message",
	"[Subagent hand-back]",
	"Caveat: The messages below were generated",
	"[Request interrupted",
}

// leadIns are dropped from the start of a prompt ("please fix …" → "fix …").
var leadIns = []string{"please ", "can you ", "could you ", "would you ", "hey ", "ok ", "okay "}

// FromPrompt is the description a prompt gives: its first line, cleaned, or "" when the
// prompt isn't one you typed.
func FromPrompt(prompt string) string {
	p := strings.TrimSpace(prompt)
	if p == "" || startsWithTag(p) {
		return ""
	}
	for _, s := range notYours {
		if strings.HasPrefix(p, s) {
			return ""
		}
	}
	if isCommand(p) {
		return ""
	}
	if i := strings.IndexByte(p, '\n'); i >= 0 {
		p = p[:i]
	}
	for again := true; again; {
		again = false
		for _, l := range leadIns {
			if len(p) > len(l) && strings.EqualFold(p[:len(l)], l) {
				p, again = strings.TrimSpace(p[len(l):]), true
			}
		}
	}
	if len(strings.Fields(p)) < MinWords { // "yes", "got it": a reply, not a task
		return ""
	}
	return Clean(p)
}

// Clean is s on one line, spaces collapsed, at most Max runes (cut at a word when it can), or
// "" when it is a fragment of a message rather than a description.
func Clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if startsWithTag(s) || strings.Contains(s, "<agent-message") || strings.HasPrefix(s, "[Subagent") {
		return ""
	}
	if utf8.RuneCountInString(s) <= Max {
		return s
	}
	r := []rune(s)[:Max+1]
	if i := strings.LastIndexByte(string(r), ' '); i > 0 {
		return strings.TrimSpace(string(r)[:i])
	}
	return string(r[:Max])
}

// isCommand reports whether p is a slash command such as /compact or /clear (but not a path).
func isCommand(p string) bool {
	word, _, _ := strings.Cut(p, " ")
	return len(word) > 1 && word[0] == '/' && !strings.Contains(word[1:], "/")
}

// startsWithTag reports whether s opens with markup like <task-notification> or <command-name>.
func startsWithTag(s string) bool {
	if len(s) < 3 || s[0] != '<' {
		return false
	}
	c := s[1]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '/' || c == '\\'
}
