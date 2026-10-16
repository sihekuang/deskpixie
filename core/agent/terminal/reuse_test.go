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

// psRunner answers `ps` with a canned process table and records every command run (including
// `ps`/`ps -E` themselves, so a test can assert nothing else ran).
type psRunner struct {
	mu    sync.Mutex
	ps    string
	psErr error
	// env answers `ps -E` for a pid: its command line followed by its environment.
	env map[string]string
	ran [][]string
}

func (p *psRunner) Run(argv []string, _ []agent.EnvVar) (agent.CommandOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ran = append(p.ran, argv)
	if argv[0] == psBin {
		if argv[1] == "-E" {
			return agent.CommandOutput{Stdout: p.env[argv[len(argv)-1]]}, nil
		}
		return agent.CommandOutput{Stdout: p.ps}, p.psErr
	}
	return agent.CommandOutput{}, nil
}
func (p *psRunner) RunBy(argv []string, env []agent.EnvVar, d time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(p, argv, env, d)
}
func (p *psRunner) SpawnDetached([]string, []agent.EnvVar) error { return errors.New("unused") }

// countingPlanner stands in for Warp/iTerm: it counts calls to Plan (not real opens — Plan never
// opens anything; it's also called to harvest the NewTerminal fallback for an attached Warp
// plan, which doesn't mean a terminal was actually shown).
type countingPlanner struct{ opened int }

func (c *countingPlanner) Name() string { return "warp" }
func (c *countingPlanner) Plan(agent.Attach) (agent.FocusPlan, error) {
	c.opened++
	return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "url", URL: "fake://new"}}, nil
}

// titledPlanner is Warp: its attach tab titles its window while the tab is active.
type titledPlanner struct{ countingPlanner }

func (*titledPlanner) WindowTitle() string { return "DeskPixie" }

// failingTitledPlanner is Warp, but its own Plan (the new-terminal fallback) always errors.
type failingTitledPlanner struct{ titledPlanner }

func (*failingTitledPlanner) Plan(agent.Attach) (agent.FocusPlan, error) {
	return agent.FocusPlan{}, errors.New("disk full")
}

func table(rows ...string) string { return strings.Join(rows, "\n") + "\n" }

var warpTable = table(
	"  1     0 ??       /sbin/launchd",
	"  500   1 ??       /Applications/Warp.app/Contents/MacOS/stable",
	"  610 500 ttys013  /bin/zsh -l",
	"  611 610 ttys013  /Users/x/.local/bin/herdr session attach deskpixie",
	"  620 500 ttys014  /bin/zsh -l",
	"  621 620 ttys014  /Users/x/.local/bin/herdr session attach deskpixie",
)

// warpEnv is what `ps -E` shows for the attach clients in warpTable: Warp gives every session
// its own focus URL.
var warpEnv = map[string]string{
	"611": "/Users/x/.local/bin/herdr session attach deskpixie TERM=xterm-256color WARP_FOCUS_URL=warp://session/1af8eb188540461cb7150e6a371d0e63 HOME=/Users/x\n",
	"621": "/Users/x/.local/bin/herdr session attach deskpixie WARP_FOCUS_URL=warp://session/0d845f78c7214b4aa4fe0c2df7ec636c\n",
}

func onlyPsRan(t *testing.T, ran [][]string) {
	t.Helper()
	for _, c := range ran {
		if c[0] != psBin {
			t.Fatalf("ran something other than ps: %q", c)
		}
	}
}

// #115: the attached session's own focus URL shows its exact tab, whatever window titles say
// (Warp restores a dead tab with the old title) and with no Accessibility permission. Candidates
// are newest client first.
func TestAnAttachedWarpSessionIsShownByItsFocusURL(t *testing.T) {
	r := &psRunner{ps: warpTable, env: warpEnv}
	inner := &titledPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"warp://session/0d845f78c7214b4aa4fe0c2df7ec636c", "warp://session/1af8eb188540461cb7150e6a371d0e63"}
	if !reflect.DeepEqual(plan.FocusURLs, want) {
		t.Fatalf("%q", plan.FocusURLs)
	}
	if len(plan.RaiseScript) == 0 {
		t.Fatal("no raise script")
	}
	if !reflect.DeepEqual(plan.KillPIDs, []int{611, 621}) {
		t.Fatalf("%v", plan.KillPIDs)
	}
	if plan.NewTerminal == nil {
		t.Fatal("no new terminal")
	}
	if inner.opened != 1 {
		t.Fatalf("opened %d", inner.opened)
	}
	onlyPsRan(t, r.ran)
}

