package agent_test

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// userFake is a secondary host whose agents are sessions you started.
type userFake struct {
	*agenttest.FakeSessionHost
	described map[string]string
	labeled   map[string]bool // sessions with verbatim turned off
	desc      string          // "" = "login bug"
	past      string          // "" = "auth"
	mu        sync.Mutex      // lastTurn and asks: the running agent lists sessions while a test sets them
	lastTurn  string
	asks      bool
	// unreachable: pix no longer reaches the sessions; keysErr: pix refuses keys with it.
	unreachable bool
	keysErr     error
}

func (u *userFake) SendKeys(ctx context.Context, n string, keys []string) error {
	u.mu.Lock()
	err := u.keysErr
	u.mu.Unlock()
	if err != nil {
		return err
	}
	return u.FakeSessionHost.SendKeys(ctx, n, keys)
}

func (u *userFake) setTurn(text string, asks bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.lastTurn, u.asks = text, asks
}

func (u *userFake) Owns(n string) bool { _, ok := u.StatusOf(n); return ok }
func (u *userFake) List(ctx context.Context) ([]agent.AgentInfo, error) {
	l, err := u.FakeSessionHost.List(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := range l {
		l[i].Origin = agent.OriginUser
		l[i].Cwd = "/x/focus"
		desc, past := cmp.Or(u.desc, "login bug"), cmp.Or(u.past, "auth")
		l[i].User = &agent.UserSession{Tool: "claude", Repo: "focus", Where: "WebStorm", Description: desc,
			PastDescriptions: []string{past}, Reachable: !u.unreachable, Verbatim: !u.labeled[l[i].Name],
			LastTurn: u.lastTurn, LastTurnAsks: u.asks,
			Started: time.Now().Add(-90 * time.Minute), LastActive: time.Now().Add(-3 * time.Minute)}
	}
	return l, err
}
func (u *userFake) SetDescription(n, t string) error    { u.described[n] = t; return nil }
func (u *userFake) Verbatim(n string) (bool, error)     { return !u.labeled[n], nil }
func (u *userFake) SetVerbatim(n string, on bool) error { u.labeled[n] = !on; return nil }

func newSessionHarness(t *testing.T) (*agent.CompositeHost, *userFake, *agenttest.FakeSessionHost) {
	t.Helper()
	ctx := context.Background()
	primary := agenttest.NewFakeSessionHost()
	user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}, labeled: map[string]bool{}}
	user.FakeSessionHost.StartAgent(ctx, agent.AgentSpec{Name: "focus-claude"})
	primary.StartAgent(ctx, agent.AgentSpec{Name: "focus-fix"})
	return agent.NewCompositeHost(primary, user), user, primary
}

func TestListSessionsShowsOnlyYours(t *testing.T) {
	c, _, _ := newSessionHarness(t)
	r := agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallListSessions})
	rows, _ := r.Value.(map[string]any)["sessions"].([]agent.SessionRow)
	if r.Err != nil || len(rows) != 1 || rows[0].Name != "focus-claude" || rows[0].Description != "login bug" ||
		rows[0].Where != "WebStorm" || rows[0].PastDescriptions[0] != "auth" || rows[0].Status != "idle" ||
		rows[0].Cwd != "/x/focus" || rows[0].Started != "1h30m ago" || rows[0].LastActive != "3m ago" {
		t.Fatalf("%+v %v", rows, r.Err)
	}
}

func TestMessageAndInterruptSession(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	ctx := context.Background()
	if r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallInterruptSession, Name: "focus-claude"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if len(user.Prompts()) != 1 || len(user.Keys()) != 1 || user.Keys()[0].Keys[0] != "Escape" {
		t.Fatalf("prompts %v keys %v", user.Prompts(), user.Keys())
	}
	if len(user.Closed()) != 0 {
		t.Fatal("interrupting must not close")
	}
}

// TestMessageSessionIsVerbatimByDefault (#143): a session is sent the text exactly as given,
// with no prefix, unless its verbatim setting is off.
func TestMessageSessionIsVerbatimByDefault(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	if r := agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "go ahead"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := user.Prompts(); len(got) != 1 || got[0].Agent != "focus-claude" || got[0].Text != "go ahead" {
		t.Fatalf("prompts: %v", got)
	}
}

