package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Ported from ai-mascot crates/agent-http/src/hooks.rs (18 tests), plus Go-only extras at the
// end. Rust's paused tokio clock becomes a short limit on the test server.

// fakeRuntime is the runtime end of the hook channel (Rust: the mpsc receiver).
type fakeRuntime struct {
	ch     chan agent.HookEvent
	closed bool
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{ch: make(chan agent.HookEvent, 16)} }

func (f *fakeRuntime) Deliver(ev agent.HookEvent) bool {
	if f.closed {
		return false
	}
	f.ch <- ev
	return true
}

func (f *fakeRuntime) tryRecv() (agent.HookEvent, bool) {
	select {
	case ev := <-f.ch:
		return ev, true
	default:
		return agent.HookEvent{}, false
	}
}

func (f *fakeRuntime) recv(t *testing.T) agent.HookEvent {
	t.Helper()
	select {
	case ev := <-f.ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no hook event")
		return agent.HookEvent{}
	}
}

type hookRig struct {
	app   *Server
	token string
	rt    *fakeRuntime
}

func rig() hookRig {
	tokens := NewTokens()
	token := tokens.Issue("coordinator")
	rt := newFakeRuntime()
	return hookRig{app: NewServer(tokens, rt), token: token, rt: rt}
}

func postReq(path string, bearer *string, body io.Reader) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", "application/json")
	if bearer != nil {
		r.Header.Set("Authorization", "Bearer "+*bearer)
	}
	return r
}

func strp(s string) *string { return &s }

func send(h http.Handler, r *http.Request) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

type result struct {
	status int
	text   string
}

func sendAsync(h http.Handler, r *http.Request) <-chan result {
	done := make(chan result, 1)
	go func() {
		s, t := send(h, r)
		done <- result{s, t}
	}()
	return done
}

func await(t *testing.T, c <-chan result) result {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("request never answered")
		return result{}
	}
}

func mustJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("not JSON: %q: %v", text, err)
	}
	return v
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonEqual compares two values as JSON documents.
func jsonEqual(a, b any) bool {
	var x, y any
	_ = json.Unmarshal([]byte(toJSON(a)), &x)
	_ = json.Unmarshal([]byte(toJSON(b)), &y)
	return reflect.DeepEqual(x, y)
}

func behavior(t *testing.T, text string) any {
	t.Helper()
	v := mustJSON(t, text)
	out, _ := v["hookSpecificOutput"].(map[string]any)
	d, _ := out["decision"].(map[string]any)
	return d["behavior"]
}

func isDeny(t *testing.T, text string) bool {
	t.Helper()
	return behavior(t, text) == "deny"
}

// noReadBody fails the test when the handler reads it.
type noReadBody struct{ t *testing.T }

func (b noReadBody) Read([]byte) (int, error) {
	b.t.Error("the body was read")
	return 0, io.EOF
}

func TestNoOrWrongBearerIs401(t *testing.T) {
	r := rig()
	if s, _ := send(r.app, postReq("/hooks/stop", nil, strings.NewReader("{}"))); s != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", s)
	}
	if s, _ := send(r.app, postReq("/hooks/stop", strp("x"), strings.NewReader("{}"))); s != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: %d", s)
	}
}

func TestTheBearerIsCheckedBeforeTheBodyIsRead(t *testing.T) {
	r := rig()
	huge := strings.Repeat("x", MaxBody*4)
	if s, _ := send(r.app, postReq("/hooks/stop", nil, strings.NewReader(huge))); s != http.StatusUnauthorized {
		t.Fatalf("status %d", s)
	}
	// Go extra: prove the body is never touched.
	if s, _ := send(r.app, postReq("/hooks/stop", nil, noReadBody{t})); s != http.StatusUnauthorized {
		t.Fatalf("status %d", s)
	}
}

func TestBodiesOverTheLimitAre413(t *testing.T) {
	r := rig()
	huge := `{"last_assistant_message":"` + strings.Repeat("x", MaxBody) + `"}`
	if s, _ := send(r.app, postReq("/hooks/stop", &r.token, strings.NewReader(huge))); s != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", s)
	}
}

func TestStopAnswersEmptyJSONAndReportsTheLastMessage(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"hook_event_name": "Stop", "stop_hook_active": false, "last_assistant_message": "Done.\u0007\n<spoken>Hi.</spoken>"})
	status, text := send(r.app, postReq("/hooks/stop", &r.token, strings.NewReader(body)))
	if status != http.StatusOK || text != "{}" {
		t.Fatalf("(%d, %q)", status, text)
	}
	ev, ok := r.rt.tryRecv()
	if !ok || ev.Kind != agent.HookStop {
		t.Fatalf("%+v", ev)
	}
	if ev.Agent != "coordinator" {
		t.Fatalf("agent %q", ev.Agent)
	}
	if ev.Message != "Done.\n<spoken>Hi.</spoken>" {
		t.Fatalf("control chars stripped: %q", ev.Message)
	}
}

