// Command fakeclaude stands in for Claude Code under a real pix in the agent's end-to-end
// tests: it draws Claude's input box, calls `pix hook` the way Claude Code's hooks do, and
// shows Claude's interrupt line after an Esc while it works. No network, no API key.
//
// It takes steps from the FIFO $FAKE_DIR/ctl, one per line, and writes "ok <step>" to
// $FAKE_DIR/done after each:
//
//	work     you typed a prompt of your own: UserPromptSubmit (working)
//	tool     one of its own tools finished: PostToolUse; what pix answered goes to $FAKE_DIR/context
//	subtool  a tool of one of its subagents finished: PostToolUse with agent_id; pix's answer too
//	stop     its turn ended: Stop (idle)
//	ask      it asks you a menu (AskUserQuestion): its PreToolUse, then Claude's menu, which a
//	         digit answers: an option, "Type something." (then your words and Enter), or "Chat
//	         about this"
//	subperm  a subagent's Bash call needs your permission: its PermissionRequest (with agent_id)
//	         runs as its dialog shows, which a digit answers
//	redraw   it draws its whole screen again, as after a resize
//	split    it draws its menu again in two writes, the second 1.5 s after the first: a
//	         redraw cut between reads, drawn out so pix looks at its first half
//	shrink   it draws its menu as a smaller pane shows it: the question scrolled off the top,
//	         its options and footer still there
//
// What was answered is a line of $FAKE_DIR/answers ("menu <label>", "subperm <how>").
//
// What pix types is appended to $FAKE_DIR/typed as it comes; each prompt pix pastes and sends
// is a line of $FAKE_DIR/prompts.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

var (
	dir     = os.Getenv("FAKE_DIR")
	mu      sync.Mutex
	working bool
	shown   string // the menu or dialog on screen, waiting for a digit ("": none)
	shownBy string // what it asks: "menu" or "subperm"
	// own: the menu's "Type something." is open, with the words typed into it so far.
	own *strings.Builder
)

// The menu ask asks, and the subagent's call subperm asks about.
var (
	menuInput = map[string]any{"questions": []any{map[string]any{"question": "Push main?", "header": "Push",
		"multiSelect": false, "options": []any{map[string]any{"label": "Push"}, map[string]any{"label": "Don't push"}}}}}
	menuScreen = "\x1b[2J\x1b[H☐ Push\r\n\r\nPush main?\r\n\r\n❯ 1. Push\r\n  2. Don't push\r\n  3. Type something.\r\n" +
		"────────\r\n  4. Chat about this\r\n\r\nEnter to select · ↑/↓ to navigate · Esc to cancel\r\n"
	lsInput    = map[string]any{"command": "ls -la", "description": "List files"}
	permScreen = "\x1b[2J\x1b[H────────────────────────────────────────\r\n Bash command\r\n List files\r\n" +
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n ls -la\r\n╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n Do you want to proceed?\r\n" +
		" ❯ 1. Yes\r\n   2. Yes, and don't ask again for: ls -la\r\n   3. No\r\n\r\n Esc to cancel\r\n"
)

func main() {
	if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), old)
	}
	hook("SessionStart", map[string]any{"source": "startup"})
	box()
	go keys()
	ctl, err := os.OpenFile(filepath.Join(dir, "ctl"), os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeclaude:", err)
		os.Exit(1)
	}
	s := bufio.NewScanner(ctl)
	for s.Scan() {
		step := strings.TrimSpace(s.Text())
		switch step {
		case "work":
			setWorking(true)
			hook("UserPromptSubmit", map[string]any{"prompt": "keep fixing the upload test"})
			fmt.Print("⏺ Working on it…\r\n")
		case "tool":
			out := hook("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
			appendTo("context", out+"\n")
		case "subtool":
			out := hook("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
				"agent_id": "a0d84cff17c3490ba", "agent_type": "general-purpose"})
			appendTo("context", out+"\n")
		case "stop":
			setWorking(false)
			hook("Stop", map[string]any{"last_assistant_message": "Done for now."})
			box()
		case "ask":
			go ask()
		case "subperm":
			go subperm()
		case "split":
			at := strings.Index(menuScreen, "❯")
			fmt.Print(menuScreen[:at])
			go func() {
				time.Sleep(1500 * time.Millisecond)
				fmt.Print(menuScreen[at:])
			}()
		case "shrink":
			fmt.Print("\x1b[2J\x1b[H" + menuScreen[strings.Index(menuScreen, "❯"):])
		case "redraw":
			mu.Lock()
			screen := shown
			mu.Unlock()
			if screen == "" {
				fmt.Print("\x1b[2J\x1b[H")
				box()
			} else {
				fmt.Print(screen)
			}
		}
		appendTo("done", "ok "+step+"\n")
	}
}

// ask is AskUserQuestion: its PreToolUse, then Claude's menu, which waits for a key.
func ask() {
	hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": menuInput})
	show("menu", menuScreen)
	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude Code needs your attention"})
}

