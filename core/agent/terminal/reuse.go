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

func (l *ReusingLauncher) Open(a agent.Attach) error {
	if app, tty, ok := l.attached(a.Argv); ok {
		return l.bringForward(app, tty)
	}
	return l.inner.Open(a)
}

type proc struct {
	ppid    int
	tty     string
	command string
}

// attached finds a process running exactly argv (the attach command) and the app it runs in
// (the nearest ancestor inside a .app bundle), with its tty.
func (l *ReusingLauncher) attached(argv []string) (app, tty string, ok bool) {
	out, err := l.runner.Run([]string{psBin, "-axww", "-o", "pid=,ppid=,tty=,command="}, baseEnv())
	if err != nil || out.Status != 0 {
		return "", "", false
	}
	procs := map[int]proc{}
	var clients []int
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
			clients = append(clients, pid)
		}
	}
	for _, pid := range clients {
		seen := map[int]bool{}
		for at := procs[pid].ppid; at > 1 && !seen[at]; at = procs[at].ppid {
			seen[at] = true
			p, known := procs[at]
			if !known {
				break
			}
			if i := strings.Index(p.command, ".app/Contents/MacOS/"); i >= 0 {
				return p.command[:i+len(".app")], procs[pid].tty, true
			}
		}
	}
	return "", "", false
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
