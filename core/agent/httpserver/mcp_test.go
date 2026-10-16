package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Ported from ai-mascot crates/agent-http/src/mcp.rs (12 tests). Rust's paused tokio clock
// becomes a short limit on the test server.

type mcpRig struct {
	app         *Server
	coordinator string
	worker      string
	rt          *fakeRuntime
}

func mrig() mcpRig {
	tokens := NewTokens()
	coordinator := tokens.Issue("coordinator")
	worker := tokens.Issue("focus-fix")
	rt := newFakeRuntime()
	return mcpRig{app: NewServer(tokens, rt), coordinator: coordinator, worker: worker, rt: rt}
}

func mreq(method string, bearer *string, body string) *http.Request {
	r := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != nil {
		r.Header.Set("Authorization", "Bearer "+*bearer)
	}
	return r
}

func rpc(t *testing.T, app http.Handler, bearer string, body any) map[string]any {
	t.Helper()
	status, text := send(app, mreq(http.MethodPost, &bearer, toJSON(body)))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, text)
	}
	return mustJSON(t, text)
}

func rpcAsync(t *testing.T, app http.Handler, bearer string, body any) <-chan map[string]any {
	t.Helper()
	c := sendAsync(app, mreq(http.MethodPost, &bearer, toJSON(body)))
	out := make(chan map[string]any, 1)
	go func() {
		r := <-c
		var v map[string]any
		_ = json.Unmarshal([]byte(r.text), &v)
		out <- v
	}()
	return out
}

func awaitRPC(t *testing.T, c <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("rpc never answered")
		return nil
	}
}

func toolCall(name string, args any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
}

// get walks a decoded JSON value by object keys and array indexes.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func TestTheBearerIsCheckedFirstAndBodiesAreCappedAt16KB(t *testing.T) {
	r := mrig()
	if s, _ := send(r.app, mreq("POST", nil, "{}")); s != http.StatusUnauthorized {
		t.Fatalf("no bearer %d", s)
	}
	if s, _ := send(r.app, mreq("POST", strp("stale"), strings.Repeat("x", MaxMcpBody*4))); s != http.StatusUnauthorized {
		t.Fatalf("stale %d", s)
	}
	big := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping", "params": map[string]any{"pad": strings.Repeat("x", MaxMcpBody)}}
	if s, _ := send(r.app, mreq("POST", &r.coordinator, toJSON(big))); s != http.StatusRequestEntityTooLarge {
		t.Fatalf("big %d", s)
	}
}

func TestBrowsersAndStreamsAreRefused(t *testing.T) {
	r := mrig()
	withOrigin := mreq("POST", &r.coordinator, "{}")
	withOrigin.Header.Set("Origin", "https://evil.example")
	if s, _ := send(r.app, withOrigin); s != http.StatusForbidden {
		t.Fatalf("origin %d", s)
	}
	for _, m := range []string{"GET", "DELETE"} {
		w := httptest.NewRecorder()
		r.app.ServeHTTP(w, mreq(m, &r.coordinator, ""))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: %d %q", m, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestInitializeEchoesAKnownVersionAndOffersTools(t *testing.T) {
	r := mrig()
	v := rpc(t, r.app, r.coordinator, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "claude-code", "version": "2"}}})
	if v["id"] != 1.0 {
		t.Fatalf("id %v", v["id"])
	}
	if get(v, "result", "protocolVersion") != "2025-03-26" {
		t.Fatal(toJSON(v))
	}
	if get(v, "result", "capabilities", "tools", "listChanged") != false {
		t.Fatal(toJSON(v))
	}
	// Rename (spec §6): Rust says "ai-mascot".
	if get(v, "result", "serverInfo", "name") != "deskpixie" {
		t.Fatal(toJSON(v))
	}
	v = rpc(t, r.app, r.coordinator, map[string]any{"jsonrpc": "2.0", "id": "a", "method": "initialize", "params": map[string]any{"protocolVersion": "1999-01-01"}})
	if get(v, "result", "protocolVersion") != "2025-06-18" || v["id"] != "a" {
		t.Fatal(toJSON(v))
	}
}

