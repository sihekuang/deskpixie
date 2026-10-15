package pixhost

import (
	"strings"
	"testing"
	"time"
)

func TestAddressFromRepoAndTool(t *testing.T) {
	a, ok := Address("/Users/me/Projects/DeskPixie", "claude", nil)
	if !ok || a != "deskpixie-claude" {
		t.Fatalf("%q %v", a, ok)
	}
	b, _ := Address("/Users/me/Projects/DeskPixie", "claude", []string{"deskpixie-claude"})
	if b != "deskpixie-claude-2" {
		t.Fatalf("%q", b)
	}
}

func TestAddressNoGitRoot(t *testing.T) {
	a, _ := Address("", "codex", nil)
	if a != "repo-codex" {
		t.Fatalf("%q", a)
	}
}

func TestAddressAvoidsWorkerNames(t *testing.T) {
	a, _ := Address("/x/focus", "claude", []string{"focus-claude", "focus-claude-2"})
	if a != "focus-claude-3" {
		t.Fatalf("%q", a)
	}
}

func TestDescriptionPrecedence(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	d.Set(SourceAuto, "fix login", t0)
	d.Set(SourceAgent, "fixing login redirect loop", t0)
	if d.Current() != "fixing login redirect loop" {
		t.Fatal(d.Current())
	}
	d.Set(SourceUser, "login bug", t0)
	if d.Set(SourceAgent, "something else", t0.Add(2*time.Minute)); d.Current() != "login bug" {
		t.Fatalf("user must win: %q", d.Current())
	}
	d.Clear(SourceUser)
	if d.Current() != "something else" {
		t.Fatal(d.Current())
	}
}

func TestQuickAgentUpdatesReplaceWithoutHistory(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	d.Set(SourceAgent, "one", t0)
	if !d.Set(SourceAgent, "two", t0.Add(30*time.Second)) || d.Current() != "two" {
		t.Fatalf("an agent update within a minute still replaces the description: %q", d.Current())
	}
	if len(d.Past()) != 0 {
		t.Fatalf("but leaves no history: %v", d.Past())
	}
	d.Set(SourceAgent, "three", t0.Add(2*time.Minute))
	if got := strings.Join(d.Past(), ","); got != "two" {
		t.Fatal(got)
	}
}

func TestMessageFragmentsAreNeverADescription(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	d.Set(SourceAuto, "fix login", t0)
	if d.Set(SourceAgent, `<agent-message from="a91706fc445c4c179"> [Subagent`, t0) || d.Current() != "fix login" {
		t.Fatalf("%q", d.Current())
	}
}

func TestPastKeepsFive(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	for i, s := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		d.Set(SourceAgent, s, t0.Add(time.Duration(i)*2*time.Minute))
	}
	if got := strings.Join(d.Past(), ","); got != "f,e,d,c,b" {
		t.Fatal(got)
	}
}

func TestCleanDescription(t *testing.T) {
	if got := CleanDescription("  two\nlines\t here "); got != "two lines here" {
		t.Fatalf("%q", got)
	}
	if got := CleanDescription(strings.Repeat("é", 80)); len([]rune(got)) != MaxDescription {
		t.Fatalf("len %d", len([]rune(got)))
	}
}
