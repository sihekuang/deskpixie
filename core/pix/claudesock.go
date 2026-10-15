package pix

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeRecord is ~/.claude/sessions/<pid>.json. It is undocumented (see agent-deck's
// internal/send/claudesocket.go); re-check the fields when Claude Code updates.
type ClaudeRecord struct {
	Pid          int    `json:"pid"`
	SessionID    string `json:"sessionId"`
	ProcStart    string `json:"procStart"`
	PeerProtocol int    `json:"peerProtocol"`
	SocketPath   string `json:"messagingSocketPath"`
	Name         string `json:"name"`
	keyFile      string
}

// FindClaudeRecord reads the session record Claude keeps for process pid.
func FindClaudeRecord(home string, pid int) (ClaudeRecord, error) {
	dir := filepath.Join(home, ".claude", "sessions")
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return ClaudeRecord{}, err
	}
	var r ClaudeRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return ClaudeRecord{}, err
	}
	if keys, _ := filepath.Glob(filepath.Join(dir, strconv.Itoa(pid)+".*.key")); len(keys) == 1 {
		r.keyFile = keys[0]
	}
	return r, nil
}

// ProcStart is the process's start time as Claude records it.
func ProcStart(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PostClaude posts text into the session's inbox: Claude queues it for its next turn, or
// starts one when idle. It refuses unless the record is this exact process and protocol 1.
func PostClaude(r ClaudeRecord, text string) error {
	if r.PeerProtocol != 1 {
		return fmt.Errorf("claude peerProtocol %d not supported", r.PeerProtocol)
	}
	start, err := ProcStart(r.Pid)
	if err != nil || start != strings.TrimSpace(r.ProcStart) {
		return fmt.Errorf("claude session record is not process %d", r.Pid)
	}
	c, err := net.DialTimeout("unix", r.SocketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if r.keyFile != "" {
		if tok, err := os.ReadFile(r.keyFile); err == nil {
			line, _ := json.Marshal(map[string]any{"type": "auth", "token": strings.TrimSpace(string(tok))})
			c.Write(append(line, '\n'))
		}
	}
	line, _ := json.Marshal(map[string]any{
		"msgV": 1, "msg_id": uuid(), "type": "user", "session_id": r.SessionID,
		"message": map[string]any{"role": "user", "content": text}, "priority": "next",
	})
	if _, err := c.Write(append(line, '\n')); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond) // the server never replies; give it time to read
	return nil
}

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
