package terminal

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// psRunner answers `ps` with a canned process table and records every other command.
type psRunner struct {
	mu    sync.Mutex
	ps    string
	psErr error
	// raise answers osascript (the Warp window raise): its stdout, or raiseErr.
	raise    string
	raiseErr error
	ran      [][]string
}

func (p *psRunner) Run(argv []string, _ []agent.EnvVar) (agent.CommandOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if argv[0] == psBin {
		return agent.CommandOutput{Stdout: p.ps}, p.psErr
	}
	p.ran = append(p.ran, argv)
	if argv[0] == "/usr/bin/osascript" {
		return agent.CommandOutput{Stdout: p.raise}, p.raiseErr
	}
	return agent.CommandOutput{}, nil
}
func (p *psRunner) RunBy(argv []string, env []agent.EnvVar, d time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(p, argv, env, d)
}
func (p *psRunner) SpawnDetached([]string, []agent.EnvVar) error { return errors.New("unused") }

// countingLauncher stands in for Warp/iTerm: it counts new terminals.
type countingLauncher struct{ opened int }

func (c *countingLauncher) Name() string            { return "warp" }
func (c *countingLauncher) Open(agent.Attach) error { c.opened++; return nil }

// titledLauncher is Warp: its attach tab titles its window while the tab is active.
type titledLauncher struct{ countingLauncher }

func (*titledLauncher) WindowTitle() string { return "DeskPixie" }

func table(rows ...string) string { return strings.Join(rows, "\n") + "\n" }

var warpTable = table(
	"  1     0 ??       /sbin/launchd",
	"  500   1 ??       /Applications/Warp.app/Contents/MacOS/stable",
	"  610 500 ttys013  /bin/zsh -l",
	"  611 610 ttys013  /Users/x/.local/bin/herdr session attach deskpixie",
	"  620 500 ttys014  /bin/zsh -l",
	"  621 620 ttys014  /Users/x/.local/bin/herdr session attach deskpixie",
)

// C (#110): Warp can't be told which tab to show, but the window whose active tab is the attach
// has that tab's title: it is raised and Warp brought to the front.
func TestAnAttachedWarpTabsWindowIsRaised(t *testing.T) {
	r := &psRunner{ps: warpTable, raise: "ok\n"}
	inner := &titledLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 0 || len(r.ran) != 1 || r.ran[0][0] != "/usr/bin/osascript" {
		t.Fatalf("opened %d, ran %q", inner.opened, r.ran)
	}
	script := strings.Join(r.ran[0], " ")
	for _, want := range []string{`"/Applications/Warp.app"`, `name is "DeskPixie"`, "AXRaise", "frontmost"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %s: %s", want, script)
		}
	}
}

// A (#110): no window shows the attach tab (it sits behind another tab), or macOS refused the
// window list (no Accessibility or Automation permission): the attached terminals are ended and
// a new one opens, in the front window, so the one that shows is the only one.
func TestWithNoWarpWindowShowingTheAttachTheOldOnesEndAndANewOneOpens(t *testing.T) {
	for _, r := range []*psRunner{
		{ps: warpTable, raise: "none\n"},
		{ps: warpTable, raiseErr: errors.New("not allowed assistive access")},
	} {
		inner := &titledLauncher{}
		if err := Reuse(inner, r).Open(attach()); err != nil {
			t.Fatal(err)
		}
		if inner.opened != 1 {
			t.Fatalf("opened %d", inner.opened)
		}
		want := [][]string{{"/bin/kill", "-HUP", "611", "621"}}
		if got := r.ran[1:]; !reflect.DeepEqual(got, want) {
			t.Fatalf("%q", r.ran)
		}
	}
}

// A terminal without a window title to look for is brought forward as an app.
func TestAnAttachedTerminalWithoutAWindowTitleIsBroughtForward(t *testing.T) {
	r := &psRunner{ps: warpTable}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"/usr/bin/open", "-a", "/Applications/Warp.app"}}; inner.opened != 0 || !reflect.DeepEqual(r.ran, want) {
		t.Fatalf("opened %d, ran %q", inner.opened, r.ran)
	}
}

func TestAnAttachedItermSessionIsSelectedByItsTty(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  700   1 ??       /Applications/iTerm.app/Contents/MacOS/iTerm2",
		"  710 700 ttys004  /usr/bin/login -fp x",
		"  711 710 ttys004  -zsh",
		"  712 711 ttys004  /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 0 || len(r.ran) != 1 || r.ran[0][0] != "/usr/bin/osascript" {
		t.Fatalf("opened %d, ran %q", inner.opened, r.ran)
	}
	script := strings.Join(r.ran[0], " ")
	for _, want := range []string{`tell application "iTerm"`, `"/dev/ttys004"`, "select"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %s: %s", want, script)
		}
	}
}

func TestWithNothingAttachedANewTerminalOpens(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  500   1 ??       /Applications/Warp.app/Contents/MacOS/stable",
		// Another session, and a command that merely mentions the attach line, don't count.
		"  611 500 ttys013  /Users/x/.local/bin/herdr session attach other",
		"  612 500 ttys014  grep /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 1 || len(r.ran) != 0 {
		t.Fatalf("opened %d, ran %q", inner.opened, r.ran)
	}
}

func TestAnAttachOutsideAnyAppOpensANewTerminal(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  900   1 ??       /usr/sbin/sshd",
		"  911 900 ttys030  /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 1 {
		t.Fatal("an attach over ssh can't be brought forward here: open one")
	}
}

func TestAFailingPsOpensANewTerminal(t *testing.T) {
	r := &psRunner{psErr: errors.New("no ps")}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil || inner.opened != 1 {
		t.Fatalf("%v %d", err, inner.opened)
	}
}

func TestReuseKeepsTheTerminalsName(t *testing.T) {
	if n := Reuse(&countingLauncher{}, &psRunner{}).Name(); n != "warp" {
		t.Fatal(n)
	}
}
