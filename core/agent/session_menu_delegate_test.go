package agent

import (
	"strings"
	"testing"
)

// #383: a request whose words clearly pick an option of an open session menu answers that menu,
// whatever tool Pixie chose: 2026-10-27 14:44, "let's create both and assign to me" went to
// the coordinator as a new request (the voice model had lost track of the question), and it
// typed the words into another session.

var storiesMenu = SessionQuestion{ID: "q1", Label: "jira", App: "dev.warp.Warp-Stable", Kind: "menu",
	Text:    "Create these 2 stories under OVA-23377?",
	Options: []string{"Create both, unassigned", "Create both, assign to me", "Change something first"}}

// toCoordinator: the request went on to the coordinator (queued or in flight).
func toCoordinator(c *AgentController, id JobID) bool {
	return c.inbox.Queued(id) || c.inbox.Carries(id)
}

func TestADelegatedAnswerPicksTheOpenMenusOption(t *testing.T) {
	for _, tc := range []struct{ said, want string }{
		{"let's create both and assign to me.", "2"},
		{"Yeah, let's create both and assign to me.", "2"},
		{"Create both unassigned. Three points each is fine", "1"},
		{"change something first", "3"},
	} {
		c := ctl()
		askSession(c, storiesMenu, T0)
		fx := c.Step(DelegateInput(job(1), tc.said, "The user wants 'both' created."), T0+1)
		a := answers(fx)
		if len(a) != 1 || a[0].Name != sessionName || a[0].Text != "q1" || a[0].Answer != tc.want {
			t.Errorf("%q: %+v", tc.said, a)
			continue
		}
		if toCoordinator(c, job(1)) {
			t.Errorf("%q: also went to the coordinator", tc.said)
		}
		j, _ := c.jobs.Get(job(1))
		if j.Status != JobDone || j.Reply == nil || !strings.Contains(*j.Reply, "jira's menu") {
			t.Errorf("%q: job %+v", tc.said, j)
		}
		if c.confirms.IsPendingToken("hawk") || len(c.confirms.Pending()) != 0 {
			t.Errorf("%q: the menu still waits", tc.said)
		}
	}
}

func TestADelegatedRequestThatPicksNoMenuGoesToTheCoordinator(t *testing.T) {
	several := storiesMenu
	several.Multi = true
	for _, tc := range []struct {
		name  string
		menus []SessionQuestion
		said  string
	}{
		{"no menu open", nil, "let's create both and assign to me."},
		{"names no option", []SessionQuestion{storiesMenu}, "fix the login bug in the api repo"},
		{"two options", []SessionQuestion{storiesMenu}, "create both, unassigned or assign to me"},
		{"several picks", []SessionQuestion{several}, "create both, assign to me"},
		{"a push", []SessionQuestion{pushMenu}, "push"},
		{"says to push", []SessionQuestion{storiesMenu}, "create both and assign to me, then push it to main"},
	} {
		c := ctl()
		for _, q := range tc.menus {
			askSession(c, q, T0)
		}
		fx := c.Step(DelegateInput(job(1), tc.said, ""), T0+1)
		if a := answers(fx); len(a) != 0 {
			t.Errorf("%s: picked %+v", tc.name, a)
		}
		if !toCoordinator(c, job(1)) {
			t.Errorf("%s: never reached the coordinator", tc.name)
		}
		if len(tc.menus) > 0 && len(c.confirms.Pending()) != 1 {
			t.Errorf("%s: the menu should still wait", tc.name)
		}
	}
}

// Two sessions' menus that the words both answer: which is the user's to say, so the
// coordinator gets it.
func TestADelegatedAnswerTwoMenusFitGoesToTheCoordinator(t *testing.T) {
	c := ctl()
	askSession(c, storiesMenu, T0)
	other := storiesMenu
	c.Step(PaneInput(PaneEvent{Kind: PaneEventQuestion, Agent: "web-claude", Question: &other}), T0)
	fx := c.Step(DelegateInput(job(1), "create both, assign to me", ""), T0+1)
	if len(answers(fx)) != 0 || !toCoordinator(c, job(1)) {
		t.Fatalf("%+v", answers(fx))
	}
}

// An "and" left out on both sides: an option with "and" in it still matches itself, and two
// options joined by "and" still pick neither.
func TestMenuChoiceAndIsAFiller(t *testing.T) {
	opts := []string{"Red and Blue", "Green", "Postgres", "SQLite"}
	for _, tc := range []struct {
		said string
		want int
	}{
		{"Red and Blue", 1},
		{"red blue", 1},
		{"and green", 2},
		{"Postgres and SQLite", 0},
	} {
		if n, ok := MenuChoice(opts, tc.said); n != tc.want || ok != (tc.want != 0) {
			t.Errorf("%q: %d %v", tc.said, n, ok)
		}
	}
}
