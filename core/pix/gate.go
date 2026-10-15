package pix

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// TypingQuiet is how long you must not have typed before pix types for Pixie.
const TypingQuiet = 1500 * time.Millisecond

// Gate follows what you type, so pix only types into the agent when it can't mix with your
// text: nothing typed since your last Enter, and quiet for TypingQuiet.
type Gate struct {
	mu      sync.Mutex
	pending int  // runes typed since the last Enter (backspace takes one off)
	unsure  bool // a key we can't follow (arrows, other controls): wait for Enter or Ctrl-C
	last    time.Time
}

// Keys takes every byte you type.
func (g *Gate) Keys(b []byte, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = now
	if len(b) == 1 && b[0] == 0x1b && g.pending == 0 {
		return // a lone Esc on an empty line (interrupting the agent) leaves nothing to follow
	}
	for _, c := range b {
		switch {
		case c == '\r' || c == '\n' || c == 0x03: // Enter, Ctrl-C
			g.pending, g.unsure = 0, false
		case c == 0x7f || c == 0x08: // backspace
			if g.pending > 0 {
				g.pending--
			}
		case c < 0x20: // escape sequences and other controls
			g.unsure = true
		case c&0xC0 == 0x80: // UTF-8 continuation byte: same rune
		default:
			g.pending++
		}
	}
}

// Clear reports whether typing into the agent now can't mix with your text.
func (g *Gate) Clear(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending > 0 || g.unsure {
		return false
	}
	return g.last.IsZero() || now.Sub(g.last) >= TypingQuiet
}

// Sanitize removes control characters (ESC included) except newlines, so a message can't
// leave the bracketed paste or press keys.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// Paste is s as a bracketed paste, then Enter.
func Paste(s string) []byte { return []byte("\x1b[200~" + s + "\x1b[201~\r") }
