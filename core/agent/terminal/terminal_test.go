package terminal

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

func argv() []string {
	return []string{"/Users/x/.local/bin/herdr", "session", "attach", "deskpixie"}
}

// attach is the attach spec the runtime hands a planner: argv plus the starting folder.
func attach() agent.Attach {
	return agent.Attach{Argv: argv(), Dir: "/Users/x/Documents/Projects"}
}

// bare is argv with no starting folder (the line is argv alone).
func bare(a []string) agent.Attach { return agent.Attach{Argv: a} }

func TestPlainWordsStayPlainAndOthersAreQuoted(t *testing.T) {
	if got := ShellLine(argv()); got != "/Users/x/.local/bin/herdr session attach deskpixie" {
		t.Fatal(got)
	}
	odd := []string{"/Users/Jane Doe/bin/herdr", "it's"}
	if got := ShellLine(odd); got != `'/Users/Jane Doe/bin/herdr' 'it'\''s'` {
		t.Fatal(got)
	}
}

func TestWarpPlanWritesExactlyThisTabConfigAndReturnsItsURL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tab_configs")
	plan, err := NewWarpPlanner(dir).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "deskpixie_attach.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberate deviation from Rust: a tab title (not the herdr path), a starting folder,
	// and a `cd` first (Warp bug #9130 can ignore `directory` before commands run).
	want := "name = \"DeskPixie\"\ntitle = \"DeskPixie\"\n\n[[panes]]\nid = \"main\"\ntype = \"terminal\"\n" +
		"directory = \"/Users/x/Documents/Projects\"\n" +
		"commands = [\"cd '/Users/x/Documents/Projects' && /Users/x/.local/bin/herdr session attach deskpixie; exit\"]\n"
	if string(b) != want {
		t.Fatalf("%q", b)
	}
	// Deliberate deviation from Rust: no ?new_window=true. A new window also gets Warp's
	// default tab, so one click made two tabs; without it the URL adds exactly one tab to
	// the active window (and launches Warp when it is not running).
	wantPlan := &agent.NewTerminalAction{Kind: "url", URL: "warp://tab_config/deskpixie_attach"}
	if !reflect.DeepEqual(plan.NewTerminal, wantPlan) {
		t.Fatalf("%+v", plan)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "deskpixie_attach.toml" {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestWarpEscapesQuotesForToml(t *testing.T) {
	toml := WarpTabConfig(bare([]string{"/a b/herdr", `say "hi"`}))
	if !strings.Contains(toml, `commands = ["'/a b/herdr' 'say \"hi\"'; exit"]`) {
		t.Fatal(toml)
	}
}

func TestItermPlanReturnsTheExpectedAppleScript(t *testing.T) {
	script := ItermScript(attach())
	plan, err := NewItermPlanner().Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	wantNewTerminal := &agent.NewTerminalAction{Kind: "applescript", Script: script}
	if !reflect.DeepEqual(plan.NewTerminal, wantNewTerminal) {
		t.Fatalf("%+v", plan)
	}
	want := []string{
		`tell application "iTerm"`,
		"activate",
		// Deviation from Rust (as for Warp): start in the projects folder.
		`create window with default profile command "cd '/Users/x/Documents/Projects' && /Users/x/.local/bin/herdr session attach deskpixie"`,
		"end tell",
	}
	if !reflect.DeepEqual(script, want) {
		t.Fatal(script)
	}
}

func TestAFailingLaunchIsAnErrorNamingTheTerminal(t *testing.T) {
	// Warp: an unwritable tab-configs directory (a file sits where the directory should be).
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewWarpPlanner(filepath.Join(blocked, "tab_configs")).Plan(attach())
	if err == nil || !strings.HasPrefix(err.Error(), "could not open Warp: ") {
		t.Fatal(err)
	}
	if _, ok := err.(agent.LaunchError); !ok {
		t.Fatalf("%T", err)
	}
	// iTerm has nothing left to run, so its only failure mode left is the same control-character
	// check as TestControlCharactersAreEscapedForTomlAndRejectedForIterm/
	// TestItermRefusesAControlCharacterInTheFolder: naming the terminal still works.
	_, err = NewItermPlanner().Plan(bare([]string{"/p/herdr", "a\x07b"}))
	if err == nil || err.Error() != "could not open iTerm: the command contains control characters" {
		t.Fatal(err)
	}
}

func TestWordsStartingWithEqualsAreQuoted(t *testing.T) {
	if got := ShellLine([]string{"=cmd", "a=b"}); got != "'=cmd' a=b" {
		t.Fatal(got)
	}
}

func TestBackslashesAndAppleScriptSpecialsAreEscaped(t *testing.T) {
	argv := []string{"/p/herdr", `a\b`, `q"r`, "tab\there"}
	line := ItermScript(bare(argv))[2]
	if line != `create window with default profile command "/p/herdr 'a\\b' 'q\"r' 'tab\there'"` {
		t.Fatal(line)
	}
	if toml := WarpTabConfig(bare(argv)); !strings.Contains(toml, `'a\\b'`) {
		t.Fatal(toml)
	}
}

func TestControlCharactersAreEscapedForTomlAndRejectedForIterm(t *testing.T) {
	argv := []string{"/p/herdr", "a\x07b\nc\x7f"}
	toml := WarpTabConfig(bare(argv))
	if !strings.Contains(toml, `\u0007`) || !strings.Contains(toml, `\n`) || !strings.Contains(toml, `\u007F`) {
		t.Fatal(toml)
	}
	if strings.ContainsRune(toml, 7) || strings.ContainsRune(toml, 0x7f) {
		t.Fatal("raw control char in toml")
	}
	_, err := NewItermPlanner().Plan(bare(argv))
	if err == nil || !strings.HasPrefix(err.Error(), "could not open iTerm") {
		t.Fatal(err)
	}
}

// Extra (no Rust twin): an unchanged tab config is not rewritten.
func TestWarpDoesNotRewriteAnUnchangedTabConfig(t *testing.T) {
	dir := t.TempDir()
	l := NewWarpPlanner(dir)
	if _, err := l.Plan(attach()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "deskpixie_attach.toml")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Plan(attach()); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if !st.ModTime().Equal(old) {
		t.Fatal("rewritten")
	}
}

func TestLaunchersReportTheirNames(t *testing.T) {
	var _ agent.TerminalPlanner = (*WarpPlanner)(nil)
	var _ agent.TerminalPlanner = (*ItermPlanner)(nil)
	if NewWarpPlanner("").Name() != "warp" || NewItermPlanner().Name() != "iterm" {
		t.Fatal("names")
	}
}

// Extra (no Rust twin): the starting folder is shell-quoted in the cd and TOML-escaped in both keys.
func TestAnOddStartingFolderIsQuotedForTheShellAndEscapedForToml(t *testing.T) {
	a := agent.Attach{Argv: []string{"/p/herdr"}, Dir: `/Users/x/it's "here"`}
	if got := AttachLine(a); got != `cd '/Users/x/it'\''s "here"' && /p/herdr` {
		t.Fatal(got)
	}
	toml := WarpTabConfig(a)
	if !strings.Contains(toml, "directory = \"/Users/x/it's \\\"here\\\"\"\n") ||
		!strings.Contains(toml, `commands = ["cd '/Users/x/it'\\''s \"here\"' && /p/herdr; exit"]`) {
		t.Fatal(toml)
	}
	if line := ItermScript(a)[2]; line != `create window with default profile command "cd '/Users/x/it'\\''s \"here\"' && /p/herdr"` {
		t.Fatal(line)
	}
}

// Extra (no Rust twin): with no starting folder there is no directory key and no cd.
func TestNoStartingFolderMeansNoDirectoryAndNoCd(t *testing.T) {
	a := bare(argv())
	if got := AttachLine(a); got != ShellLine(argv()) {
		t.Fatal(got)
	}
	if toml := WarpTabConfig(a); strings.Contains(toml, "directory") || strings.Contains(toml, "cd ") {
		t.Fatal(toml)
	}
}

// Extra (no Rust twin): a control character in the folder is refused by iTerm too.
func TestItermRefusesAControlCharacterInTheFolder(t *testing.T) {
	_, err := NewItermPlanner().Plan(agent.Attach{Argv: argv(), Dir: "/a\x07b"})
	if err == nil || !strings.HasPrefix(err.Error(), "could not open iTerm") {
		t.Fatal(err)
	}
}