func TestALongStopMessageKeepsItsTrailingSpokenBlock(t *testing.T) {
	r := rig()
	tail := `<spoken ref="job_x">Done.</spoken>`
	message := strings.Repeat("é", 50*1024) + tail
	if len(message) <= 100*1024 {
		t.Fatal("fixture too short")
	}
	send(r.app, postReq("/hooks/stop", &r.token, strings.NewReader(toJSON(map[string]any{"last_assistant_message": message}))))
	ev, ok := r.rt.tryRecv()
	if !ok || ev.Kind != agent.HookStop {
		t.Fatalf("%+v", ev)
	}
	if !strings.HasSuffix(ev.Message, tail) {
		t.Fatal("tail lost")
	}
	if len(ev.Message) > maxStopText {
		t.Fatalf("len %d", len(ev.Message))
	}
}

func TestPostToolAnswersEmptyJSONAndReportsTheTool(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x/a.rs"}, "tool_response": map[string]any{}})
	status, text := send(r.app, postReq("/hooks/post-tool", &r.token, strings.NewReader(body)))
	if status != http.StatusOK || text != "{}" {
		t.Fatalf("(%d, %q)", status, text)
	}
	ev, ok := r.rt.tryRecv()
	if !ok || ev.Kind != agent.HookPostTool || ev.ToolName != "Edit" {
		t.Fatalf("%+v", ev)
	}
}

func TestAMalformedPermissionRequestIsA200Deny(t *testing.T) {
	r := rig()
	status, text := send(r.app, postReq("/hooks/permission", &r.token, strings.NewReader("not json")))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if !isDeny(t, text) {
		t.Fatal(text)
	}
	if mustJSON(t, text)["hookSpecificOutput"].(map[string]any)["hookEventName"] != "PermissionRequest" {
		t.Fatal(text)
	}
}

func TestAPermissionWaitsForTheDecision(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "git push"}})
	pending := sendAsync(r.app, postReq("/hooks/permission", &r.token, strings.NewReader(body)))
	ev := r.rt.recv(t)
	if ev.Kind != agent.HookPermission || ev.Agent != "coordinator" || ev.ToolName != "Bash" {
		t.Fatalf("%+v", ev)
	}
	if ev.ToolInput.(map[string]any)["command"] != "git push" {
		t.Fatalf("%+v", ev.ToolInput)
	}
	ev.Reply <- agent.Allow()
	res := await(t, pending)
	if res.status != http.StatusOK {
		t.Fatalf("status %d", res.status)
	}
	if !jsonEqual(mustJSON(t, res.text), DecisionBody(agent.Allow())) {
		t.Fatal(res.text)
	}
}

func TestAPermissionNobodyAnswersIsDenied(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "x"}})
	pending := sendAsync(r.app, postReq("/hooks/permission", &r.token, strings.NewReader(body)))
	ev := r.rt.recv(t)
	close(ev.Reply) // Rust: drop(reply)
	res := await(t, pending)
	if behavior(t, res.text) != "deny" {
		t.Fatal(res.text)
	}
}

func TestAnOversizedPermissionBodyIsA200Deny(t *testing.T) {
	r := rig()
	huge := `{"tool_name":"Bash","tool_input":"` + strings.Repeat("x", MaxBody) + `"}`
	status, text := send(r.app, postReq("/hooks/permission", &r.token, strings.NewReader(huge)))
	if status != http.StatusOK || !isDeny(t, text) {
		t.Fatalf("(%d, %q)", status, text)
	}
}

func TestABadOrStaleBearerOnPermissionIsA200Deny(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{}})
	status, text := send(r.app, postReq("/hooks/permission", strp("stale"), strings.NewReader(body)))
	if status != http.StatusOK || !isDeny(t, text) {
		t.Fatalf("(%d, %q)", status, text)
	}
	status, text = send(r.app, postReq("/hooks/permission", nil, strings.NewReader(body)))
	if status != http.StatusOK || !isDeny(t, text) {
		t.Fatalf("(%d, %q)", status, text)
	}
}

func TestAPermissionThatOutlivesTheLimitIsDeniedWithTheReplyAlive(t *testing.T) {
	r := rig()
	r.app.permissionLimit = 50 * time.Millisecond
	body := toJSON(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "x"}})
	pending := sendAsync(r.app, postReq("/hooks/permission", &r.token, strings.NewReader(body)))
	ev := r.rt.recv(t)
	res := await(t, pending)
	if res.status != http.StatusOK || !isDeny(t, res.text) {
		t.Fatalf("%+v", res)
	}
	// The reply is still alive: a late answer must not block or panic.
	ev.Reply <- agent.Allow()
}

