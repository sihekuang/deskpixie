//go:build live

package agentapp_test

// Opt-in live check of a Codex coordinator (#87) on both session hosts:
//
//	go test -count=1 -tags live -run LiveCodexCoordinator -v -timeout 20m ./app/agentapp/
//
// agentapp.Build with agent.coordinatorKind = codex, the real herdr / tmux host on the smoke
// session, the hook server on a free loopback port, and temp app data, home, projects root and
// CODEX_HOME. Nothing of ~/.codex is used: a real OpenAI login would mean copying credentials,
// so Codex talks to mockResponses, a local Responses API that scripts each turn. What runs for
// real is Codex 0.162 itself: its launch with the app's -c hooks and their trust (no review
// screen), the hooks posting to the hook server, the PermissionRequest flow, the pixie MCP
// server, `resume --last`, and a Codex worker started through start_worker. The model's choices
// are the script's, not a GPT model's.
//
// Steps: delegate "pong" (the Stop hook's reply reaches the voice loop); delegate a task whose
// turn runs an escalated command (a PermissionRequest, confirmed by voice, then the command
// ran); restart the coordinator (it resumes with `resume --last`); with workers on, delegate a
// repo task (start_worker → a Codex worker → report_result → the coordinator's done reply).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	herdrsmoke "github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux"
	tmuxsmoke "github.com/sihekuang/deskpixie/core/tmux/smoketest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// mockResponses is a scripted Responses API for Codex (wire_api = "responses").
type mockResponses struct {
	t        *testing.T
	mu       sync.Mutex
	requests []map[string]any
	// dumped: the first request's tools were logged.
	dumped bool
}

var refRe = regexp.MustCompile(`\[ref (job_[0-9a-f]+)\]`)

func lastUserText(body map[string]any) string {
	items, _ := body["input"].([]any)
	text := ""
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] != "message" || m["role"] != "user" {
			continue
		}
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if s, ok := pm["text"].(string); ok {
				text = s
			}
		}
	}
	return text
}

func lastItem(body map[string]any) map[string]any {
	items, _ := body["input"].([]any)
	if len(items) == 0 {
		return nil
	}
	m, _ := items[len(items)-1].(map[string]any)
	return m
}

// toolNamed finds the request's tool for an MCP tool (Codex may list MCP tools flat as
// mcp__pixie__<tool> or inside a namespace): the function_call fields that call it.
func toolNamed(body map[string]any, tool string) (map[string]any, bool) {
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tl, _ := raw.(map[string]any)
		name, _ := tl["name"].(string)
		if name == "mcp__pixie__"+tool || name == "mcp__pixie__."+tool {
			return map[string]any{"name": name}, true
		}
		if sub, ok := tl["tools"].([]any); ok && strings.Contains(name, "pixie") {
			for _, s := range sub {
				sm, _ := s.(map[string]any)
				if sm["name"] == tool {
					return map[string]any{"name": tool, "namespace": name}, true
				}
			}
		}
	}
	return nil, false
}

func (m *mockResponses) call(body map[string]any, tool string, args any) map[string]any {
	fields, ok := toolNamed(body, tool)
	if !ok {
		m.t.Errorf("mock: no %s tool in the request", tool)
		return nil
	}
	b, _ := json.Marshal(args)
	item := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", time.Now().UnixNano()), "call_id": fmt.Sprintf("call_%d", time.Now().UnixNano()), "arguments": string(b)}
	for k, v := range fields {
		item[k] = v
	}
	return item
}

func message(text string) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		"content": []any{map[string]any{"type": "output_text", "text": text}}}
}

