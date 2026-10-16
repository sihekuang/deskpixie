package pix

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// screen is a small terminal emulator: enough of what Claude Code draws (cursor moves, erases,
// scrolling) to read its dialogs back as lines of text (#105). Colours and modes are ignored.
// Only the PTY reader goroutine touches it.
type screen struct {
	rows, cols int
	cells      [][]rune // 0: blank
	row, col   int
	top, bot   int // scroll region (inclusive)
	saved      [2]int
	pending    []byte // an escape sequence or UTF-8 rune cut off at the end of a write
}

func newScreen(rows, cols int) *screen {
	v := &screen{rows: rows, cols: cols}
	v.reset()
	return v
}

func (v *screen) reset() {
	v.cells = make([][]rune, v.rows)
	for i := range v.cells {
		v.cells[i] = make([]rune, v.cols)
	}
	v.row, v.col, v.top, v.bot = 0, 0, 0, v.rows-1
}

// Lines is the screen's text, one string per row, trailing blanks trimmed.
func (v *screen) Lines() []string {
	out := make([]string, v.rows)
	for i, r := range v.cells {
		var b strings.Builder
		for _, c := range r {
			switch c {
			case 0:
				b.WriteRune(' ')
			case -1: // the right half of a wide rune
			default:
				b.WriteRune(c)
			}
		}
		out[i] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func (v *screen) Write(p []byte) {
	b := append(v.pending, p...)
	v.pending = nil
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			n := v.escape(b[i:])
			if n == 0 { // incomplete: keep for the next write
				v.pending = append([]byte(nil), b[i:]...)
				return
			}
			i += n
		case c == '\r':
			v.col = 0
			i++
		case c == '\n', c == 0x0b, c == 0x0c:
			v.lineFeed()
			i++
		case c == '\b':
			v.col = max(v.col-1, 0)
			i++
		case c == '\t':
			v.col = min((v.col/8+1)*8, v.cols-1)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && n == 1 && !utf8.FullRune(b[i:]) {
				v.pending = append([]byte(nil), b[i:]...)
				return
			}
			v.put(r)
			i += n
		}
	}
}

func (v *screen) put(r rune) {
	w := runeWidth(r)
	if w == 0 {
		return
	}
	if v.col+w > v.cols { // autowrap
		v.col = 0
		v.lineFeed()
	}
	v.cells[v.row][v.col] = r
	if w == 2 {
		v.cells[v.row][v.col+1] = -1
	}
	v.col += w
	if v.col >= v.cols {
		v.col = v.cols // a pending wrap: the next rune goes to the next line
	}
}

func (v *screen) lineFeed() {
	if v.row == v.bot {
		copy(v.cells[v.top:v.bot], v.cells[v.top+1:v.bot+1])
		v.cells[v.bot] = make([]rune, v.cols)
		return
	}
	v.row = min(v.row+1, v.rows-1)
}

var csi = regexp.MustCompile(`^\x1b\[([0-9;?<>=]*)[ -/]*([@-~])`)

// escape handles the sequence at the start of b and returns its length (0: incomplete).
func (v *screen) escape(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	switch b[1] {
	case '[':
		m := csi.FindSubmatch(b)
		if m == nil {
			for _, c := range b[2:] { // incomplete unless a final byte shows up malformed
				if c >= 0x40 && c <= 0x7e {
					return 2
				}
			}
			return 0
		}
		v.csi(string(m[1]), m[2][0])
		return len(m[0])
	case ']', 'P', '_', '^': // OSC/DCS…: up to BEL or ST
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return 0
	case '(', ')', '*', '+', '#':
		if len(b) < 3 {
			return 0
		}
		return 3
	case '7':
		v.saved = [2]int{v.row, v.col}
	case '8':
		v.row, v.col = v.saved[0], min(v.saved[1], v.cols-1)
	case 'c':
		v.reset()
	case 'D':
		v.lineFeed()
	case 'M':
		if v.row == v.top {
			copy(v.cells[v.top+1:v.bot+1], v.cells[v.top:v.bot])
			v.cells[v.top] = make([]rune, v.cols)
		} else {
			v.row = max(v.row-1, 0)
		}
	}
	return 2
}