// Go can no longer observe a focus URL "failing" at plan-build time (opening it is the app's
// job); the plan just carries every rung so the app can fall through at runtime.
func TestAWarpPlanCarriesBothTheFocusURLAndTheRaiseScript(t *testing.T) {
	r := &psRunner{ps: warpTable, env: warpEnv}
	inner := &titledPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FocusURLs) == 0 {
		t.Fatal("no focus urls")
	}
	if len(plan.RaiseScript) == 0 {
		t.Fatal("no raise script")
	}
	if len(plan.KillPIDs) == 0 {
		t.Fatal("no kill pids")
	}
	if plan.NewTerminal == nil {
		t.Fatal("no new terminal")
	}
	if inner.opened != 1 {
		t.Fatalf("opened %d", inner.opened)
	}
}

// Only a Warp session URL is a candidate, never another value of the variable.
func TestAFocusURLThatIsNotAWarpSessionIsIgnored(t *testing.T) {
	r := &psRunner{ps: warpTable, env: map[string]string{
		"621": "herdr session attach deskpixie WARP_FOCUS_URL=https://evil.example/x\n",
		"611": "herdr session attach deskpixie WARP_FOCUS_URL=warp://session/../x\n",
	}}
	plan, err := Reuse(&titledPlanner{}, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FocusURLs) != 0 {
		t.Fatalf("%q", plan.FocusURLs)
	}
	if len(plan.RaiseScript) == 0 {
		t.Fatal("no raise script")
	}
}

// Three clients; only the oldest and the newest have a WARP_FOCUS_URL. Candidates are newest
// first, and a client without one is skipped rather than producing an empty entry.
func TestWarpFocusURLCandidatesAreNewestFirstAndSkipClientsWithoutOne(t *testing.T) {
	ps := table(
		"  1     0 ??       /sbin/launchd",
		"  500   1 ??       /Applications/Warp.app/Contents/MacOS/stable",
		"  610 500 ttys013  /bin/zsh -l",
		"  611 610 ttys013  /Users/x/.local/bin/herdr session attach deskpixie",
		"  620 500 ttys014  /bin/zsh -l",
		"  621 620 ttys014  /Users/x/.local/bin/herdr session attach deskpixie",
		"  630 500 ttys015  /bin/zsh -l",
		"  631 630 ttys015  /Users/x/.local/bin/herdr session attach deskpixie",
	)
	r := &psRunner{ps: ps, env: map[string]string{
		"611": "herdr session attach deskpixie WARP_FOCUS_URL=warp://session/oldest\n",
		"631": "herdr session attach deskpixie WARP_FOCUS_URL=warp://session/newest\n",
		// 621 has no WARP_FOCUS_URL at all: skipped, not an empty/invalid entry.
	}}
	plan, err := Reuse(&titledPlanner{}, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"warp://session/newest", "warp://session/oldest"}; !reflect.DeepEqual(plan.FocusURLs, want) {
		t.Fatalf("%q", plan.FocusURLs)
	}
}

// C (#110): Warp can't be told which tab to show, but the window whose active tab is the attach
// has that tab's title: the plan carries the AppleScript that raises and fronts it.
func TestAnAttachedWarpTabsWindowIsRaised(t *testing.T) {
	r := &psRunner{ps: warpTable}
	inner := &titledPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FocusURLs) != 0 {
		t.Fatalf("%q", plan.FocusURLs)
	}
	script := strings.Join(plan.RaiseScript, " ")
	for _, want := range []string{`"/Applications/Warp.app"`, `name is "DeskPixie"`, "AXRaise", "frontmost"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %s: %s", want, script)
		}
	}
	if !reflect.DeepEqual(plan.KillPIDs, []int{611, 621}) {
		t.Fatalf("%v", plan.KillPIDs)
	}
	if plan.NewTerminal == nil {
		t.Fatal("no new terminal")
	}
	if inner.opened != 1 {
		t.Fatalf("opened %d", inner.opened)
	}
}

