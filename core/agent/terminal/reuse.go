package terminal

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sihekuang/deskpixie/core/agent"
)

// psBin lists every process: pid, parent, tty and the full command line (-ww: never cut).
const psBin = "/bin/ps"

// ReusingPlanner decides to bring forward a terminal already attached to the session instead of
// a plan for another one (#98): every click on Pixie, an update line or a roster row ends in a
// Plan, and the pane is focused before it. In Warp it carries, in order, the session's focus URL
// (#115), the window titled for the attach tab (needs Accessibility), then a new terminal
// replacing the attached ones — the app walks that ladder itself, since whether each rung
// succeeds can only be known by trying it live. With nothing attached (or an attach it can't
// bring forward, e.g. over ssh), it delegates to the inner planner.
type ReusingPlanner struct {
	inner  agent.TerminalPlanner
	runner agent.CommandRunner // ps / ps -E only; nothing here executes an OS action anymore
}

// Reuse wraps inner (Warp, iTerm) so an attached terminal is reused.
func Reuse(inner agent.TerminalPlanner, runner agent.CommandRunner) *ReusingPlanner {
	return &ReusingPlanner{inner: inner, runner: runner}
}

func (l *ReusingPlanner) Name() string { return l.inner.Name() }

// windowTitled is a terminal whose attach tab titles its window while the tab is active (Warp,
// which can't be told which tab to show).
type windowTitled interface{ WindowTitle() string }

func (l *ReusingPlanner) Plan(a agent.Attach) (agent.FocusPlan, error) {
	app, tty, clients, ok := l.attached(a.Argv)
	if !ok {
		return l.inner.Plan(a)
	}
	titled, isTitled := l.inner.(windowTitled)
	// iTerm's session is selected by its tty; no fallback (iTerm always has the window).
	if strings.HasSuffix(app, "/iTerm.app") {
		return agent.FocusPlan{ItermSelect: ItermSelectScript("/dev/" + tty)}, nil
	}
	// A terminal with no window title to look for is brought forward as an app; no fallback.
	if !isTitled {
		return agent.FocusPlan{ActivateApp: app}, nil
	}
	// #115/#110: Warp's own focus URL, then raising the titled window, then (if the app finds
	// neither live) ending the attached clients and opening a new terminal.
	plan := agent.FocusPlan{
		FocusURLs:   l.warpFocusURLs(clients),
		RaiseScript: raiseWindowScript(app, titled.WindowTitle()),
		KillPIDs:    clients,
	}
	// A failure here (e.g. an unwritable tab-configs directory) must not discard the rungs ps
	// already found above — the focus URL or the raise may still work live, whether or not a
	// fallback could ever be built. Only actually reaching the fallback can fail the whole plan.
	if newTerminal, err := l.inner.Plan(a); err == nil {
		plan.NewTerminal = newTerminal.NewTerminal
	}
	return plan, nil
}

// warpFocusURL is the WARP_FOCUS_URL Warp sets in each session's environment.
var warpFocusURL = regexp.MustCompile(`(?:^|\s)WARP_FOCUS_URL=(warp://session/[0-9A-Za-z-]+)(?:\s|$)`)

// warpFocusURLs is every attach client's own WARP_FOCUS_URL (read via `ps -E`), newest first,
// clients without one skipped.
func (l *ReusingPlanner) warpFocusURLs(clients []int) []string {
	var urls []string
	for i := len(clients) - 1; i >= 0; i-- {
		pid := strconv.Itoa(clients[i])
		out, err := l.runner.Run([]string{psBin, "-E", "-ww", "-o", "command=", "-p", pid}, baseEnv())
		if err != nil || out.Status != 0 {
			continue
		}
		if m := warpFocusURL.FindStringSubmatch(out.Stdout); m != nil {
			urls = append(urls, m[1])
		}
	}
	return urls
}

// raiseWindowScript is the System Events AppleScript that raises app's window titled title (needs
// Accessibility and Automation permission; its "ok"/"none" result tells the app whether it found one).
func raiseWindowScript(app, title string) []string {
	return []string{
		`tell application "System Events"`,
		"set p to first application process whose POSIX path of application file is " + quoted(app),
		"set ws to (windows of p whose name is " + quoted(title) + ")",
		`if (count of ws) is 0 then return "none"`,
		"set frontmost of p to true",
		`perform action "AXRaise" of item 1 of ws`,
		`return "ok"`,
		"end tell",
	}
}

type proc struct {
	ppid    int
	tty     string
	command string
}

// attached finds a process running exactly argv (the attach command) and the app it runs in
// (the nearest ancestor inside a .app bundle), with its tty; clients are every process running
// argv in that app.
func (l *ReusingPlanner) attached(argv []string) (app, tty string, clients []int, ok bool) {
	procs, ok := processTable(l.runner)
	if !ok {
		return "", "", nil, false
	}
	ok = false
	var attaches []int
	line := strings.Join(argv, " ")
	for pid, p := range procs {
		if p.command == line {
			attaches = append(attaches, pid)
		}
	}
	slices.Sort(attaches)
	for _, pid := range attaches {
		if a := appOf(procs, pid); a != "" && !ok {
			app, tty, ok = a, procs[pid].tty, true
		}
	}
	for _, pid := range attaches {
		if ok && appOf(procs, pid) == app {
			clients = append(clients, pid)
		}
	}
	return app, tty, clients, ok
}

// processTable is every process from `ps`, by pid; false if ps failed.
func processTable(runner agent.CommandRunner) (map[int]proc, bool) {
	out, err := runner.Run([]string{psBin, "-axww", "-o", "pid=,ppid=,tty=,command="}, baseEnv())
	if err != nil || out.Status != 0 {
		return nil, false
	}
	procs := map[int]proc{}
	for _, row := range strings.Split(out.Stdout, "\n") {
		f := strings.Fields(row)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs[pid] = proc{ppid: ppid, tty: f[2], command: strings.Join(f[3:], " ")}
	}
	return procs, true
}

// appOf is the .app pid runs in (its nearest ancestor inside a .app bundle), "" if none.
func appOf(procs map[int]proc, pid int) string {
	seen := map[int]bool{}
	for at := procs[pid].ppid; at > 1 && !seen[at]; at = procs[at].ppid {
		seen[at] = true
		p, known := procs[at]
		if !known {
			break
		}
		if i := strings.Index(p.command, ".app/Contents/MacOS/"); i >= 0 {
			return p.command[:i+len(".app")]
		}
	}
	return ""
}

// ItermSelectScript selects the iTerm session on tty (its window and tab too) and activates iTerm.
func ItermSelectScript(tty string) []string {
	return []string{
		`tell application "iTerm"`,
		"activate",
		"repeat with w in windows",
		"repeat with t in tabs of w",
		"repeat with s in sessions of t",
		"if tty of s is " + quoted(tty) + " then",
		"select w",
		"select t",
		"select s",
		"return",
		"end if",
		"end repeat",
		"end repeat",
		"end repeat",
		"end tell",
	}
}
