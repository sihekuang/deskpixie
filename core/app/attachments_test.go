package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/voice"
)

// #240: attachments' calls and event, and Pixie's take_clipboard tool.

func (f *fakeAgent) Attachments() *attach.Store {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attach
}

func attachHarness(t *testing.T) (*harness, *fakeAgent) {
	t.Helper()
	f := &fakeAgent{tray: AgentTray{Enabled: true}, attach: attach.New(filepath.Join(t.TempDir(), "attachments"), filepath.Join(t.TempDir(), "copies.jsonl"), time.Now)}
	return newHarness(t, withAgent(f)), f
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func itemCount(e event) int {
	items, _ := e["items"].([]any)
	return len(items)
}

func TestAddListRemove(t *testing.T) {
	h, _ := attachHarness(t)
	out := h.call("attachments.add", map[string]string{"name": "a.png", "data": b64([]byte("png"))})
	var got struct{ Item attach.Item }
	if err := json.Unmarshal(out, &got); err != nil || got.Item.Kind != "image" || got.Item.Name != "a.png" {
		t.Fatal(string(out))
	}
	h.waitFor("attachments", func(e event) bool { return itemCount(e) == 1 }, time.Second)
	if out := h.call("attachments.list", nil); !strings.Contains(string(out), `"name":"a.png"`) {
		t.Fatal(string(out))
	}
	h.call("attachments.remove", map[string]string{"id": got.Item.ID})
	h.waitFor("attachments", func(e event) bool { return itemCount(e) == 0 }, time.Second)
	if _, err := h.callErr("attachments.add", map[string]string{"path": filepath.Join(t.TempDir(), "missing")}); err == nil || err.Error() != "Couldn't read that file." {
		t.Fatal(err)
	}
}

func TestAddRefusesTooBig(t *testing.T) {
	h, _ := attachHarness(t)
	big := make([]byte, attach.MaxBytes+1)
	if _, err := h.callErr("attachments.add", map[string]string{"name": "big.png", "data": b64(big)}); err == nil || err.Error() != "That file is over 2 MB." {
		t.Fatal(err)
	}
}

func TestAttachmentsWithoutAnAgent(t *testing.T) {
	h := newHarness(t)
	if _, err := h.callErr("attachments.add", map[string]string{"name": "a.png", "data": b64([]byte("x"))}); err == nil || err.Error() != "The coordinator is off." {
		t.Fatal(err)
	}
}

// runTakeClipboard runs the tool and returns its reply once the shell has answered via reply.
func runTakeClipboard(t *testing.T, h *harness, reply func(request string)) map[string]any {
	t.Helper()
	tool := h.app.clipboardTool()
	done := make(chan map[string]any, 1)
	go func() {
		out := tool.Invoke(context.Background(), json.RawMessage(`{}`))
		var m map[string]any
		_ = json.Unmarshal(out.Output, &m)
		done <- m
	}()
	e := h.waitFor("clipboard.read", nil, time.Second)
	if reply != nil {
		reply(e["request"].(string))
	}
	select {
	case m := <-done:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
		return nil
	}
}

func TestTakeClipboard(t *testing.T) {
	h, _ := attachHarness(t)
	m := runTakeClipboard(t, h, func(req string) {
		h.call("attachments.add", map[string]string{"name": "a.png", "data": b64([]byte("png"))})
		h.call("clipboard.done", map[string]string{"request": req})
	})
	if got, _ := json.Marshal(m); string(got) != `{"attached":["a.png"]}` {
		t.Fatal(string(got))
	}
}

func TestTakeClipboardWithNothingAttachable(t *testing.T) {
	h, _ := attachHarness(t)
	m := runTakeClipboard(t, h, func(req string) {
		h.call("clipboard.done", map[string]string{"request": req, "error": "There's no image or file on the clipboard."})
	})
	if m["error"] != "There's no image or file on the clipboard." {
		t.Fatal(m)
	}
	// Done with no error but nothing added: said the same way.
	m = runTakeClipboard(t, h, func(req string) { h.call("clipboard.done", map[string]string{"request": req}) })
	if m["error"] != "There's no image or file on the clipboard." {
		t.Fatal(m)
	}
}

func TestTakeClipboardTimesOut(t *testing.T) {
	old := clipboardWait
	clipboardWait = 50 * time.Millisecond
	t.Cleanup(func() { clipboardWait = old })
	h, _ := attachHarness(t)
	if m := runTakeClipboard(t, h, nil); m["error"] != "Couldn't read the clipboard." {
		t.Fatal(m)
	}
}

func TestTakeClipboardIsAVoiceToolOnlyWithTheAgent(t *testing.T) {
	h, _ := attachHarness(t)
	tools, _ := h.app.voiceExtras()
	if !hasTool(tools, "take_clipboard") {
		t.Fatal("missing")
	}
	plain := newHarness(t)
	if tools, _ := plain.app.voiceExtras(); hasTool(tools, "take_clipboard") {
		t.Fatal("without the agent")
	}
}

func hasTool(tools []voice.Tool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
}
