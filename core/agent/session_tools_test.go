package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// userFake is a secondary host whose agents are sessions you started.
type userFake struct {
	*agenttest.FakeSessionHost
	described map[string]string
}

func (u *userFake) Owns(n string) bool { _, ok := u.StatusOf(n); return ok }
func (u *userFake) List(ctx context.Context) ([]agent.AgentInfo, error) {
	l, err := u.FakeSessionHost.List(ctx)
	for i := range l {
		l[i].Origin = agent.OriginUser
		l[i].Cwd = "/x/focus"
		l[i].User = &agent.UserSession{Tool: "claude", Repo: "focus", Where: "WebStorm", Description: "login bug",
			PastDescriptions: []string{"auth"}, Reachable: true,
			Started: time.Now().Add(-90 * time.Minute), LastActive: time.Now().Add(-3 * time.Minute)}
	}
	return l, err
}
func (u *userFake) SetDescription(n, t string) error { u.described[n] = t; return nil }

func newSessionHarness(t *testing.T) (*agent.CompositeHost, *userFake, *agenttest.FakeSessionHost) {
	t.Helper()
	ctx := context.Background()
	primary := agenttest.NewFakeSessionHost()
	user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}}
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

// TestMessageSessionVerbatimIsUnlabeled (#129): verbatim:true must deliver the text raw, with
// no "Coordinator says:" prefix, so a session can tell it's really the user's own answer, not
// an instruction.
func TestMessageSessionVerbatimIsUnlabeled(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	ctx := context.Background()
	if r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "go ahead", Verbatim: true}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := user.Prompts(); len(got) != 1 || got[0].Agent != "focus-claude" || got[0].Text != "go ahead" {
		t.Fatalf("prompts: %v", got)
	}
}

// TestMessageSessionWithoutVerbatimIsLabeled (#129, extended): the default (verbatim omitted
// or false) is prefixed "Coordinator says: " instead of relying on Claude Code's own
// cross-session wrapper, which labels it as coming from another Claude session.
func TestMessageSessionWithoutVerbatimIsLabeled(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	ctx := context.Background()
	if r := agent.SessionToolForTest(ctx, c, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "hi"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := user.Prompts(); len(got) != 1 || got[0].Text != "Coordinator says: hi" {
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
	for _, k := range []agent.McpCallKind{agent.CallMessageSession, agent.CallInterruptSession, agent.CallCloseSession, agent.CallDescribeSession} {
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
		{"message_session", map[string]any{"name": "focus-claude", "text": "go ahead", "verbatim": true}, agent.McpCall{Kind: agent.CallMessageSession, Name: "focus-claude", Text: "go ahead", Verbatim: true}},
		{"interrupt_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallInterruptSession, Name: "focus-claude"}},
		{"close_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallCloseSession, Name: "focus-claude"}},
		{"describe_session", map[string]any{"name": "focus-claude", "text": " login\nbug "}, agent.McpCall{Kind: agent.CallDescribeSession, Name: "focus-claude", Text: "login bug"}},
		{"describe_session", map[string]any{"name": "focus-claude"}, agent.McpCall{Kind: agent.CallDescribeSession, Name: "focus-claude"}},
	}
	for _, c := range cases {
		got, err := agent.ParseToolCall(agent.RoleCoordinator, c.name, c.args)
		if err != nil || got.Kind != c.want.Kind || got.Name != c.want.Name || got.Text != c.want.Text || got.Verbatim != c.want.Verbatim {
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

// TestVerbatimFlagEndToEnd (#137): each form a model may send for verbatim, from the raw tool
// arguments to the text the session is sent.
func TestVerbatimFlagEndToEnd(t *testing.T) {
	const raw, labeled = "go ahead", "Coordinator says: go ahead"
	args := func(v any, set bool) map[string]any {
		a := map[string]any{"name": "focus-claude", "text": raw}
		if set {
			a["verbatim"] = v
		}
		return a
	}
	for _, c := range []struct {
		name string
		args map[string]any
		want string // "" means refused, nothing sent
	}{
		{"bool true is delivered raw", args(true, true), raw},
		{"string true is delivered raw", args("true", true), raw},
		{"mixed case with spaces is delivered raw", args(" True ", true), raw},
		{"string false gets the coordinator prefix", args("false", true), labeled},
		{"omitted gets the coordinator prefix", args(nil, false), labeled},
		{"an unknown word is refused and nothing is sent", args("yes", true), ""},
		{"a number is refused and nothing is sent", args(1, true), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, user, _ := newSessionHarness(t)
			call, err := agent.ParseToolCall(agent.RoleCoordinator, "message_session", c.args)
			if c.want == "" {
				if err == nil || !strings.Contains(err.Error(), "verbatim must be true or false") {
					t.Fatalf("want refusal naming verbatim, got %v", err)
				}
				if len(user.Prompts()) != 0 {
					t.Fatalf("a refused call must send nothing: %v", user.Prompts())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r := agent.SessionToolForTest(context.Background(), h, call); r.Err != nil {
				t.Fatal(r.Err)
			}
			if got := user.Prompts(); len(got) != 1 || got[0].Text != c.want {
				t.Fatalf("sent %v, want %q", got, c.want)
			}
		})
	}
	t.Run("start_worker reads override_existing_session as a string", func(t *testing.T) {
		base := map[string]any{"kind": "claude", "repo": "focus", "task": "fix it", "job_id": "job_0123456789ab"}
		base["override_existing_session"] = "true"
		got, err := agent.ParseToolCall(agent.RoleCoordinator, "start_worker", base)
		if err != nil || !got.Worker.OverrideExistingSession {
			t.Fatalf("got %+v, %v", got.Worker, err)
		}
		base["override_existing_session"] = "maybe"
		if _, err := agent.ParseToolCall(agent.RoleCoordinator, "start_worker", base); err == nil {
			t.Fatal(`override_existing_session "maybe" must be refused`)
		}
	})
}

// TestParseVerbatimString (#137): a model sometimes sends verbatim as the string "true". It
// must still be verbatim, never silently delivered as the coordinator's own prefixed text;
// anything that isn't a yes or no is an error the model can correct.
func TestParseVerbatimString(t *testing.T) {
	for _, c := range []struct {
		v    any
		want bool
	}{{"true", true}, {"TRUE", true}, {"false", false}, {false, false}} {
		got, err := agent.ParseToolCall(agent.RoleCoordinator, "message_session", map[string]any{"name": "x", "text": "go ahead", "verbatim": c.v})
		if err != nil || got.Verbatim != c.want {
			t.Errorf("verbatim %#v: got %v, %v", c.v, got.Verbatim, err)
		}
	}
	for _, v := range []any{"yes", 1} {
		if _, err := agent.ParseToolCall(agent.RoleCoordinator, "message_session", map[string]any{"name": "x", "text": "go ahead", "verbatim": v}); err == nil {
			t.Errorf("verbatim %#v must be refused, not treated as false", v)
		}
	}
}
