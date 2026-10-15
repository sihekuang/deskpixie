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
//
// Only Enter on its own (a lone CR read, not after a backslash) submits. CR or LF inside a
// longer read is Option+Enter, Ctrl-J or a paste, which add a line to your draft. Enter on a
// line with text may also just accept a completion (@file), so the gate stays shut until the
// agent starts the turn (Started); a /command or an empty line needs no turn.
type Gate struct {
	mu       sync.Mutex
	pending  int  // runes typed since the last Enter (backspace takes one off)
	unsure   bool // a key we can't follow (arrows, other controls): wait for Enter or Ctrl-C
	awaiting bool // Enter on a line with text: shut until the turn starts
	slash    bool // the line began with /
	lastByte byte
	last     time.Time
}

// Started: the agent began a turn, so what you typed was sent.
func (g *Gate) Started() {
	g.mu.Lock()
	g.awaiting = false
	g.mu.Unlock()
}

// Keys takes every byte you type.
func (g *Gate) Keys(b []byte, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = now
	if len(b) == 1 && b[0] == 0x1b && g.pending == 0 {
		return // a lone Esc on an empty line (interrupting the agent) leaves nothing to follow
	}
	if len(b) == 1 && b[0] == '\r' && g.lastByte != '\\' { // Enter
		g.awaiting = g.awaiting || ((g.pending > 0 || g.unsure) && !g.slash)
		g.pending, g.unsure, g.slash, g.lastByte = 0, false, false, '\r'
		return
	}
	for _, c := range b {
		if g.pending == 0 && !g.unsure && c == '/' {
			g.slash = true
		}
		g.lastByte = c
		switch {
		case c == 0x03: // Ctrl-C clears the line
			g.pending, g.unsure, g.awaiting, g.slash = 0, false, false, false
		case c == '\r' || c == '\n': // a line added to the draft
			g.pending++
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
	if g.pending > 0 || g.unsure || g.awaiting {
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