// reply is the script: one output item per request.
func (m *mockResponses) reply(body map[string]any) map[string]any {
	user := lastUserText(body)
	refs := refRe.FindAllStringSubmatch(user, -1)
	ref := ""
	if len(refs) > 0 {
		ref = refs[len(refs)-1][1]
	}
	tail := lastItem(body)
	afterTool := tail != nil && strings.HasSuffix(fmt.Sprint(tail["type"]), "_output")
	if _, hasTools := body["tools"]; !hasTools {
		return message("Smoke title") // Codex's thread-title request
	}
	switch {
	case strings.Contains(user, "You are a worker for the DeskPixie coordinator"):
		if afterTool {
			return message("Reported.")
		}
		return m.call(body, "report_result", map[string]any{"outcome": "done", "summary": "Wrote hello.txt.", "evidence": "commit abc123"})
	case strings.Contains(user, "reports done"):
		return message(fmt.Sprintf(`The worker is done. <spoken ref=%q status="done">The worker finished.</spoken>`, ref))
	case strings.Contains(user, "hello file") && !afterTool:
		return m.call(body, "start_worker", map[string]any{"kind": "codex", "repo": "demo", "job_id": ref, "name": "hello", "task": "Write hello.txt and report_result done."})
	case strings.Contains(user, "hello file"):
		return message(fmt.Sprintf(`Started. <spoken ref=%q status="working">A worker is on it.</spoken>`, ref))
	case strings.Contains(user, "escalate") && !afterTool:
		b, _ := json.Marshal(map[string]any{"cmd": "touch ../escalated", "sandbox_permissions": "require_escalated", "justification": "Create the marker"})
		return map[string]any{"type": "function_call", "id": "fc_esc", "call_id": fmt.Sprintf("call_esc_%d", time.Now().UnixNano()), "name": "exec_command", "arguments": string(b)}
	case strings.Contains(user, "escalate"):
		return message(fmt.Sprintf(`Done. <spoken ref=%q status="done">Made the marker.</spoken>`, ref))
	case strings.Contains(user, "pong"):
		return message(fmt.Sprintf(`<spoken ref=%q status="done">Pong.</spoken>`, ref))
	}
	return message("<spoken>Hello.</spoken>")
}

func (m *mockResponses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[],"data":[]}`)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	m.requests = append(m.requests, body)
	if tools, ok := body["tools"].([]any); ok && !m.dumped {
		for _, tl := range tools {
			b, _ := json.Marshal(tl)
			if strings.Contains(string(b), "pixie") {
				m.dumped = true
				m.t.Logf("mock: a pixie tool as Codex lists it: %.400s", b)
				break
			}
		}
	}
	item := m.reply(body)
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.added", "item": item, "output_index": 0},
		{"type": "response.output_item.done", "item": item, "output_index": 0},
		{"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]any{
			"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
			"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
	}
	bw := bufio.NewWriter(w)
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Fprintf(bw, "event: %s\ndata: %s\n\n", e["type"], b)
	}
	_ = bw.Flush()
}

// codexHome is a temp CODEX_HOME whose only config points Codex at the mock and trusts the
// smoke's repo (a Codex worker's folder trust is the user's to give; here the user is the smoke).
func codexHome(t *testing.T, dir, mockURL, repo string) string {
	t.Helper()
	home := filepath.Join(dir, "codex-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := fmt.Sprintf(`model = "gpt-smoke"
model_provider = "smoke"

[model_providers.smoke]
name = "smoke"
base_url = %q
wire_api = "responses"
requires_openai_auth = false

