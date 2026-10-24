package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sihekuang/deskpixie/core/richtext"
	"github.com/sihekuang/deskpixie/core/settings"
)

func spansOf(t *testing.T, out json.RawMessage) []richtext.Span {
	t.Helper()
	var got struct {
		Spans []richtext.Span `json:"spans"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err, string(out))
	}
	return got.Spans
}

func kindsOf(spans []richtext.Span) map[string]richtext.Span {
	m := map[string]richtext.Span{}
	for _, s := range spans {
		if s.Kind != richtext.KindText {
			m[s.Kind] = s
		}
	}
	return m
}

// #277: a session's reply is split into spans; its paths resolve against the session's folder
// and open in the editor it runs in.
func TestTextSpansResolveAgainstTheSessionsFolder(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "main.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeAgent{places: map[string][2]string{"api-claude": {folder, "com.microsoft.VSCode"}}}
	h := newHarness(t, withAgent(f))
	text := "see main.go:3 and https://x.com"
	spans := spansOf(t, h.call("text.spans", map[string]any{"text": text, "sessions": []string{"nobody", "api-claude"}}))
	k := kindsOf(spans)
	p := k[richtext.KindPath]
	if p.Path != filepath.Join(folder, "main.go") || p.Line != 3 || len(p.Open) != 2 ||
		p.Open[0] != "vscode://file"+filepath.Join(folder, "main.go")+":3" {
		t.Fatalf("%+v", p)
	}
	if k[richtext.KindLink].URL != "https://x.com" {
		t.Fatalf("%+v", spans)
	}

	// The chosen editor is read on every call.
	h.set("agent.editor", richtext.EditorZed)
	p = kindsOf(spansOf(t, h.call("text.spans", map[string]any{"text": text, "sessions": []string{"api-claude"}})))[richtext.KindPath]
	if len(p.Open) != 2 || p.Open[0] != "zed://file"+filepath.Join(folder, "main.go")+":3" {
		t.Fatalf("%+v", p)
	}
}

func TestTextSpansWithoutASessionOnlyKnowAbsolutePaths(t *testing.T) {
	folder := t.TempDir()
	file := filepath.Join(folder, "a.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	k := kindsOf(spansOf(t, h.call("text.spans", map[string]any{"text": "a.txt and " + file})))
	if p := k[richtext.KindPath]; p.Path != file || p.Text != file || len(p.Open) != 1 {
		t.Fatalf("%+v", k)
	}
}

func TestTextSpansNeedText(t *testing.T) {
	h := newHarness(t)
	if out := h.call("text.spans", map[string]any{"text": ""}); string(out) != `{"spans":[]}` {
		t.Fatal(string(out))
	}
	if _, err := h.callErr("text.spans", "not an object"); err == nil {
		t.Fatal("bad args are an error")
	}
}