// TestSetSessionVerbatim (#143): with verbatim off, a session's messages are prefixed
// "Coordinator says: "; back on, they're sent as given again. list_sessions shows the setting.
func TestSetSessionVerbatim(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	run := func(call agent.McpCall) agent.McpReply {
		t.Helper()
		r := agent.SessionToolForTest(context.Background(), c, call)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		return r
	}
	listed := func() bool {
		t.Helper()
		rows, _ := run(agent.McpCall{Kind: agent.CallListSessions}).Value.(map[string]any)["sessions"].([]agent.SessionRow)
		return rows[0].Verbatim
	}
	if !listed() {
		t.Fatal("list_sessions must show verbatim on by default")
	}
	r := run(agent.McpCall{Kind: agent.CallSetSessionVerbatim, Name: "focus-claude", Verbatim: false})
	if v, ok := r.Value.(map[string]any)["verbatim"].(bool); !ok || v {
		t.Fatalf("reply must give the new setting: %v", r.Value)
	}
	if listed() {
		t.Fatal("list_sessions must show verbatim off")
	}
	run(agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"})
	run(agent.McpCall{Kind: agent.CallSetSessionVerbatim, Name: "focus-claude", Verbatim: true})
	run(agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "go ahead"})
	if got := user.Prompts(); len(got) != 2 || got[0].Text != "Coordinator says: hi" || got[1].Text != "go ahead" {
		t.Fatalf("prompts: %v", got)
	}
}

func TestCloseSession(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	if r := agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallCloseSession, Name: "focus-claude"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if len(user.Closed()) != 1 {
		t.Fatal(user.Closed())
	}
}

func TestSessionToolsRefuseWorkers(t *testing.T) {
	c, _, primary := newSessionHarness(t)
	for _, k := range []agent.McpCallKind{agent.CallMessageSession, agent.CallInterruptSession, agent.CallCloseSession, agent.CallDescribeSession, agent.CallSetSessionVerbatim} {
		r := agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: k, Name: "focus-fix", Text: "x"})
		if r.Err == nil || !strings.Contains(r.Err.Error(), "worker") {
			t.Fatalf("%v: %v", k, r.Err)
		}
	}
	if len(primary.Keys()) != 0 || len(primary.Prompts()) != 0 || len(primary.Closed()) != 0 {
		t.Fatal("a worker must not be touched by session tools")
	}
}

func TestSessionToolsUnknownName(t *testing.T) {
	c, _, _ := newSessionHarness(t)
	r := agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallInterruptSession, Name: "nobody"})
	if r.Err == nil || r.Err.Error() != "No session nobody." {
		t.Fatalf("%v", r.Err)
	}
}

func TestDescribeSession(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallDescribeSession, Name: "focus-claude", Text: "uploads"})
	if user.described["focus-claude"] != "uploads" {
		t.Fatal(user.described)
	}
}

func TestSessionToolsWithoutPix(t *testing.T) {
	r := agent.SessionToolForTest(context.Background(), agenttest.NewFakeSessionHost(), agent.McpCall{Kind: agent.CallListSessions})
	if rows, _ := r.Value.(map[string]any)["sessions"].([]agent.SessionRow); r.Err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("an empty list when pix isn't running: %+v %v", r.Value, r.Err)
	}
}