// subperm is a subagent's Bash call asking for permission: its dialog shows while its hook
// runs, and its Notification follows (Claude Code 2.1.310). A key answers it.
func subperm() {
	go hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": lsInput,
		"agent_id": "a0d84cff17c3490ba", "agent_type": "general-purpose"})
	time.Sleep(200 * time.Millisecond) // the hook has started
	show("subperm", permScreen)
	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
}

// ownScreen is the menu with "Type something." open, holding words.
func ownScreen(words string) string {
	if words == "" {
		words = "Type something."
	}
	return strings.Replace(strings.Replace(menuScreen, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. "+words, 1)
}

// show draws a menu or dialog that waits for a key.
func show(by, screen string) {
	mu.Lock()
	shown, shownBy = screen, by
	mu.Unlock()
	fmt.Print(screen)
}

// answered: what was asked is answered; its tool runs and ends with its PostToolUse.
func answered(by, how string) {
	appendTo("answers", by+" "+how+"\n")
	fmt.Print("\x1b[2J\x1b[H")
	box()
	switch by {
	case "menu":
		hook("PostToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": menuInput})
	case "subperm":
		hook("PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": lsInput,
			"agent_id": "a0d84cff17c3490ba", "agent_type": "general-purpose"})
	}
}

// keys reads what pix types: a pasted prompt sent with Enter is submitted, an Esc of its own
// stops a working turn the way Claude does (its interrupt line, and no Stop hook).
func keys() {
	var paste bytes.Buffer
	pasting, pasted := false, ""
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		b := buf[:n]
		appendTo("typed", string(b))
		mu.Lock()
		by := shownBy
		mu.Unlock()
		mu.Lock()
		typing := own
		mu.Unlock()
		if typing != nil { // your words in "Type something.", until Enter sends them
			words, enter := strings.CutSuffix(string(b), "\r")
			typing.WriteString(words)
			if enter {
				mu.Lock()
				shown, shownBy, own = "", "", nil
				mu.Unlock()
				go answered("menu", "own "+typing.String())
			} else {
				show("menu", ownScreen(typing.String()))
			}
			continue
		}
		if by != "" && len(b) == 1 && b[0] >= '1' && b[0] <= '9' { // a digit picks on the menu or dialog
			switch {
			case by == "menu" && b[0] == '3':
				mu.Lock()
				own = &strings.Builder{}
				mu.Unlock()
				show("menu", ownScreen(""))
				continue
			case by == "menu" && b[0] == '4':
				mu.Lock()
				shown, shownBy = "", ""
				mu.Unlock()
				go answered("menu", "chat")
				continue
			}
			mu.Lock()
			shown, shownBy = "", ""
			mu.Unlock()
			go answered(by, "key "+string(b))
			continue
		}
		if string(b) == "\x1b" {
			if isWorking() {
				setWorking(false)
				fmt.Print("\x1b[1m⎿  Interrupted · What should Claude do instead?\x1b[0m\r\n")
				box()
			}
			continue
		}
		for len(b) > 0 {
			switch {
			case !pasting && bytes.HasPrefix(b, []byte("\x1b[200~")):
				pasting, b = true, b[len("\x1b[200~"):]
				paste.Reset()
			case pasting:
				if i := bytes.Index(b, []byte("\x1b[201~")); i >= 0 {
					paste.Write(b[:i])
					pasting, pasted, b = false, paste.String(), b[i+len("\x1b[201~"):]
					fmt.Print("[pasted]\r\n") // Claude redraws its box once it takes a paste
					continue
				}
				paste.Write(b)
				b = nil
			case b[0] == '\r':
				if pasted != "" {
					submit(pasted)
					pasted = ""
				}
				b = b[1:]
			default:
				b = b[1:]
			}
		}
	}
}

// submit: the prompt pix typed is sent, and starts a turn.
func submit(prompt string) {
	appendTo("prompts", strings.ReplaceAll(prompt, "\n", "\\n")+"\n")
	setWorking(true)
	hook("UserPromptSubmit", map[string]any{"prompt": prompt})
	fmt.Print("⏺ On it.\r\n")
}

// box draws Claude's input box, empty.
func box() { fmt.Print("────────\r\n❯ \r\n────────\r\n") }

// hook runs `pix hook event` with payload as Claude Code would, and returns what it printed.
func hook(event string, payload map[string]any) string {
	payload["session_id"] = "fake-1"
	b, _ := json.Marshal(payload)
	cmd := exec.Command("pix", "hook", event)
	cmd.Stdin = bytes.NewReader(b)
	out, _ := cmd.Output()
	return string(out)
}

func appendTo(name, text string) {
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(text)
}

func setWorking(on bool) {
	mu.Lock()
	working = on
	mu.Unlock()
}

func isWorking() bool {
	mu.Lock()
	defer mu.Unlock()
	return working
}