func TestNotificationsAndClientResponsesAreAcceptedWithoutABody(t *testing.T) {
	r := mrig()
	status, text := send(r.app, mreq("POST", &r.worker, toJSON(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})))
	if status != http.StatusAccepted || text != "" {
		t.Fatalf("(%d, %q)", status, text)
	}
	status, _ = send(r.app, mreq("POST", &r.worker, toJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{}})))
	if status != http.StatusAccepted {
		t.Fatalf("status %d", status)
	}
}

func TestProtocolErrorsUseJSONRPCCodes(t *testing.T) {
	r := mrig()
	_, text := send(r.app, mreq("POST", &r.worker, "not json"))
	if get(mustJSON(t, text), "error", "code") != -32700.0 {
		t.Fatal(text)
	}
	_, text = send(r.app, mreq("POST", &r.worker, "[]"))
	if get(mustJSON(t, text), "error", "code") != -32600.0 {
		t.Fatal(text)
	}
	v := rpc(t, r.app, r.worker, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "resources/list"})
	if get(v, "error", "code") != -32601.0 {
		t.Fatal(toJSON(v))
	}
	v = rpc(t, r.app, r.worker, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if !reflect.DeepEqual(v["result"], map[string]any{}) {
		t.Fatal(toJSON(v))
	}
}

func TestToolsListDependsOnTheIdentity(t *testing.T) {
	r := mrig()
	names := func(v map[string]any) []string {
		var out []string
		tools, _ := get(v, "result", "tools").([]any)
		for _, tl := range tools {
			out = append(out, get(tl, "name").(string))
		}
		return out
	}
	list := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}
	if got := names(rpc(t, r.app, r.coordinator, list)); !reflect.DeepEqual(got, append([]string{"start_worker", "create_project", "message_worker", "stop_worker", "list_workers"}, agent.SessionTools[:]...)) {
		t.Fatal(got)
	}
	if got := names(rpc(t, r.app, r.worker, list)); !reflect.DeepEqual(got, []string{"report_progress", "report_result", "ask_coordinator", "report_blocked"}) {
		t.Fatal(got)
	}
}

func TestAWorkerTokenCannotCallStartWorkerAndTheCoordinatorCannotReport(t *testing.T) {
	r := mrig()
	start := toolCall("start_worker", map[string]any{"kind": "claude", "repo": "focus", "task": "x", "job_id": "job_0123456789ab"})
	v := rpc(t, r.app, r.worker, start)
	if get(v, "error", "code") != -32602.0 {
		t.Fatal(toJSON(v))
	}
	v = rpc(t, r.app, r.coordinator, toolCall("report_result", map[string]any{"outcome": "done", "summary": "s", "evidence": "e"}))
	if get(v, "error", "code") != -32602.0 {
		t.Fatal(toJSON(v))
	}
	if _, ok := r.rt.tryRecv(); ok {
		t.Fatal("nothing reached the app")
	}
}

func TestBadArgumentsAreAToolErrorTheAgentSees(t *testing.T) {
	r := mrig()
	v := rpc(t, r.app, r.worker, toolCall("report_result", map[string]any{"outcome": "maybe", "summary": "s"}))
	if get(v, "result", "isError") != true {
		t.Fatal(toJSON(v))
	}
	if get(v, "result", "content", 0, "text") != "outcome must be done or failed" {
		t.Fatal(toJSON(v))
	}
	if _, ok := r.rt.tryRecv(); ok {
		t.Fatal("nothing reached the app")
	}
}

