package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// fakeRunner records every Run and answers from a canned result; it never launches anything.
type fakeRunner struct {
	mu  sync.Mutex
	ran []ranCmd
	out agent.CommandOutput
	err error
}

type ranCmd struct {
	argv []string
	env  []agent.EnvVar
}

func (f *fakeRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, ranCmd{argv, env})
	return f.out, f.err
}
func (f *fakeRunner) RunBy(argv []string, env []agent.EnvVar, d time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(f, argv, env, d)
}
func (f *fakeRunner) SpawnDetached([]string, []agent.EnvVar) error { return errors.New("unused") }

func attach() []string {
	return []string{"/Users/x/.local/bin/herdr", "session", "attach", "deskpixie"}
}

func TestPlainWordsStayPlainAndOthersAreQuoted(t *testing.T) {
	if got := ShellLine(attach()); got != "/Users/x/.local/bin/herdr session attach deskpixie" {
		t.Fatal(got)
	}
	odd := []string{"/Users/Jane Doe/bin/herdr", "it's"}
	if got := ShellLine(odd); got != `'/Users/Jane Doe/bin/herdr' 'it'\''s'` {
		t.Fatal(got)
	}
}

func TestWarpWritesExactlyThisTabConfigAndOpensTheURL(t *testing.T) {
	r := &fakeRunner{}
	dir := filepath.Join(t.TempDir(), "tab_configs")
	if err := NewWarpLauncher(r, dir).Open(attach()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "deskpixie_attach.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "name = \"DeskPixie\"\n\n[[panes]]\nid = \"main\"\ntype = \"terminal\"\ncommands = [\"/Users/x/.local/bin/herdr session attach deskpixie\"]\n"
	if string(b) != want {
		t.Fatalf("%q", b)
	}
	if len(r.ran) != 1 || !reflect.DeepEqual(r.ran[0].argv, []string{"/usr/bin/open", "warp://tab_config/deskpixie_attach?new_window=true"}) {
		t.Fatalf("%v", r.ran)
	}
	if !reflect.DeepEqual(r.ran[0].env, baseEnv()) {
		t.Fatal("nothing else from the app's environment")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "deskpixie_attach.toml" {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestWarpEscapesQuotesForToml(t *testing.T) {
	toml := WarpTabConfig([]string{"/a b/herdr", `say "hi"`})
	if !strings.Contains(toml, `commands = ["'/a b/herdr' 'say \"hi\"'"]`) {
		t.Fatal(toml)
	}
}

func TestItermRunsTheExpectedAppleScript(t *testing.T) {
	r := &fakeRunner{}
	script := ItermScript(attach())
	if err := NewItermLauncher(r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 1 {
		t.Fatal(r.ran)
	}
	a := r.ran[0].argv
	if a[0] != "/usr/bin/osascript" || a[1] != "-e" || len(a) != 1+2*4 {
		t.Fatal(a)
	}
	var lines []string
	for i := 2; i < len(a); i += 2 {
		lines = append(lines, a[i])
	}
	if !reflect.DeepEqual(lines, script) || !reflect.DeepEqual(r.ran[0].env, baseEnv()) {
		t.Fatal(a)
	}
	want := []string{
		`tell application "iTerm"`,
		"activate",
		`create window with default profile command "/Users/x/.local/bin/herdr session attach deskpixie"`,
		"end tell",
	}
	if !reflect.DeepEqual(script, want) {
		t.Fatal(script)
	}
}

func TestAFailingLaunchIsAnErrorNamingTheTerminal(t *testing.T) {
	r := &fakeRunner{out: agent.CommandOutput{Status: 1, Stderr: "no app for warp://"}}
	err := NewWarpLauncher(r, t.TempDir()).Open(attach())
	if err == nil || err.Error() != "could not open Warp: no app for warp://" {
		t.Fatal(err)
	}
	if _, ok := err.(agent.LaunchError); !ok {
		t.Fatalf("%T", err)
	}
	// A runner error is wrapped too; iTerm failing names iTerm.
	r2 := &fakeRunner{err: errors.New("spawn failed")}
	err = NewItermLauncher(r2).Open(attach())
	if err == nil || !strings.HasPrefix(err.Error(), "could not open iTerm") {
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
	line := ItermScript(argv)[2]
	if line != `create window with default profile command "/p/herdr 'a\\b' 'q\"r' 'tab\there'"` {
		t.Fatal(line)
	}
	if toml := WarpTabConfig(argv); !strings.Contains(toml, `'a\\b'`) {
		t.Fatal(toml)
	}
}

func TestControlCharactersAreEscapedForTomlAndRejectedForIterm(t *testing.T) {
	argv := []string{"/p/herdr", "a\x07b\nc\x7f"}
	toml := WarpTabConfig(argv)
	if !strings.Contains(toml, `\u0007`) || !strings.Contains(toml, `\n`) || !strings.Contains(toml, `\u007F`) {
		t.Fatal(toml)
	}
	if strings.ContainsRune(toml, 7) || strings.ContainsRune(toml, 0x7f) {
		t.Fatal("raw control char in toml")
	}
	r := &fakeRunner{}
	err := NewItermLauncher(r).Open(argv)
	if err == nil || !strings.HasPrefix(err.Error(), "could not open iTerm") {
		t.Fatal(err)
	}
	if len(r.ran) != 0 {
		t.Fatal("nothing was run")
	}
}

// Extra (no Rust twin): an unchanged tab config is not rewritten.
func TestWarpDoesNotRewriteAnUnchangedTabConfig(t *testing.T) {
	r := &fakeRunner{}
	dir := t.TempDir()
	l := NewWarpLauncher(r, dir)
	if err := l.Open(attach()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "deskpixie_attach.toml")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := l.Open(attach()); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if !st.ModTime().Equal(old) {
		t.Fatal("rewritten")
	}
}

func TestLaunchersReportTheirNames(t *testing.T) {
	var _ agent.TerminalLauncher = (*WarpLauncher)(nil)
	var _ agent.TerminalLauncher = (*ItermLauncher)(nil)
	if NewWarpLauncher(nil, "").Name() != "warp" || NewItermLauncher(nil).Name() != "iterm" {
		t.Fatal("names")
	}
}
