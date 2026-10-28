package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// #384: the coordinator sees the menu a session waits on, and picks what the person named with
// answer_session_menu, as a tap would; a message to it would only wait behind the menu.

// menuTool picks of the menu the session waits on now, by its id as the coordinator saw it.
func menuTool(c *AgentController, agent string, picks []int, now Millis) []Effect {
	menu := ""
	for _, p := range c.confirms.Pending() {
		if p.Menu && p.Session == sessionName {
			menu = p.Question
		}
	}
	return menuToolOf(c, agent, menu, picks, now)
}

func menuToolOf(c *AgentController, agent, menu string, picks []int, now Millis) []Effect {
	return c.Step(McpInput(agent, McpCall{Kind: CallAnswerSessionMenu, Name: sessionName, Menu: menu, Picks: picks}), now)
}

func toolOutcome(fx []Effect) Effect {
	if r := effectsOfKind(fx, EfToolResult); len(r) == 1 {
		return r[0]
	}
	return Effect{}
}

func TestTheCoordinatorSeesASessionsMenu(t *testing.T) {
	c := ctl()
	askSession(c, dbChoice, T0)
	want := "Menu q8: Which database? Options (pick one): 1. Postgres | 2. SQLite | 3. MySQL"
	if got := c.SessionMenus(); len(got) != 1 || got[sessionName] != want {
		t.Fatalf("%q", got)
	}
	rows := withMenus([]SessionRow{{Name: sessionName, Status: "blocked"}, {Name: "other"}}, c.SessionMenus())
	if rows[0].Menu != want || rows[1].Menu != "" {
		t.Fatalf("%+v", rows)
	}
	if note := RequestNote(rows, nil, nil); !strings.Contains(note, `waiting on its menu: "`+want+`"`) {
		t.Fatal(note)
	}
	// Answered, it's gone.
	c.Step(ChooseInput("hawk", 1), T0+1)
	if got := c.SessionMenus(); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	several := dbChoice
	several.Multi = true
	c = ctl()
	askSession(c, several, T0)
	if got := c.SessionMenus()[sessionName]; !strings.Contains(got, "(pick one or more)") {
		t.Fatal(got)
	}
}

func TestAnswerSessionMenuPicksTheOptionLikeATap(t *testing.T) {
	c := ctl()
	askSession(c, dbChoice, T0)
	fx := menuTool(c, Coordinator, []int{2}, T0+1)
	if a := answers(fx); len(a) != 1 || a[0].Name != sessionName || a[0].Text != "q8" || a[0].Answer != "2" {
		t.Fatalf("%+v", a)
	}
	r := toolOutcome(fx)
	v, _ := r.Value.(map[string]any)
	if r.Err != "" || v["status"] != "answered" || strings.Join(v["picked"].([]string), ",") != "SQLite" {
		t.Fatalf("%+v", r)
	}
	if c.confirms.IsPendingToken("hawk") {
		t.Fatal("the menu still waits")
	}

	several := dbChoice
	several.Multi = true
	c = ctl()
	askSession(c, several, T0)
	fx = menuTool(c, Coordinator, []int{3, 1}, T0+1)
	if a := answers(fx); len(a) != 1 || a[0].Answer != "picks:1,3" {
		t.Fatalf("%+v", a)
	}
}

func TestAnswerSessionMenuRefusesWhatIsntItsOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		multi bool
		picks []int
		err   string
	}{
		{"out of range", false, []int{4}, "options are 1 to 3"},
		{"zero", false, []int{0}, "options are 1 to 3"},
		{"none", false, nil, "which option"},
		{"two on a one-pick menu", false, []int{1, 2}, "takes one option"},
		{"one twice", true, []int{2, 2}, "each once"},
	} {
		q := dbChoice
		q.Multi = tc.multi
		c := ctl()
		askSession(c, q, T0)
		fx := menuTool(c, Coordinator, tc.picks, T0+1)
		if r := toolOutcome(fx); !strings.Contains(r.Err, tc.err) || len(answers(fx)) != 0 {
			t.Errorf("%s: %+v %+v", tc.name, r, answers(fx))
		}
		if !c.confirms.IsPendingToken("hawk") {
			t.Errorf("%s: the menu should still wait", tc.name)
		}
	}
	// No menu open there; and a worker can't answer one.
	c := ctl()
	if r := toolOutcome(menuTool(c, Coordinator, []int{1}, T0)); !strings.Contains(r.Err, "isn't waiting on a menu") {
		t.Fatalf("%+v", r)
	}
	askSession(c, dbChoice, T0)
	if r := toolOutcome(menuTool(c, "worker-1", []int{1}, T0+1)); r.Err != "This tool is not available to you." || !c.confirms.IsPendingToken("hawk") {
		t.Fatalf("%+v", r)
	}
}

// As by voice, an option that pushes is the person's tap in the bubble.
func TestAnswerSessionMenuLeavesAPushToTheBubble(t *testing.T) {
	c := ctl()
	askSession(c, pushMenu, T0)
	fx := menuTool(c, Coordinator, []int{1}, T0+1)
	if r := toolOutcome(fx); !strings.Contains(r.Err, "push") || !strings.Contains(r.Err, "bubble") || len(answers(fx)) != 0 {
		t.Fatalf("%+v %+v", r, answers(fx))
	}
	if len(c.confirms.Pending()) != 1 {
		t.Fatal("the menu should still wait")
	}
	if a := answers(menuTool(c, Coordinator, []int{2}, T0+2)); len(a) != 1 || a[0].Answer != "2" { // "Don't push"
		t.Fatalf("%+v", a)
	}
}