func (v *screen) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") {
		switch params {
		case "?1049", "?47", "?1047": // alternate screen in or out: a fresh page either way
			if final == 'h' || final == 'l' {
				v.reset()
			}
		}
		return
	}
	if strings.ContainsAny(params, "<>=") {
		return
	}
	var args []int
	for _, s := range strings.Split(params, ";") {
		n, _ := strconv.Atoi(s)
		args = append(args, n)
	}
	arg := func(i, def int) int {
		if i < len(args) && args[i] > 0 {
			return args[i]
		}
		return def
	}
	if v.col >= v.cols {
		v.col = v.cols - 1
	}
	switch final {
	case 'A':
		v.row = max(v.row-arg(0, 1), 0)
	case 'B', 'e':
		v.row = min(v.row+arg(0, 1), v.rows-1)
	case 'C', 'a':
		v.col = min(v.col+arg(0, 1), v.cols-1)
	case 'D':
		v.col = max(v.col-arg(0, 1), 0)
	case 'E':
		v.row, v.col = min(v.row+arg(0, 1), v.rows-1), 0
	case 'F':
		v.row, v.col = max(v.row-arg(0, 1), 0), 0
	case 'G', '`':
		v.col = min(arg(0, 1), v.cols) - 1
	case 'd':
		v.row = min(arg(0, 1), v.rows) - 1
	case 'H', 'f':
		v.row, v.col = min(arg(0, 1), v.rows)-1, min(arg(1, 1), v.cols)-1
	case 'K':
		from, to := v.col, v.cols
		switch arg(0, 0) {
		case 1:
			from, to = 0, v.col+1
		case 2:
			from = 0
		}
		clear(v.cells[v.row][from:min(to, v.cols)])
	case 'J':
		switch arg(0, 0) {
		case 0:
			clear(v.cells[v.row][v.col:])
			for r := v.row + 1; r < v.rows; r++ {
				clear(v.cells[r])
			}
		case 1:
			for r := 0; r < v.row; r++ {
				clear(v.cells[r])
			}
			clear(v.cells[v.row][:v.col+1])
		default:
			for r := range v.cells {
				clear(v.cells[r])
			}
		}
	case 'X':
		clear(v.cells[v.row][v.col:min(v.col+arg(0, 1), v.cols)])
	case 'r':
		v.top, v.bot = arg(0, 1)-1, min(arg(1, v.rows), v.rows)-1
		if v.top >= v.bot {
			v.top, v.bot = 0, v.rows-1
		}
		v.row, v.col = 0, 0
	case 's':
		v.saved = [2]int{v.row, v.col}
	case 'u':
		v.row, v.col = v.saved[0], v.saved[1]
	}
}

// runeWidth: columns a rune takes (0 for combining marks, 2 for wide East Asian and emoji).
func runeWidth(r rune) int {
	switch {
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f):
		return 0
	case (r >= 0x1100 && r <= 0x115f) || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd):
		return 2
	}
	return 1
}

var optionLine = regexp.MustCompile(`^\s*(?:❯\s*)?(\d)\.\s+(.*\S)\s*$`)

// dialog is a Claude Code permission dialog as shown (#105): its title ("Bash command"),
// Claude's summary under it (or the file's name), what the dashed box holds (the command, the
// file's contents), its question and its numbered options.
type dialog struct {
	Title, Summary, Body, Question string
	Options                        []string
	// Keys: what answers each option ("y", "p", Escape: Codex shows them); nil: its number.
	Keys []string
}

