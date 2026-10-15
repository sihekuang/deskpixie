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
	ran   [][]string
}

func (p *psRunner) Run(argv []string, _ []agent.EnvVar) (agent.CommandOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if argv[0] == psBin {
		return agent.CommandOutput{Stdout: p.ps}, p.psErr
	}
	p.ran = append(p.ran, argv)
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

func table(rows ...string) string { return strings.Join(rows, "\n") + "\n" }

func TestAnAttachedWarpTabIsBroughtForwardInsteadOfOpeningANewOne(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  500   1 ??       /Applications/Warp.app/Contents/MacOS/stable",
		"  610 500 ttys013  /bin/zsh -l",
		"  611 610 ttys013  /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingLauncher{}
	if err := Reuse(inner, r).Open(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 0 {
		t.Fatal("a terminal is already attached: no new one")
	}
	if want := [][]string{{"/usr/bin/open", "-a", "/Applications/Warp.app"}}; !reflect.DeepEqual(r.ran, want) {
		t.Fatalf("%q", r.ran)
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
