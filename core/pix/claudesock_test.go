package pix

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestFindClaudeRecordAndPost(t *testing.T) {
	home := t.TempDir()
	sess := filepath.Join(home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	sock := filepath.Join(t.TempDir(), "c.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	pid := os.Getpid()
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": "s-1", "procStart": start, "peerProtocol": 1, "messagingSocketPath": sock})
	os.WriteFile(filepath.Join(sess, strconv.Itoa(pid)+".json"), b, 0o600)

	got := make(chan map[string]any, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		var m map[string]any
		json.Unmarshal(line, &m)
		got <- m
	}()
	r, err := FindClaudeRecord(home, pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := PostClaude(r, "hello"); err != nil {
		t.Fatal(err)
	}
	m := <-got
	msg, _ := m["message"].(map[string]any)
	if m["type"] != "user" || m["session_id"] != "s-1" || m["priority"] != "next" || msg["content"] != "hello" {
		t.Fatalf("%v", m)
	}
}

func TestPostClaudeRefusesWrongProcess(t *testing.T) {
	r := ClaudeRecord{Pid: os.Getpid(), ProcStart: "Thu Jan  1 00:00:00 1970", PeerProtocol: 1, SocketPath: "/nonexistent"}
	if err := PostClaude(r, "x"); err == nil {
		t.Fatal("procStart mismatch must refuse")
	}
	r2 := ClaudeRecord{Pid: os.Getpid(), PeerProtocol: 2}
	r2.ProcStart, _ = ProcStart(os.Getpid())
	if err := PostClaude(r2, "x"); err == nil {
		t.Fatal("unknown peerProtocol must refuse")
	}
}

func TestFindClaudeRecordMissing(t *testing.T) {
	if _, err := FindClaudeRecord(t.TempDir(), 1); err == nil {
		t.Fatal("want error")
	}
}
