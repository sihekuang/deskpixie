package pix

import (
	"regexp"
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

// Sent: the agent took a prompt you typed, so your line was sent however your terminal sent the
// Enter (#185): nothing of yours is left to mix with.
func (g *Gate) Sent() {
	g.mu.Lock()
	g.pending, g.unsure, g.awaiting, g.slash = 0, false, false, false
	g.mu.Unlock()
}

// Keys takes every byte you type. The terminal's own reports (focus, mouse) aren't keys (#138).
func (g *Gate) Keys(b []byte, now time.Time) {
	if b = withoutReports(b); len(b) == 0 {
		return
	}
	b = []byte(kittyKeys.Replace(string(b)))
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

// terminalReports is what a terminal sends that you didn't type (#138): its window gaining or
// losing focus (ESC[I, ESC[O), and SGR mouse reports (ESC[<b;x;yM or m), which fullscreen
// Claude Code asks for on every mouse move. And its answers to what an agent asks it at
// startup (#181): device attributes (ESC[?…c, ESC[>…c), keyboard protocol flags (ESC[?…u), a
// mode report (ESC[?…$y), the cursor position (ESC[row;colR), and DCS and OSC strings (its
// version, colors, Warp's own queries).
var terminalReports = regexp.MustCompile(`\x1b\[[IO]|\x1b\[<\d+;\d+;\d+[Mm]` +
	`|\x1b\[[?>][\d;]*c|\x1b\[\?\d*u|\x1b\[\?[\d;]*\$y|\x1b\[\d+;\d+R` +
	`|\x1bP[^\x1b]*\x1b\\|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func withoutReports(b []byte) []byte { return terminalReports.ReplaceAll(b, nil) }

// kittyKeys are Esc and Ctrl-C as the kitty keyboard protocol sends them, which Claude Code
// turns on (#181): read as the bytes they were before.
var kittyKeys = strings.NewReplacer("\x1b[27u", "\x1b", "\x1b[27;1u", "\x1b", "\x1b[99;5u", "\x03",
	"\x1b[13u", "\r", "\x1b[13;1u", "\r") // and Enter (#185)

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
