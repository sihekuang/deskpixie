package terminal

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/sihekuang/deskpixie/core/agent"
)

// SessionPlanner decides how to show one of your own pix sessions (#148): the very tab or pane it
// runs in, found at click time from its agent process's own environment, as AgentVille does.
// It only ever brings something forward — never kills or replaces your terminal.
type SessionPlanner struct {
	runner agent.CommandRunner // ps / ps -E only
}

func NewSessionPlanner(runner agent.CommandRunner) *SessionPlanner {
	return &SessionPlanner{runner: runner}
}

// itermSessionID is ITERM_SESSION_ID ("w0t1p0:<uuid>"); only the uuid names the session.
var itermSessionID = regexp.MustCompile(`(?:^|\s)ITERM_SESSION_ID=w\d+t\d+p\d+:([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})(?:\s|$)`)

// PlanSession is the plan for the session whose agent process is pid: Warp's focus URL for its
// tab, iTerm's session by its id, else the app it runs in. The agent runs in pix's own PTY, so its
// tty names nothing in the terminal; the variables it inherited from the terminal's shell do.
func (p *SessionPlanner) PlanSession(pid int) (agent.FocusPlan, error) {
	out, err := p.runner.Run([]string{psBin, "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)}, baseEnv())
	if err == nil && out.Status == 0 {
		if m := warpFocusURL.FindStringSubmatch(out.Stdout); m != nil {
			return agent.FocusPlan{FocusURLs: []string{m[1]}}, nil
		}
		if m := itermSessionID.FindStringSubmatch(out.Stdout); m != nil {
			return agent.FocusPlan{ItermSelect: ItermSessionScript(m[1])}, nil
		}
	}
	if procs, ok := processTable(p.runner); ok {
		if app := appOf(procs, pid); app != "" {
			return agent.FocusPlan{ActivateApp: app}, nil
		}
	}
	return agent.FocusPlan{}, agent.LaunchError(fmt.Sprintf("could not find the terminal of session process %d", pid))
}

// ItermSessionScript selects the iTerm session whose unique id is id (its window and tab too) and
// activates iTerm.
func ItermSessionScript(id string) []string {
	return []string{
		`tell application "iTerm"`,
		"activate",
		"repeat with w in windows",
		"repeat with t in tabs of w",
		"repeat with s in sessions of t",
		"if unique id of s is " + quoted(id) + " then",
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
