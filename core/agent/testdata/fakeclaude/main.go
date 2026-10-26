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

	"golang.org/x/term"
)

var (
	dir     = os.Getenv("FAKE_DIR")
	mu      sync.Mutex
	working bool
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
		}
		appendTo("done", "ok "+step+"\n")
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
