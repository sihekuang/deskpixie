package tmux

// A small terminal agent for the real-tmux tests: the test binary runs it (TestMain) when tmux
// starts `claude` or `codex` from a wrapper script the tests put first on the server's PATH.
// It behaves like Claude Code / Codex as far as the host can tell:
//   - raw terminal, bracketed paste on; it redraws its whole screen on every change;
//   - idle: its input box ("› Ask Codex to do anything") and "? for shortcuts" under it;
//   - Enter (CR, or LF outside a paste) submits; inside a paste line breaks are text;
//   - a turn shows "• Working (esc to interrupt)" for a second, then "OK: received N line(s)";
//     a prompt containing "slowly" keeps working until Escape, which prints "■ Interrupted";
//   - "/exit" ends it; `--exit-at-start` makes it exit at once (a failed start);
//   - $FAKE_AGENT_REPORT, if set, gets one JSON line at start ({args, cwd, env}) and one per
//     submitted prompt ({prompt}).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const fakeAgentEnv = "DESKPIXIE_FAKE_AGENT"

func TestMain(m *testing.M) {
	if os.Getenv(fakeAgentEnv) != "" {
		os.Exit(fakeAgentMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeAgent struct {
	kind       string
	transcript []string
	input      []rune
	working    bool
	slow       bool
	until      time.Time
	report     *os.File
}

func (a *fakeAgent) log(v any) {
	if a.report == nil {
		return
	}
	b, _ := json.Marshal(v)
	_, _ = a.report.Write(append(b, '\n'))
}

// clip keeps a row inside an 80-column pane.
func clip(s string) string {
	if utf8.RuneCountInString(s) > 78 {
		return string([]rune(s)[:78])
	}
	return s
}

func (a *fakeAgent) draw() {
	rows := []string{"fake " + a.kind + " agent"}
	t := a.transcript[max(0, len(a.transcript)-14):]
	rows = append(rows, t...)
	rows = append(rows, "")
	if a.working {
		rows = append(rows, "• Working (esc to interrupt)")
	}
	box := strings.ReplaceAll(string(a.input), "\n", "↵")
	if box == "" {
		box = "Ask Codex to do anything"
	}
	rows = append(rows, "› "+box, "  ? for shortcuts")
	for i := range rows {
		rows[i] = clip(rows[i])
	}
	fmt.Print("\x1b[2J\x1b[H" + strings.Join(rows, "\r\n"))
}

func (a *fakeAgent) say(lines ...string) { a.transcript = append(a.transcript, lines...) }

func (a *fakeAgent) submit() (exit bool) {
	text := string(a.input)
	a.input = nil
	if strings.TrimSpace(text) == "/exit" {
		return true
	}
	a.log(map[string]any{"prompt": text})
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if i == 0 {
			a.say("› " + l)
		} else {
			a.say("  " + l)
		}
	}
	a.working, a.slow = true, strings.Contains(text, "slowly")
	a.until = time.Now().Add(time.Second)
	if a.slow {
		a.until = time.Now().Add(2 * time.Minute)
	}
	a.say(fmt.Sprintf("(turn: %d line(s))", len(lines)))
	return false
}

func fakeAgentMain(args []string) int {
	a := &fakeAgent{kind: os.Getenv(fakeAgentEnv)}
	if p := os.Getenv("FAKE_AGENT_REPORT"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			a.report = f
			defer f.Close()
		}
	}
	cwd, _ := os.Getwd()
	a.log(map[string]any{"args": args, "cwd": cwd, "env": os.Getenv("FAKE_AGENT_VALUE")})
	if slices.Contains(args, "--exit-at-start") {
		fmt.Print("fake agent: nothing to continue\r\n")
		return 1
	}
	raw := exec.Command("stty", "raw", "-echo")
	raw.Stdin = os.Stdin
	if err := raw.Run(); err != nil {
		fmt.Println("stty:", err)
		return 2
	}
	// Bracketed paste on; the agent's own title, which the host must not let replace its label.
	fmt.Print("\x1b[?2004h\x1b]2;fake agent title\x07")
	a.draw()

	in := make(chan byte, 4096)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			for _, b := range buf[:n] {
				in <- b
			}
			if err != nil {
				close(in)
				return
			}
		}
	}()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	paste := false
	// next is the next input byte, waiting at most d (ok false: none came, or stdin ended).
	next := func(d time.Duration) (byte, bool) {
		select {
		case b, ok := <-in:
			return b, ok
		case <-time.After(d):
			return 0, false
		}
	}
	for {
		select {
		case <-tick.C:
			if a.working && time.Now().After(a.until) {
				a.working = false
				a.say("OK")
				a.draw()
			}
			continue
		case b, ok := <-in:
			if !ok {
				return 0
			}
			switch {
			case b == 0x1b:
				c, more := next(30 * time.Millisecond)
				if !more || c != '[' {
					// A lone Escape: interrupts a turn.
					if a.working {
						a.working = false
						a.say("■ Interrupted")
					}
					if more && c >= 0x20 && c < 0x7f {
						a.input = append(a.input, rune(c)) // the key after it
					}
					break
				}
				var seq []byte
				for {
					c, more := next(100 * time.Millisecond)
					if !more {
						break
					}
					seq = append(seq, c)
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				switch string(seq) {
				case "200~":
					paste = true
				case "201~":
					paste = false
				}
			case b == '\r' || b == '\n':
				if paste {
					a.input = append(a.input, '\n')
				} else if a.submit() {
					fmt.Print("\x1b[?2004l")
					return 0
				}
			case b == 0x7f:
				if len(a.input) > 0 {
					a.input = a.input[:len(a.input)-1]
				}
			case b >= 0x20:
				// Collect a UTF-8 sequence.
				p := []byte{b}
				for !utf8.FullRune(p) {
					c, more := next(100 * time.Millisecond)
					if !more {
						break
					}
					p = append(p, c)
				}
				r, _ := utf8.DecodeRune(p)
				a.input = append(a.input, r)
			}
			a.draw()
		}
	}
}