// A (#110): whether or not the app's own raise finds the window at runtime is no longer
// something Go can see — the plan always carries the pids to kill and a new terminal, for the
// app to use if every earlier rung fails.
func TestWithNothingToRaiseThePlanStillCarriesKillAndNewTerminal(t *testing.T) {
	r := &psRunner{ps: warpTable}
	inner := &titledPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.KillPIDs, []int{611, 621}) {
		t.Fatalf("%v", plan.KillPIDs)
	}
	if plan.NewTerminal == nil {
		t.Fatal("no new terminal")
	}
	if inner.opened != 1 {
		t.Fatalf("opened %d", inner.opened)
	}
}

// A failure building the new-terminal fallback (e.g. an unwritable tab-configs directory) must
// not discard the rungs ps already found: the focus URL or the raise may still work live, and
// only actually reaching the fallback (which Go can't observe) should ever fail the whole plan.
func TestAnInnerPlanFailureStillLetsTheAttachedRungsThrough(t *testing.T) {
	r := &psRunner{ps: warpTable, env: warpEnv}
	plan, err := Reuse(&failingTitledPlanner{}, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FocusURLs) == 0 {
		t.Fatal("no focus urls")
	}
	if len(plan.RaiseScript) == 0 {
		t.Fatal("no raise script")
	}
	if !reflect.DeepEqual(plan.KillPIDs, []int{611, 621}) {
		t.Fatalf("%v", plan.KillPIDs)
	}
	if plan.NewTerminal != nil {
		t.Fatalf("no fallback terminal when the inner planner fails: %+v", plan.NewTerminal)
	}
}

// A terminal without a window title to look for is brought forward as an app, with no fallback.
func TestAnAttachedTerminalWithoutAWindowTitleIsBroughtForward(t *testing.T) {
	r := &psRunner{ps: warpTable}
	inner := &countingPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	if plan.ActivateApp != "/Applications/Warp.app" {
		t.Fatalf("%q", plan.ActivateApp)
	}
	want := agent.FocusPlan{ActivateApp: plan.ActivateApp}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("%+v", plan)
	}
	if inner.opened != 0 {
		t.Fatalf("opened %d", inner.opened)
	}
	onlyPsRan(t, r.ran)
}

// An attached iTerm session is selected by its tty, with no fallback.
func TestAnAttachedItermSessionIsSelectedByItsTty(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  700   1 ??       /Applications/iTerm.app/Contents/MacOS/iTerm2",
		"  710 700 ttys004  /usr/bin/login -fp x",
		"  711 710 ttys004  -zsh",
		"  712 711 ttys004  /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingPlanner{}
	plan, err := Reuse(inner, r).Plan(attach())
	if err != nil {
		t.Fatal(err)
	}
	want := agent.FocusPlan{ItermSelect: plan.ItermSelect}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("%+v", plan)
	}
	if inner.opened != 0 {
		t.Fatalf("opened %d", inner.opened)
	}
	script := strings.Join(plan.ItermSelect, " ")
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
	inner := &countingPlanner{}
	if _, err := Reuse(inner, r).Plan(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 1 {
		t.Fatalf("opened %d", inner.opened)
	}
}

func TestAnAttachOutsideAnyAppOpensANewTerminal(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  900   1 ??       /usr/sbin/sshd",
		"  911 900 ttys030  /Users/x/.local/bin/herdr session attach deskpixie",
	)}
	inner := &countingPlanner{}
	if _, err := Reuse(inner, r).Plan(attach()); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 1 {
		t.Fatal("an attach over ssh can't be brought forward here: open one")
	}
}

func TestAFailingPsOpensANewTerminal(t *testing.T) {
	r := &psRunner{psErr: errors.New("no ps")}
	inner := &countingPlanner{}
	if _, err := Reuse(inner, r).Plan(attach()); err != nil || inner.opened != 1 {
		t.Fatalf("%v %d", err, inner.opened)
	}
}

func TestReuseKeepsTheTerminalsName(t *testing.T) {
	if n := Reuse(&countingPlanner{}, &psRunner{}).Name(); n != "warp" {
		t.Fatal(n)
	}
}