// A permission dialog is no menu: only the person allows it (bubble or confirm word).
func TestAnswerSessionMenuLeavesPermissionsAlone(t *testing.T) {
	c := ctl()
	askSession(c, SessionQuestion{ID: "q9", Label: "api", Kind: "permission", Tool: "Bash", Input: `{"command":"git push"}`,
		Text: "Run git push?", Prompt: "Run git push?", Options: []string{"Yes", "No"}}, T0)
	if r := toolOutcome(menuTool(c, Coordinator, []int{1}, T0+1)); !strings.Contains(r.Err, "isn't waiting on a menu") {
		t.Fatalf("%+v", r)
	}
	if len(c.SessionMenus()) != 0 {
		t.Fatal(c.SessionMenus())
	}
}

func TestParseAnswerSessionMenu(t *testing.T) {
	for _, tc := range []struct {
		args  map[string]any
		picks []int
		err   string
	}{
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{2.0}}, []int{2}, ""},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{json.Number("2")}}, []int{2}, ""}, // as the MCP server decodes it
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{json.Number("2.5")}}, nil, "option numbers"},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{"3", 1.0}}, []int{3, 1}, ""},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": 2.0}, []int{2}, ""},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{}}, nil, "option numbers"},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{1.5}}, nil, "option numbers"},
		{map[string]any{"name": "api-claude", "menu": "q1", "options": []any{"SQLite"}}, nil, "option numbers"},
		{map[string]any{"name": "api-claude", "menu": "q1"}, nil, "option numbers"},
		{map[string]any{"name": "api-claude", "options": []any{1.0}}, nil, "menu"}, // review of #387: which menu, always
		{map[string]any{"options": []any{1.0}}, nil, "name"},
	} {
		call, err := ParseToolCall(RoleCoordinator, "answer_session_menu", tc.args)
		switch {
		case tc.err != "":
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: %v", tc.args, err)
			}
		case err != nil || call.Kind != CallAnswerSessionMenu || call.Name != "api-claude" || call.Menu != "q1" || !slicesEqual(call.Picks, tc.picks):
			t.Errorf("%v: %+v %v", tc.args, call, err)
		}
	}
	if _, err := ParseToolCall(RoleWorker, "answer_session_menu", map[string]any{"name": "api-claude", "options": []any{1.0}}); err == nil {
		t.Fatal("a worker has no answer_session_menu")
	}
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Review of #387: a pick for a menu that has since been answered never lands on the newer menu
// the session asks; a question that pushes, or an option whose description does, is the
// person's tap; and the menu is masked as it is read, against confirm words given out since.
func TestTheMenuToolPicksOnlyTheMenuItSaw(t *testing.T) {
	c := ctl()
	askSession(c, dbChoice, T0)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventQuestionGone, Agent: sessionName, Question: &SessionQuestion{ID: dbChoice.ID}}), T0+1)
	drop := SessionQuestion{ID: "q9", Label: "api", Kind: "menu", Text: "Drop the old tables?", Options: []string{"Yes", "No"}}
	askSession(c, drop, T0+2)
	fx := menuToolOf(c, Coordinator, "q8", []int{1}, T0+3)
	if r := toolOutcome(fx); !strings.Contains(r.Err, "That menu was answered") || !strings.Contains(r.Err, "Drop the old tables?") {
		t.Fatalf("%+v", r)
	}
	if len(answers(fx)) != 0 || len(c.confirms.Pending()) != 1 {
		t.Fatal("the newer menu was answered")
	}
}

func TestTheMenuToolLeavesAPushInTheQuestionOrDescriptionToThePerson(t *testing.T) {
	for _, q := range []SessionQuestion{
		{ID: "q4", Label: "api", Kind: "menu", Text: "Push main to origin?", Options: []string{"Yes", "No"}},
		{ID: "q5", Label: "api", Kind: "menu", Text: "Ready?", Options: []string{"Ship it", "Wait"}, Details: []string{"push and merge the PR", ""}},
	} {
		c := ctl()
		askSession(c, q, T0)
		fx := menuTool(c, Coordinator, []int{1}, T0+1)
		if r := toolOutcome(fx); !strings.Contains(r.Err, "the person picks it in the bubble") {
			t.Errorf("%s: %+v", q.Text, r)
		}
		if len(answers(fx)) != 0 {
			t.Errorf("%s: answered", q.Text)
		}
	}
	// The description is shown, so the coordinator knows what an option does.
	c := ctl()
	askSession(c, SessionQuestion{ID: "q6", Label: "api", Kind: "menu", Text: "Which?", Options: []string{"A", "B"}, Details: []string{"the fast one", ""}}, T0)
	if got := c.SessionMenus()[sessionName]; !strings.Contains(got, "1. A (the fast one) | 2. B") {
		t.Fatal(got)
	}
}