func TestARequestWithAnOriginHeaderIsRefused(t *testing.T) {
	r := rig()
	withOrigin := func(path, body string) *http.Request {
		req := postReq(path, &r.token, strings.NewReader(body))
		req.Header.Set("Origin", "https://evil.example")
		return req
	}
	if s, _ := send(r.app, withOrigin("/hooks/stop", "{}")); s != http.StatusForbidden {
		t.Fatalf("stop %d", s)
	}
	if s, _ := send(r.app, withOrigin("/hooks/post-tool", "{}")); s != http.StatusForbidden {
		t.Fatalf("post-tool %d", s)
	}
	status, text := send(r.app, withOrigin("/hooks/permission", "{}"))
	if status != http.StatusOK || !isDeny(t, text) {
		t.Fatalf("(%d, %q)", status, text)
	}
}

// addrListener is a listener that only reports an address. Rust binds 0.0.0.0:0; a fake keeps
// the test off the network (and away from the macOS firewall prompt).
type addrListener struct{ addr net.Addr }

func (l addrListener) Accept() (net.Conn, error) { return nil, errors.New("closed") }
func (l addrListener) Close() error              { return nil }
func (l addrListener) Addr() net.Addr            { return l.addr }

func TestServeRefusesANonLoopbackListener(t *testing.T) {
	ln := addrListener{&net.TCPAddr{IP: net.IPv4zero, Port: 4999}}
	err := Serve(t.Context(), ln, NewTokens(), newFakeRuntime())
	if !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("err = %v", err)
	}
}

func TestInvisibleFormatCharactersAreDropped(t *testing.T) {
	if got := clean("a\u200Bb\u202Ec\u2066d\uFEFFe\u0007f"); got != "abcdef" {
		t.Fatalf("clean = %q", got)
	}
	if got := cleanTail("x\u200Fy"); got != "xy" {
		t.Fatalf("cleanTail = %q", got)
	}
}

func TestALargePostToolInputIsDroppedToNull(t *testing.T) {
	r := rig()
	body := toJSON(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"content": strings.Repeat("x", 20*1024)}})
	send(r.app, postReq("/hooks/post-tool", &r.token, strings.NewReader(body)))
	ev, ok := r.rt.tryRecv()
	if !ok || ev.Kind != agent.HookPostTool || ev.ToolInput != nil {
		t.Fatalf("%+v", ev.Kind)
	}
}

func TestUnknownHooksAre404(t *testing.T) {
	r := rig()
	if s, _ := send(r.app, postReq("/hooks/nope", &r.token, strings.NewReader("{}"))); s != http.StatusNotFound {
		t.Fatalf("status %d", s)
	}
}

func TestDecisionBodiesMatchClaudeCodesSchema(t *testing.T) {
	allow := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`
	if !jsonEqual(DecisionBody(agent.Allow()), json.RawMessage(allow)) {
		t.Fatal(toJSON(DecisionBody(agent.Allow())))
	}
	deny := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"No answer from the user."}}}`
	if !jsonEqual(DecisionBody(agent.Deny("No answer from the user.")), json.RawMessage(deny)) {
		t.Fatal(toJSON(DecisionBody(agent.Deny("No answer from the user."))))
	}
}

// --- Go-only extras ---

// errBody fails mid-read (not the size limit): Rust answers 400 (a deny on permission).
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestABodyReadErrorIs400OrADeny(t *testing.T) {
	r := rig()
	if s, _ := send(r.app, postReq("/hooks/stop", &r.token, errBody{})); s != http.StatusBadRequest {
		t.Fatalf("stop %d", s)
	}
	status, text := send(r.app, postReq("/hooks/permission", &r.token, errBody{}))
	if status != http.StatusOK || !isDeny(t, text) {
		t.Fatalf("(%d, %q)", status, text)
	}
}

func TestAPermissionWhileTheRuntimeIsGoneIsDenied(t *testing.T) {
	r := rig()
	r.rt.closed = true
	body := toJSON(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{}})
	status, text := send(r.app, postReq("/hooks/permission", &r.token, strings.NewReader(body)))
	if status != http.StatusOK || !isDeny(t, text) || !strings.Contains(text, "shutting down") {
		t.Fatalf("(%d, %q)", status, text)
	}
}

