package terminal

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

// sessionTable: a pix session (claude, pid 4242) under pix under a shell in some terminal app.
func sessionTable(app string) string {
	return table(
		"  1     0 ??       /sbin/launchd",
		"  500   1 ??       "+app+"/Contents/MacOS/term",
		"  610 500 ttys013  /bin/zsh -l",
		"  700 610 ttys013  /Users/x/.local/bin/pix claude",
		" 4242 700 ttys040  /Users/x/.local/bin/claude",
	)
}

// onlyThisRung fails unless plan has nothing but what want has: your own session is never killed
// or replaced by a new terminal, whatever happens.
func onlyThisRung(t *testing.T, plan, want agent.FocusPlan) {
	t.Helper()
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan %+v, want %+v", plan, want)
	}
}

// #148: your Warp tab is shown by the focus URL Warp gave the session (as AgentVille does).
func TestYourWarpSessionIsShownByItsOwnFocusURL(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/Warp.app"), env: map[string]string{
		"4242": "/Users/x/.local/bin/claude TERM_PROGRAM=WarpTerminal WARP_FOCUS_URL=warp://session/daccf2aa9b3b450eb477a09dfae3af2d HOME=/Users/x\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{FocusURLs: []string{"warp://session/daccf2aa9b3b450eb477a09dfae3af2d"}})
	onlyPsRan(t, r.ran)
}

// #148: your iTerm session is selected by the id iTerm gave it (its tty is pix's own PTY, not
// iTerm's, so the tty can't find it).
func TestYourItermSessionIsSelectedByItsSessionID(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/iTerm.app"), env: map[string]string{
		"4242": "/Users/x/.local/bin/claude TERM_PROGRAM=iTerm.app ITERM_SESSION_ID=w0t1p0:5AB334EE-8AC2-41AE-B5EE-2A2D2CE3729C\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{ItermSelect: ItermSessionScript("5AB334EE-8AC2-41AE-B5EE-2A2D2CE3729C")})
	if s := strings.Join(plan.ItermSelect, "\n"); !strings.Contains(s, `unique id of s is "5AB334EE-8AC2-41AE-B5EE-2A2D2CE3729C"`) {
		t.Fatalf("selects by session id:\n%s", s)
	}
}

// A terminal without a per-tab handle is brought forward as the app the session runs in.
func TestYourSessionInAnotherTerminalBringsThatAppForward(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/Ghostty.app"), env: map[string]string{
		"4242": "/Users/x/.local/bin/claude TERM_PROGRAM=ghostty HOME=/Users/x\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{ActivateApp: "/Applications/Ghostty.app"})
}

// Only a Warp session URL or a well-formed iTerm session id is trusted; anything else falls back
// to bringing the app forward (they end up in a URL and an AppleScript).
func TestAHandleThatIsNotWellFormedIsIgnored(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/Warp.app"), env: map[string]string{
		"4242": `/Users/x/.local/bin/claude WARP_FOCUS_URL=https://evil.example/x ITERM_SESSION_ID=w0t0p0:x"_do_shell_script_"y` + "\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{ActivateApp: "/Applications/Warp.app"})
}

// An IDE's integrated terminal runs under a helper bundle nested in the IDE's; the IDE is what
// comes forward. A Warp tab's variable it inherited (VS Code launched with `code .` from Warp)
// must not send the click to that Warp tab.
func TestYourSessionInAnIDEBringsTheIDEForwardNotAnInheritedWarpTab(t *testing.T) {
	r := &psRunner{ps: table(
		"  1     0 ??       /sbin/launchd",
		"  500   1 ??       /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
		"  510 500 ??       /Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper",
		"  610 510 ttys013  /bin/zsh -l",
		"  700 610 ttys013  /Users/x/.local/bin/pix claude",
		" 4242 700 ttys040  /Users/x/.local/bin/claude",
	), env: map[string]string{
		"4242": "/Users/x/.local/bin/claude TERM_PROGRAM=vscode WARP_FOCUS_URL=warp://session/daccf2aa9b3b450eb477a09dfae3af2d\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{ActivateApp: "/Applications/Visual Studio Code.app"})
}

// An iTerm session id inherited into Warp (iTerm launched Warp) is not used for a Warp session.
func TestAnItermIDInheritedIntoWarpIsIgnored(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/Warp.app"), env: map[string]string{
		"4242": "/Users/x/.local/bin/claude ITERM_SESSION_ID=w0t1p0:5AB334EE-8AC2-41AE-B5EE-2A2D2CE3729C\n",
	}}
	plan, err := NewSessionPlanner(r).PlanSession(4242)
	if err != nil {
		t.Fatal(err)
	}
	onlyThisRung(t, plan, agent.FocusPlan{ActivateApp: "/Applications/Warp.app"})
}

// A session whose process is gone (or isn't in any app) has no terminal to show.
func TestASessionWithNoProcessIsAnError(t *testing.T) {
	r := &psRunner{ps: sessionTable("/Applications/Warp.app"), env: map[string]string{}}
	if plan, err := NewSessionPlanner(r).PlanSession(9999); err == nil {
		t.Fatalf("no error, plan %+v", plan)
	}
}
