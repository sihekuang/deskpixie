package terminal

import (
	"strconv"
	"strings"

	"github.com/sihekuang/deskpixie/core/agent"
)

// psBin lists every process: pid, parent, tty and the full command line (-ww: never cut).
const psBin = "/bin/ps"

// ReusingLauncher brings forward a terminal already attached to the session instead of opening
// another one (#98): every click on Pixie, an update line or a roster row ends in Open, and the
// pane is focused before it. With nothing attached (or an attach it can't bring forward, e.g.
// over ssh), it opens a new terminal through the inner launcher.
type ReusingLauncher struct {
	inner  agent.TerminalLauncher
	runner agent.CommandRunner
}

// Reuse wraps inner (Warp, iTerm) so an attached terminal is reused.
func Reuse(inner agent.TerminalLauncher, runner agent.CommandRunner) *ReusingLauncher {
	return &ReusingLauncher{inner: inner, runner: runner}
}

func (l *ReusingLauncher) Name() string { return l.inner.Name() }

// windowTitled is a terminal whose attach tab titles its window while the tab is active (Warp,
// which can't be told which tab to show).
type windowTitled interface{ WindowTitle() string }

func (l *ReusingLauncher) Open(a agent.Attach) error {
	app, tty, clients, ok := l.attached(a.Argv)
	if !ok {
		return l.inner.Open(a)
	}
	titled, isTitled := l.inner.(windowTitled)
	if strings.HasSuffix(app, "/iTerm.app") || !isTitled {
		return l.bringForward(app, tty)
	}
	// #110: raise the window showing the attach tab; with none (the tab sits behind another, or
	// macOS refused the window list), end the attached terminals and open one in front, so the
	// one that shows is the only one (a tab we opened closes when its attach ends).
	if l.raiseWindow(app, titled.WindowTitle()) {
		return nil
	}
	kill := []string{"/bin/kill", "-HUP"}
	for _, pid := range clients {
		kill = append(kill, strconv.Itoa(pid))
	}
	_, _ = l.runner.Run(kill, baseEnv())
	return l.inner.Open(a)
}

// raiseWindow brings app's window titled title to the front, if it has one (System Events:
// needs Accessibility and Automation permission, else false).
func (l *ReusingLauncher) raiseWindow(app, title string) bool {
	cmd := []string{"/usr/bin/osascript"}
	for _, s := range []string{
		`tell application "System Events"`,
		"set p to first application process whose POSIX path of application file is " + quoted(app),
		"set ws to (windows of p whose name is " + quoted(title) + ")",
		`if (count of ws) is 0 then return "none"`,
		"set frontmost of p to true",
		`perform action "AXRaise" of item 1 of ws`,
		`return "ok"`,
		"end tell",
	} {
		cmd = append(cmd, "-e", s)
	}
	out, err := l.runner.Run(cmd, baseEnv())
	return err == nil && out.Status == 0 && strings.TrimSpace(out.Stdout) == "ok"
}

type proc struct {
	ppid    int
	tty     string
	command string
}

// attached finds a process running exactly argv (the attach command) and the app it runs in
// (the nearest ancestor inside a .app bundle), with its tty; clients are every process running
// argv in that app.
func (l *ReusingLauncher) attached(argv []string) (app, tty string, clients []int, ok bool) {
	out, err := l.runner.Run([]string{psBin, "-axww", "-o", "pid=,ppid=,tty=,command="}, baseEnv())
	if err != nil || out.Status != 0 {
		return "", "", nil, false
	}
	procs := map[int]proc{}
	var attaches []int
	line := strings.Join(argv, " ")
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
		p := proc{ppid: ppid, tty: f[2], command: strings.Join(f[3:], " ")}
		procs[pid] = p
		if p.command == line {
			attaches = append(attaches, pid)
		}
	}
	// appOf is the .app pid runs in, "" if none.
	appOf := func(pid int) string {
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
	for _, pid := range attaches {
		if a := appOf(pid); a != "" && !ok {
			app, tty, ok = a, procs[pid].tty, true
		}
	}
	for _, pid := range attaches {
		if ok && appOf(pid) == app {
			clients = append(clients, pid)
		}
	}
	return app, tty, clients, ok
}

// bringForward shows the attached terminal: in iTerm the exact window, tab and session (by its
// tty); elsewhere the app's front window (Warp can't be told which tab to show).
func (l *ReusingLauncher) bringForward(app, tty string) error {
	if strings.HasSuffix(app, "/iTerm.app") {
		cmd := []string{"/usr/bin/osascript"}
		for _, s := range ItermSelectScript("/dev/" + tty) {
			cmd = append(cmd, "-e", s)
		}
		return run(l.runner, cmd, "iTerm")
	}
	return run(l.runner, []string{"/usr/bin/open", "-a", app}, app)
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