func TestHooksOnlyAcceptPost(t *testing.T) {
	r := rig()
	req := httptest.NewRequest(http.MethodGet, "/hooks/stop", nil)
	req.Header.Set("Authorization", "Bearer "+r.token)
	w := httptest.NewRecorder()
	r.app.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Allow"))
	}
	if s, _ := send(r.app, postReq("/elsewhere", &r.token, strings.NewReader("{}"))); s != http.StatusNotFound {
		t.Fatalf("elsewhere %d", s)
	}
}

func TestServeAnswersOnLoopback(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Skip("no loopback:", err)
	}
	tokens := NewTokens()
	tok := tokens.Issue("coordinator")
	done := make(chan error, 1)
	go func() { done <- Serve(t.Context(), ln, tokens, newFakeRuntime()) }()
	req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/hooks/stop", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(b) != "{}" || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q %q", res.StatusCode, b, res.Header.Get("Content-Type"))
	}
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its listener closed")
	}
}

// The state route (tmux session host, #64): Claude Code's UserPromptSubmit, Stop and
// Notification hooks say working, idle and blocked; an explicit status wins.
func TestTheStateRouteReportsTheCallersStatus(t *testing.T) {
	r := rig()
	for _, tc := range []struct {
		body string
		want agent.PaneStatus
	}{
		{`{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, agent.PaneWorking},
		{`{"hook_event_name":"Stop","last_assistant_message":"done"}`, agent.PaneIdle},
		{`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, agent.PaneBlocked},
		{`{"status":"working"}`, agent.PaneWorking},
		{`{"status":"idle","hook_event_name":"UserPromptSubmit"}`, agent.PaneIdle},
		{`{"status":"blocked"}`, agent.PaneBlocked},
	} {
		code, text := send(r.app, postReq("/hooks/state", &r.token, strings.NewReader(tc.body)))
		if code != http.StatusOK || text != "{}" {
			t.Fatalf("%s: %d %q", tc.body, code, text)
		}
		ev := r.rt.recv(t)
		if ev.Kind != agent.HookState || ev.Agent != "coordinator" || ev.Status != tc.want {
			t.Fatalf("%s: %+v", tc.body, ev)
		}
	}
}

func TestTheStateRouteRefusesStrangersAndUnknownStates(t *testing.T) {
	r := rig()
	if code, _ := send(r.app, postReq("/hooks/state", nil, strings.NewReader(`{"status":"idle"}`))); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code, _ := send(r.app, postReq("/hooks/state", strp("nope"), strings.NewReader(`{"status":"idle"}`))); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	for _, body := range []string{`{"status":"done"}`, `{"status":"sleeping"}`, `{"hook_event_name":"PreToolUse"}`, `{}`, `[]`, `nope`} {
		if code, _ := send(r.app, postReq("/hooks/state", &r.token, strings.NewReader(body))); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if ev, ok := r.rt.tryRecv(); ok {
		t.Fatalf("nothing delivered: %+v", ev)
	}
	req := postReq("/hooks/state", &r.token, strings.NewReader(`{"status":"idle"}`))
	req.Header.Set("Origin", "http://evil.example")
	if code, _ := send(r.app, req); code != http.StatusForbidden {
		t.Fatal(code)
	}
}

func TestHookBodiesCarryThePermissionMode(t *testing.T) {
	r := rig()
	for _, c := range []struct{ path, body string }{
		{"/hooks/stop", `{"last_assistant_message":"done","permission_mode":"default"}`},
		{"/hooks/post-tool", `{"tool_name":"Bash","tool_input":{},"permission_mode":"auto"}`},
		{"/hooks/stop", `{"last_assistant_message":"done"}`},
		{"/hooks/state", `{"hook_event_name":"UserPromptSubmit","permission_mode":"default"}`},
	} {
		if status, _ := send(r.app, postReq(c.path, &r.token, strings.NewReader(c.body))); status != http.StatusOK {
			t.Fatalf("%s: %d", c.path, status)
		}
	}
	for _, want := range []string{"default", "auto", "", "default"} {
		if got := r.rt.recv(t).PermissionMode; got != want {
			t.Fatalf("mode %q, want %q", got, want)
		}
	}
}

func TestTheModeRouteDeliversAModeEvent(t *testing.T) {
	r := rig()
	status, _ := send(r.app, postReq("/hooks/mode", &r.token, strings.NewReader(`{"permission_mode":"default"}`)))
	ev := r.rt.recv(t)
	if status != http.StatusOK || ev.Kind != agent.HookMode || ev.Agent != "coordinator" || ev.PermissionMode != "default" {
		t.Fatalf("%d %+v", status, ev)
	}
	if status, _ := send(r.app, postReq("/hooks/mode", nil, strings.NewReader(`{}`))); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", status)
	}
}
