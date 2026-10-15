package pix

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeLocal struct {
	mu       sync.Mutex
	events   []string
	payloads []map[string]any
	desc     string
	reply    string
}

func (f *fakeLocal) Hook(ev string, p map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	f.payloads = append(f.payloads, p)
	return f.reply
}

func (f *fakeLocal) SetDescription(s string) error { f.mu.Lock(); f.desc = s; f.mu.Unlock(); return nil }

func serveFake(t *testing.T, f *fakeLocal) string {
	sock := filepath.Join(t.TempDir(), "l.sock")
	stop, err := ServeLocal(sock, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return sock
}

func TestHookForwardsAndReplies(t *testing.T) {
	f := &fakeLocal{reply: `{"hookSpecificOutput":{"additionalContext":"hi"}}`}
	sock := serveFake(t, f)
	var out bytes.Buffer
	code := RunHook("PostToolUse", sock, "", strings.NewReader(`{"session_id":"s1"}`), &out)
	if code != 0 || len(f.events) != 1 || f.events[0] != "PostToolUse" || f.payloads[0]["session_id"] != "s1" ||
		!strings.Contains(out.String(), "additionalContext") {
		t.Fatalf("code %d events %v out %q", code, f.events, out.String())
	}
}

func TestHookPayloadFromArgument(t *testing.T) {
	// Codex's notify passes the JSON as an argument and gives no stdin.
	f := &fakeLocal{}
	sock := serveFake(t, f)
	var out bytes.Buffer
	RunHook("Stop", sock, `{"type":"agent-turn-complete"}`, strings.NewReader("must not be read"), &out)
	if len(f.payloads) != 1 || f.payloads[0]["type"] != "agent-turn-complete" {
		t.Fatalf("%v", f.payloads)
	}
}

func TestHookWithoutParentIsSilent(t *testing.T) {
	var out bytes.Buffer
	if code := RunHook("Stop", "/nonexistent.sock", "", strings.NewReader("{}"), &out); code != 0 || out.Len() != 0 {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestMCPSetDescription(t *testing.T) {
	f := &fakeLocal{}
	sock := serveFake(t, f)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"set_description","arguments":{"text":"fixing login loop"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := RunMCP(sock, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 replies (none to a notification): %q", out.String())
	}
	if !strings.Contains(lines[1], `"set_description"`) {
		t.Fatalf("tools/list: %s", lines[1])
	}
	var call struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[2]), &call)
	if call.Result.IsError || f.desc != "fixing login loop" {
		t.Fatalf("desc %q reply %s", f.desc, lines[2])
	}
}

func TestMCPWithoutParentReportsError(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_description","arguments":{"text":"x"}}}` + "\n"
	var out bytes.Buffer
	RunMCP("/nonexistent.sock", strings.NewReader(in), &out)
	if !strings.Contains(out.String(), `"isError":true`) {
		t.Fatalf("%s", out.String())
	}
}