// readDialog reads the dialog whose question is the last line showing marker ("Do you want
// to"); false when there is none, or its options aren't numbered 1, 2, … (at least two).
func readDialog(lines []string, marker string) (dialog, bool) {
	at := -1
	for i, l := range lines {
		if strings.Contains(l, marker) {
			at = i
		}
	}
	if at < 0 {
		return dialog{}, false
	}
	width := 0 // the screen's: its widest line (the dialog's rules span it)
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l))
	}
	d := dialog{Question: strings.TrimSpace(lines[at])}
	footer := false
	labelCol, prev := 0, ""
	for _, l := range lines[at+1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Esc to cancel") {
			footer = true
			break
		}
		if m := optionLine.FindStringSubmatch(l); m != nil {
			if m[1] != strconv.Itoa(len(d.Options)+1) {
				return dialog{}, false
			}
			label, _, _ := strings.Cut(m[2], " · ") // a dim hint after it
			d.Options = append(d.Options, label)
			labelCol, prev = utf8.RuneCountInString(l[:strings.Index(l, m[2])]), l
			continue
		}
		// A wrapped label goes on, indented to the label; broken at the screen's edge, it is
		// joined without a space.
		if t != "" && len(d.Options) > 0 && labelCol > 0 && len(l)-len(strings.TrimLeft(l, " ")) >= labelCol {
			sep := " "
			if utf8.RuneCountInString(prev) >= width-1 {
				sep = ""
			}
			d.Options[len(d.Options)-1] += sep + t
			prev = l
			continue
		}
		labelCol = 0 // anything else ends the options' labels
	}
	// Only a dialog drawn to its footer: half drawn, its last options may be missing.
	if !footer || len(d.Options) < 2 {
		return dialog{}, false
	}
	// Above the question, up to the dialog's top rule: title, summary, the dashed box.
	top := 0
	for i := at - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); len(t) > 10 && strings.Trim(t, "─") == "" {
			top = i + 1
			break
		}
	}
	// The box is drawn between dashed rules, or with a left border (│) on each line; what
	// follows it ("This command requires approval") is neither head nor body.
	var head, body []string
	box := 0 // dashed rules seen
	bordered := false
	for _, l := range lines[top:at] {
		t := strings.TrimSpace(l)
		switch {
		case len(t) > 10 && strings.Trim(t, "╌") == "":
			box++
		case strings.HasPrefix(t, "│"):
			bordered = true
			body = append(body, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│")))
		case t == "" || strings.HasPrefix(t, "Tip:"):
		case box == 0 && !bordered:
			head = append(head, t)
		case box == 1:
			body = append(body, t)
		}
	}
	if len(head) > 0 {
		d.Title, d.Summary = head[0], strings.Join(head[1:], " ")
	}
	d.Body = strings.Join(body, "\n")
	return d, true
}

// codexFooter ends a Codex approval prompt; codexQuestion starts it.
const codexFooter = "Press enter to confirm or esc to cancel"

var (
	codexQuestion = regexp.MustCompile(`^(Would you like to |Allow )`)
	codexOption   = regexp.MustCompile(`^(?:›\s*)?(\d)\.\s+(.*\S)\s+\((\w+)\)$`)
)

// readCodexDialog reads Codex's approval prompt, the last one above its footer: its question
// ("Would you like to run the following command?"), its reason, the command, and its numbered
// options with the key each shows ("(y)", "(esc)"). false when there is none, or an option has
// no key.
func readCodexDialog(lines []string) (dialog, bool) {
	foot := -1
	for i, l := range lines {
		if strings.Contains(l, codexFooter) {
			foot = i
		}
	}
	if foot < 0 {
		return dialog{}, false
	}
	var d dialog
	ask := -1
	for i := foot - 1; i >= 0 && ask < 0; i-- {
		if codexQuestion.MatchString(strings.TrimSpace(lines[i])) {
			ask = i
		}
	}
	if ask < 0 {
		return dialog{}, false
	}
	d.Question = strings.TrimSpace(lines[ask])
	for _, l := range lines[ask+1 : foot] {
		t := strings.TrimSpace(l)
		if m := codexOption.FindStringSubmatch(t); m != nil {
			if m[1] != strconv.Itoa(len(d.Options)+1) {
				return dialog{}, false
			}
			key := m[3]
			if key == "esc" {
				key = "\x1b"
			}
			d.Options, d.Keys = append(d.Options, m[2]), append(d.Keys, key)
			continue
		}
		switch {
		case optionLine.MatchString(t):
			return dialog{}, false // an option without its key
		case len(d.Options) > 0:
		case strings.HasPrefix(t, "Reason: "):
			d.Summary = strings.TrimPrefix(t, "Reason: ")
		case strings.HasPrefix(t, "$ "):
			d.Body = strings.TrimPrefix(t, "$ ")
		}
	}
	if len(d.Options) < 2 {
		return dialog{}, false
	}
	return d, true
}