func TestParseSessionTools(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want agent.McpCall
	}{
		{"list_sessions", map[string]any{}, agent.McpCall{Kind: agent.CallListSessions}},
		{"message_session", map[string]any{"name": "focus-claude", "text": "hi"}, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"}},
		{"set_session_verbatim", map[string]any{"name": "focus-claude", "verbatim": true}, agent.McpCall{Kind: agent.CallSetSessionVerbatim, Name: "focus-claude", Verbatim: true}},
		{"interrupt_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallInterruptSession, Name: "focus-claude"}},
		{"close_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallCloseSession, Name: "focus-claude"}},
		{"describe_session", map[string]any{"name": "focus-claude", "text": " login\nbug "}, agent.McpCall{Kind: agent.CallDescribeSession, Name: "focus-claude", Text: "login bug"}},
		{"describe_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallDescribeSession, Name: "focus-claude"}},
		{"flush_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallFlushSession, Name: "focus-claude"}},
		{"message_session", map[string]any{"name": "focus-claude", "text": "hi", "force": true}, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi", Force: true}},
	}
	for _, c := range cases {
		got, err := agent.ParseToolCall(agent.RoleCoordinator, c.name, c.args)
		if err != nil || got.Kind != c.want.Kind || got.Name != c.want.Name || got.Text != c.want.Text || got.Verbatim != c.want.Verbatim || got.Force != c.want.Force {
			t.Errorf("%s: %+v %v", c.name, got, err)
		}
	}
	if _, err := agent.ParseToolCall(agent.RoleWorker, "list_sessions", map[string]any{}); err == nil {
		t.Fatal("workers can't use session tools")
	}
	if _, err := agent.ParseToolCall(agent.RoleCoordinator, "message_session", map[string]any{"name": "x"}); err == nil {
		t.Fatal("message_session needs text")
	}
}

// TestParseSetSessionVerbatim (#143, #137): verbatim is required, as a boolean or the strings
// "true"/"false"; anything else is refused. message_session ignores a verbatim argument now
// that the session's setting decides.
func TestParseSetSessionVerbatim(t *testing.T) {
	parse := func(tool string, args map[string]any) (agent.McpCall, error) {
		return agent.ParseToolCall(agent.RoleCoordinator, tool, args)
	}
	for _, c := range []struct {
		v    any
		want bool
	}{{true, true}, {"true", true}, {" TRUE ", true}, {false, false}, {"false", false}} {
		got, err := parse("set_session_verbatim", map[string]any{"name": "focus-claude", "verbatim": c.v})
		if err != nil || got.Kind != agent.CallSetSessionVerbatim || got.Name != "focus-claude" || got.Verbatim != c.want {
			t.Errorf("verbatim %#v: %+v %v", c.v, got, err)
		}
	}
	for _, args := range []map[string]any{{"name": "x"}, {"name": "x", "verbatim": "yes"}, {"name": "x", "verbatim": 1}, {"verbatim": true}} {
		if _, err := parse("set_session_verbatim", args); err == nil {
			t.Errorf("%v must be refused", args)
		}
	}
	if got, err := parse("message_session", map[string]any{"name": "x", "text": "hi", "verbatim": "yes"}); err != nil || got.Verbatim {
		t.Errorf("message_session must ignore a verbatim argument: %+v %v", got, err)
	}
	if _, err := agent.ParseToolCall(agent.RoleWorker, "set_session_verbatim", map[string]any{"name": "x", "verbatim": true}); err == nil {
		t.Error("workers can't use session tools")
	}
}

// TestParseOverrideExistingSessionString (#137): start_worker reads its flag as a string too.
func TestParseOverrideExistingSessionString(t *testing.T) {
	args := map[string]any{"kind": "claude", "repo": "focus", "task": "fix it", "job_id": "job_0123456789ab", "override_existing_session": "true"}
	got, err := agent.ParseToolCall(agent.RoleCoordinator, "start_worker", args)
	if err != nil || !got.Worker.OverrideExistingSession {
		t.Fatalf("got %+v, %v", got.Worker, err)
	}
	args["override_existing_session"] = "maybe"
	if _, err := agent.ParseToolCall(agent.RoleCoordinator, "start_worker", args); err == nil {
		t.Fatal(`override_existing_session "maybe" must be refused`)
	}
}

// #146: list_sessions shows each session's last turn, a question as asked.
func TestListSessionsShowsTheLastTurn(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	user.setTurn("All 12 tests pass.", false)
	if rows := sessionRows(t, agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallListSessions})); rows[0].LastTurn != "All 12 tests pass." {
		t.Fatalf("%+v", rows)
	}
	user.setTurn("Should I open the PR?", true)
	if rows := sessionRows(t, agent.SessionToolForTest(context.Background(), c, agent.McpCall{Kind: agent.CallListSessions})); rows[0].LastTurn != "Asked: Should I open the PR?" {
		t.Fatalf("%+v", rows)
	}
}

// #146: what a session wrote reaches the coordinator with pending confirm words masked, so it
// can never read one back as the user's confirmation.
func TestListSessionsMasksWhatTheSessionWrote(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	user.setTurn("Say confirm hawk to merge.", false)
	mask := func(s string) string { return strings.ReplaceAll(s, "hawk", "(word removed)") }
	rows := sessionRows(t, agent.SessionToolMaskedForTest(context.Background(), c, agent.McpCall{Kind: agent.CallListSessions}, mask))
	if strings.Contains(rows[0].LastTurn, "hawk") || rows[0].Description != "login bug" {
		t.Fatalf("%+v", rows)
	}
	user.desc, user.past = "merge with hawk", "hawk auth"
	rows = sessionRows(t, agent.SessionToolMaskedForTest(context.Background(), c, agent.McpCall{Kind: agent.CallListSessions}, mask))
	if strings.Contains(rows[0].Description, "hawk") || strings.Contains(rows[0].PastDescriptions[0], "hawk") {
		t.Fatalf("%+v", rows)
	}
}

func sessionRows(t *testing.T, r agent.McpReply) []agent.SessionRow {
	t.Helper()
	rows, _ := r.Value.(map[string]any)["sessions"].([]agent.SessionRow)
	if r.Err != nil || len(rows) == 0 {
		t.Fatalf("%+v %v", rows, r.Err)
	}
	return rows
}

// deliveringFake is your sessions' host reporting whether a message was typed or waits (#183).
type deliveringFake struct {
	*userFake
	delivery agent.Delivery
	waiting  int
	// flush_session (#187)
	flushResult agent.Delivery
	flushed     []string
}

func (d *deliveringFake) PromptDelivery(ctx context.Context, name, text string) (agent.Delivery, error) {
	if err := d.Prompt(ctx, name, text); err != nil {
		return agent.Delivery{}, err
	}
	return d.delivery, nil
}

func (d *deliveringFake) List(ctx context.Context) ([]agent.AgentInfo, error) {
	l, err := d.userFake.List(ctx)
	for i := range l {
		l[i].User.Waiting, l[i].User.Outdated = d.waiting, d.delivery.Outdated
	}
	return l, err
}

// #183: message_session says when the message waits, and why; list_sessions shows how many wait.
func TestMessageSessionSaysWhenTheMessageWaits(t *testing.T) {
	_, user, primary := newSessionHarness(t)
	d := &deliveringFake{userFake: user}
	c := agent.NewCompositeHost(primary, d)
	ctx := context.Background()
	send := func() map[string]any {
		t.Helper()
		r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"})
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		return r.Value.(map[string]any)
	}
	d.delivery = agent.Delivery{Status: "typed"}
	if got := send(); got["status"] != "sent" || got["why"] != nil {
		t.Fatalf("typed: %v", got)
	}
	for reason, words := range map[string]string{"working": "busy", "blocked": "question", "typing": "typing"} {
		d.delivery = agent.Delivery{Status: "waiting", Reason: reason}
		got := send()
		if why, _ := got["why"].(string); got["status"] != "waiting" || got["reason"] != reason || !strings.Contains(why, words) {
			t.Fatalf("%s: %v", reason, got)
		}
	}
	d.delivery = agent.Delivery{} // an older pix says nothing: sent, as before
	if got := send(); got["status"] != "sent" {
		t.Fatalf("older pix: %v", got)
	}
	d.waiting = 2
	r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallListSessions})
	if rows := r.Value.(map[string]any)["sessions"].([]agent.SessionRow); rows[0].Waiting != 2 {
		t.Fatalf("%+v", rows)
	}
}

func (d *deliveringFake) Flush(ctx context.Context, name string) (agent.Delivery, error) {
	d.flushed = append(d.flushed, name)
	return d.flushResult, nil
}

// #187: "send it anyway": flush_session sends a session's waiting messages in, and
// message_session with force sends the new one in at once.
func TestFlushSessionAndForcedMessages(t *testing.T) {
	_, user, primary := newSessionHarness(t)
	d := &deliveringFake{userFake: user, delivery: agent.Delivery{Status: "waiting", Reason: "typing"}, flushResult: agent.Delivery{Status: "typed"}}
	c := agent.NewCompositeHost(primary, d)
	ctx := context.Background()
	r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallFlushSession, Name: "focus-claude"})
	if v, _ := r.Value.(map[string]any); r.Err != nil || v["status"] != "sent" || len(d.flushed) != 1 {
		t.Fatalf("flush: %v %v %v", r.Value, r.Err, d.flushed)
	}
	r = agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi", Force: true})
	if v, _ := r.Value.(map[string]any); r.Err != nil || v["status"] != "sent" || len(d.flushed) != 2 {
		t.Fatalf("forced: %v %v %v", r.Value, r.Err, d.flushed)
	}
	// A working session still gets it with its next step: flush says it waits, and why.
	d.flushResult = agent.Delivery{Status: "waiting", Reason: "working"}
	r = agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallFlushSession, Name: "focus-claude"})
	if v, _ := r.Value.(map[string]any); v["status"] != "waiting" || v["reason"] != "working" {
		t.Fatalf("flush while working: %v", r.Value)
	}
}

// #189: a session running an older pix than the app's says so, in list_sessions and in what a
// message to it returns, with what to do; a current one says nothing about it.
func TestASessionOnAnOlderPixSaysSo(t *testing.T) {
	_, user, primary := newSessionHarness(t)
	d := &deliveringFake{userFake: user, delivery: agent.Delivery{Status: "typed"}}
	c := agent.NewCompositeHost(primary, d)
	ctx := context.Background()
	send := func() map[string]any {
		t.Helper()
		r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"})
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		return r.Value.(map[string]any)
	}
	if got := send(); got["outdated"] != nil || sessionRows(t, agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallListSessions}))[0].Outdated {
		t.Fatalf("current: %v", got)
	}
	for _, d.delivery = range []agent.Delivery{{Status: "typed", Outdated: true}, {Status: "waiting", Reason: "typing", Outdated: true}} {
		got := send()
		if update, _ := got["update"].(string); got["outdated"] != true || !strings.Contains(update, "--resume") {
			t.Fatalf("%+v: %v", d.delivery, got)
		}
	}
	if rows := sessionRows(t, agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallListSessions})); !rows[0].Outdated {
		t.Fatalf("%+v", rows)
	}
}