[projects.%q]
trust_level = "trusted"
`, mockURL+"/v1", repo)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=s", "-c", "user.email=s@x", "commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func waitInjected(t *testing.T, v *liveVoice, what string, within time.Duration, ok func(string) bool, a *agentapp.Agent) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, text := range v.all() {
			if ok(text) {
				return text
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("no %s within %v; injected %q; tray %+v", what, within, v.all(), a.Tray())
	return ""
}

func TestLiveCodexCoordinatorHerdr(t *testing.T) { liveCodexCoordinator(t, "herdr") }
func TestLiveCodexCoordinatorTmux(t *testing.T)  { liveCodexCoordinator(t, tmux.Name) }

func liveCodexCoordinator(t *testing.T, host string) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not found")
	}
	var tmp string
	if host == tmux.Name {
		tmuxsmoke.Smoke(t)
		tmp = tmuxsmoke.CanonicalTemp(t)
	} else {
		herdrsmoke.Smoke(t)
		tmp = herdrsmoke.CanonicalTemp(t)
	}
	// A temp home: Build's trust store and paths stay out of the user's files (the panes keep
	// the real HOME only through the environment; Codex uses CODEX_HOME).
	home := filepath.Join(tmp, "home")
	projects := filepath.Join(home, "projects")
	repo := filepath.Join(projects, "demo")
	gitRepo(t, repo)
	data := filepath.Join(tmp, "data")

	mock := &mockResponses{t: t}
	mln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mock}
	go func() { _ = srv.Serve(mln) }()
	t.Cleanup(func() { _ = srv.Close() })
	codex := codexHome(t, tmp, "http://"+mln.Addr().String(), repo)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("the smoke must never use the app's port 48217")
	}
	s := settings.Default().Agent
	s.CoordinatorKind = settings.CoordinatorCodex
	s.SessionHost = host
	s.Session = herdrsmoke.SmokeSession
	s.Port = port
	s.ProjectsRoot = projects
	s.Workers.Enabled = true
	s.Workers.Kinds = []string{"codex"}
	s.Warm = false
	s.KeepPanesOnQuit = false
	s.PermissionTimeoutSecs = 60
	environ := append(os.Environ(), "CODEX_HOME="+codex)

	var notes []string
	var notesMu sync.Mutex
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: data, Home: home, Environ: environ,
		Hooks: app.AgentHooks{Notify: func(title, body string) {
			notesMu.Lock()
			notes = append(notes, title+": "+body)
			notesMu.Unlock()
		}},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	shut := false
	t.Cleanup(func() {
		if !shut {
			a.Shutdown()
		}
	})
	if b, err := os.ReadFile(filepath.Join(agentapp.NewPaths(data).Workspace, "AGENTS.md")); err != nil || !strings.Contains(string(b), "request_user_input") {
		t.Fatalf("AGENTS.md: %v", err)
	}

	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}
	var delegate voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "delegate" {
			delegate = tool
		}
	}
	invoke := func(task string) {
		out := delegate.Invoke(context.Background(), json.RawMessage(fmt.Sprintf(`{"task":%q}`, task)))
		var res struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(out.Output, &res); err != nil || res.JobID == "" {
			t.Fatalf("delegate failed: %s", out.Output)
		}
	}

	// 1. A plain reply: the Stop hook (a curl from Codex) carries it to the voice loop.
	invoke("Reply with the word pong and nothing else.")
	got := waitInjected(t, v, "Coordinator reply: Pong.", 3*time.Minute, func(s string) bool {
		return strings.HasPrefix(s, "Coordinator reply:") && isPong(strings.TrimPrefix(s, "Coordinator reply:"))
	}, a)
	t.Logf("ok   Codex coordinator reply through its Stop hook: %q", got)

	// 2. An escalated command: Codex's PermissionRequest hook asks by voice; confirming runs it.
	invoke("Please escalate: make the marker.")
	q := waitInjected(t, v, "a permission question", 2*time.Minute, func(s string) bool { return strings.Contains(s, "Say confirm") }, a)
	t.Logf("ok   permission question: %q", q)
	word := strings.TrimSuffix(strings.TrimSpace(q[strings.LastIndex(q, "Say confirm")+len("Say confirm"):]), ".")
	v.sig <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm " + word}
	waitInjected(t, v, "the escalated job's reply", 2*time.Minute, func(s string) bool { return strings.Contains(s, "Made the marker") }, a)
	if _, err := os.Stat(filepath.Join(agentapp.NewPaths(data).Root, "escalated")); err != nil {
		t.Fatalf("the confirmed command did not run: %v", err)
	}
	t.Log("ok   PermissionRequest → voice confirm → the command ran")

	// 3. A repo task: start_worker starts a Codex worker, whose report_result comes back.
	invoke("In the demo repo, make a hello file.")
	waitInjected(t, v, "the worker's done reply", 4*time.Minute, func(s string) bool { return strings.Contains(s, "The worker finished") }, a)
	t.Log("ok   delegation end to end: start_worker → Codex worker → report_result → done")

	// 4. Restart: the coordinator comes back with `codex resume --last` in its workspace, so
	// its next request carries the earlier conversation.
	mock.mu.Lock()
	before := len(mock.requests)
	mock.mu.Unlock()
	a.Menu("agent:restart")
	invoke("Reply with the word pong and nothing else.")
	time.Sleep(2 * time.Second)
	pongs := 0
	deadline := time.Now().Add(3 * time.Minute)
	for pongs < 2 && time.Now().Before(deadline) {
		pongs = 0
		for _, s := range v.all() {
			if strings.HasPrefix(s, "Coordinator reply:") && isPong(strings.TrimPrefix(s, "Coordinator reply:")) {
				pongs++
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if pongs < 2 {
		t.Fatalf("no reply after the restart; injected %q; tray %+v", v.all(), a.Tray())
	}
	resumed := false
	mock.mu.Lock()
	for _, r := range mock.requests[before:] {
		b, _ := json.Marshal(r["input"])
		if _, tools := r["tools"]; tools && strings.Contains(string(b), "make the marker") {
			resumed = true
		}
	}
	mock.mu.Unlock()
	if !resumed {
		t.Fatal("the restarted coordinator did not resume its conversation")
	}
	t.Log("ok   after a restart the coordinator resumed its conversation (resume --last)")

	a.Shutdown()
	shut = true
	notesMu.Lock()
	t.Logf("notifications: %q", notes)
	notesMu.Unlock()
	mock.mu.Lock()
	t.Logf("mock served %d requests", len(mock.requests))
	mock.mu.Unlock()
	_ = agent.Coordinator
}
