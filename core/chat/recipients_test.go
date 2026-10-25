package chat

import (
	"slices"
	"testing"
)

var testRecipients = []Recipient{
	{Name: "deskpixie-claude", Label: "chat window", Kind: "session", Yours: true},
	{Name: "api-codex", Label: "api-server", Kind: "session", Yours: true},
	{Name: "fix-login", Label: "fix-login", Kind: "worker"},
}

func TestRecipientsAreRunningSessionsThenWorkers(t *testing.T) {
	agents := []RosterRow{
		{Name: "coordinator", Label: "Coordinator", Kind: "coordinator", Reachable: true},
		{Name: "fix-login", Label: "fix-login", Kind: "worker", Status: "working", Reachable: true},
		{Name: "scratch", Label: "scratch", Kind: "pane", Reachable: true},
	}
	yours := []RosterRow{
		{Name: "api-codex", Label: "api-server", Kind: "session", Status: "idle", Reachable: true},
		{Name: "gone-claude", Label: "gone", Kind: "session", Reachable: false},
		{Name: "bare-claude", Kind: "session", Reachable: true},
	}
	got := Recipients(agents, yours)
	var names, labels []string
	for _, r := range got {
		names, labels = append(names, r.Name), append(labels, r.Label)
	}
	if !slices.Equal(names, []string{"api-codex", "bare-claude", "fix-login"}) {
		t.Fatal("recipients:", names)
	}
	if !slices.Equal(labels, []string{"api-server", "bare-claude", "fix-login"}) {
		t.Fatal("labels:", labels)
	}
	if !got[0].Yours || got[2].Yours || got[2].Kind != "worker" || got[0].Color != ColorFor("api-codex") {
		t.Fatalf("%+v", got)
	}
	if r := Recipients(nil, nil); r == nil || len(r) != 0 {
		t.Fatal("none: an empty list, never nil")
	}
}

func TestMentionsAreWholeWordsAfterASpace(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"@deskpixie-claude hi", []string{"deskpixie-claude"}},
		{"hi @API-CODEX, and you", []string{"api-codex"}},
		{"ask @api-codex.", []string{"api-codex"}},
		{"line one\n@fix-login go", []string{"fix-login"}},
		{"a@api-codex", nil},
		{"mail me@api-codex.com", nil},
		{"@api-codex2 is another", nil},
		{"@api-codex-2 too", nil},
		{"@ nothing @", nil},
		{"@nobody here", nil},
		{"@api-codex and @fix-login and @api-codex", []string{"api-codex", "fix-login"}},
	}
	for _, c := range cases {
		var got []string
		for _, r := range Mentions(c.text, testRecipients) {
			got = append(got, r.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.text, got, c.want)
		}
	}
}

func TestOneRecipientPerMessage(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		text   string
		target *string
		label  string
		want   string // "" = it goes
	}{
		{"to Pixie, no mentions", "what's up?", nil, "", ""},
		{"to Pixie, a word that names no one", "ping @nobody", nil, "", ""},
		{"to Pixie, an email address", "write to me@api-codex.com", nil, "", ""},
		{"to Pixie, one recipient named", "@api-codex ship it", nil, "", "Pick @api-codex from the list to send it to them."},
		{"to Pixie, any case", "@Api-Codex ship it", nil, "", "Pick @api-codex from the list to send it to them."},
		{"to Pixie, two named", "@fix-login and @api-codex", nil, "", "Send to one at a time: pick @fix-login or @api-codex."},
		{"to Pixie, the same one twice", "@fix-login @fix-login", nil, "", "Pick @fix-login from the list to send it to them."},
		{"a target, plain text", "ship it", str("api-codex"), "", ""},
		{"a target, mentioning itself", "@api-codex ship it", str("api-codex"), "", ""},
		{"a target, a word that names no one", "cc @nobody", str("api-codex"), "", ""},
		{"a target, someone else named", "tell @fix-login too", str("api-codex"), "",
			"This goes to api-server only. Remove @fix-login, or pick them instead."},
		{"a target, someone else inside a word", "x@fix-login", str("api-codex"), "", ""},
		{"a worker target", "go on", str("fix-login"), "", ""},
		{"an ended target, by its label", "hi", str("old-claude"), "docs", "docs isn't running anymore."},
		{"an unknown target, by its address", "hi", str("w9"), "", "w9 isn't running anymore."},
		{"an empty target", "hi", str(""), "", "That session isn't running anymore."},
	}
	for _, c := range cases {
		err := CheckOneRecipient(c.text, c.target, c.label, testRecipients)
		got := ""
		if err != nil {
			if _, ok := err.(*Refusal); !ok {
				t.Errorf("%s: not a Refusal: %T", c.name, err)
			}
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if err := CheckOneRecipient("hi", new(string), "", nil); err == nil {
		t.Error("no recipients at all: a target can't go")
	}
}