func TestAReportReachesTheAppCleanedWithTheCallersIdentity(t *testing.T) {
	r := mrig()
	body := toolCall("report_result", map[string]any{"outcome": "done", "summary": "Fixed <spoken>it</spoken>\u0007 \"now\"", "evidence": "cargo test"})
	pending := rpcAsync(t, r.app, r.worker, body)
	ev := r.rt.recv(t)
	if ev.Kind != agent.HookMcp || ev.Agent != "focus-fix" {
		t.Fatalf("%+v", ev)
	}
	want := agent.McpCall{Kind: agent.CallReportResult, Outcome: agent.OutcomeDone, Summary: "Fixed it 'now'", Evidence: "cargo test"}
	if !reflect.DeepEqual(ev.Call, want) {
		t.Fatalf("%+v", ev.Call)
	}
	ev.McpReply <- agent.McpReply{Value: map[string]any{"ok": true}}
	v := awaitRPC(t, pending)
	if get(v, "result", "isError") != false {
		t.Fatal(toJSON(v))
	}
	text, _ := get(v, "result", "content", 0, "text").(string)
	if !jsonEqual(json.RawMessage(text), map[string]any{"ok": true}) {
		t.Fatal(text)
	}
}

func TestAnAppErrorIsAToolError(t *testing.T) {
	r := mrig()
	pending := rpcAsync(t, r.app, r.coordinator, toolCall("stop_worker", map[string]any{"name": "nobody"}))
	ev := r.rt.recv(t)
	ev.McpReply <- agent.McpReply{Err: errors.New("No running worker nobody.")}
	v := awaitRPC(t, pending)
	want := map[string]any{"content": []any{map[string]any{"type": "text", "text": "No running worker nobody."}}, "isError": true}
	if !reflect.DeepEqual(v["result"], want) {
		t.Fatal(toJSON(v))
	}
}

func TestACallTheAppNeverAnswersTimesOutAsAToolError(t *testing.T) {
	r := mrig()
	r.app.mcpCallLimit = 50 * time.Millisecond
	pending := rpcAsync(t, r.app, r.coordinator, toolCall("list_workers", map[string]any{}))
	ev := r.rt.recv(t)
	v := awaitRPC(t, pending)
	if get(v, "result", "content", 0, "text") != "The app did not answer." {
		t.Fatal(toJSON(v))
	}
	ev.McpReply <- agent.McpReply{} // late answer: must not block
}

func TestHooksStillWorkBesideMcp(t *testing.T) {
	r := mrig()
	stop := httptest.NewRequest(http.MethodPost, "/hooks/stop", strings.NewReader("{}"))
	stop.Header.Set("Authorization", "Bearer "+r.coordinator)
	if s, _ := send(r.app, stop); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
}

// --- Go-only extras ---

func TestADroppedMcpReplyAndAGoneRuntimeAreToolErrors(t *testing.T) {
	r := mrig()
	pending := rpcAsync(t, r.app, r.coordinator, toolCall("list_workers", map[string]any{}))
	ev := r.rt.recv(t)
	close(ev.McpReply) // Rust: drop(reply)
	v := awaitRPC(t, pending)
	if get(v, "result", "content", 0, "text") != "The app did not answer." || get(v, "result", "isError") != true {
		t.Fatal(toJSON(v))
	}
	r.rt.closed = true
	v = rpc(t, r.app, r.coordinator, toolCall("list_workers", map[string]any{}))
	if get(v, "result", "content", 0, "text") != "The app is shutting down." {
		t.Fatal(toJSON(v))
	}
}

func TestToolsCallWithoutANameOrWithAnUnknownToolIsInvalidParams(t *testing.T) {
	r := mrig()
	v := rpc(t, r.app, r.worker, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{}})
	if get(v, "error", "code") != -32602.0 || get(v, "error", "message") != "Missing tool name" {
		t.Fatal(toJSON(v))
	}
	v = rpc(t, r.app, r.worker, toolCall("nope", map[string]any{}))
	if get(v, "error", "code") != -32602.0 || get(v, "error", "message") != "Unknown tool: nope" {
		t.Fatal(toJSON(v))
	}
}

func TestLargeIntegerIDsAreEchoedExactly(t *testing.T) {
	r := mrig()
	status, text := send(r.app, mreq("POST", &r.worker, `{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}`))
	if status != http.StatusOK || !strings.Contains(text, `"id":9007199254740993`) {
		t.Fatalf("(%d, %q)", status, text)
	}
}
