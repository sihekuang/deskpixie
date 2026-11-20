package pixhost

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/terminal"
)

// Tabs is the Herdr session your resumed sessions run in (#461); herdr.Host fits it.
type Tabs interface {
	RunInTab(ctx context.Context, label, cwd string, env []agent.EnvVar, line string) (string, error)
	FocusPane(ctx context.Context, pane string) error
	AttachCommand() []string
}

// Resumer resumes a past session in its own Herdr tab and plans showing it (#461: Go decides,
// the app's TerminalFocusExecutor shows).
type Resumer struct {
	History interface {
		List() []HistoryEntry
		Update(id string, f func(*HistoryEntry))
	}
	Tabs    Tabs                  // nil: Herdr isn't available
	Planner agent.TerminalPlanner // the agent.terminal setting's
	Pix     string                // the app's own pix, so resuming needs no installed command
	IsDir   func(string) bool     // nil: the real file system
}

var (
	ErrFolderGone   = agent.LaunchError("That project folder is gone.")
	ErrNoHerdr      = agent.LaunchError("Resuming needs Herdr. Set it up in Settings ▸ Agents.")
	ErrUnresumable  = agent.LaunchError("This conversation can't be resumed.")
	ErrNotInHistory = agent.LaunchError("That session is no longer in History.")
)

// resumableID is what a conversation id may be: it is typed into a terminal, where a control
// character (Ctrl-C, Enter, Escape) would act before the shell's quoting does.
var resumableID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Resume opens id's conversation in a new tab of the yours session, in its folder, and returns
// the plan that shows that tab.
func (r *Resumer) Resume(ctx context.Context, id string) (agent.FocusPlan, error) {
	list := r.History.List()
	i := slices.IndexFunc(list, func(e HistoryEntry) bool { return e.SessionID == id })
	switch {
	case !resumableID.MatchString(id):
		return agent.FocusPlan{}, ErrUnresumable
	case i < 0:
		return agent.FocusPlan{}, ErrNotInHistory
	case list[i].Unresumable:
		return agent.FocusPlan{}, ErrUnresumable
	case r.Tabs == nil:
		return agent.FocusPlan{}, ErrNoHerdr
	case !r.isDir(list[i].Cwd):
		return agent.FocusPlan{}, ErrFolderGone
	}
	e := list[i]
	line := terminal.ShellLine([]string{r.Pix, "claude", "--resume", id})
	// No PIXIE_AGENT_TOKEN: pix wraps Claude as a session of yours, not a worker.
	env := []agent.EnvVar{{Key: "PIX_WHERE", Value: "Herdr"}}
	label := cmp.Or(e.Description, filepath.Base(e.Cwd))
	pane, err := r.Tabs.RunInTab(ctx, label, e.Cwd, env, line)
	if agent.IsHostError(err, agent.HostNotFound) {
		return agent.FocusPlan{}, ErrNoHerdr
	}
	if err != nil {
		return agent.FocusPlan{}, err
	}
	// Known race, left as is: if Claude forks the conversation under a new id before this
	// lands, the old entry is already gone and the new one has no pane; a click on the session
	// then shows it by its process (the usual planner), not by its tab.
	r.History.Update(id, func(x *HistoryEntry) { x.HerdrPane = pane })
	return r.Show(ctx, pane)
}

// Show plans showing a resumed session's tab (a click on a session whose Where is Herdr):
// bring its pane forward, then attach a terminal to the session.
func (r *Resumer) Show(ctx context.Context, pane string) (agent.FocusPlan, error) {
	if r.Tabs == nil {
		return agent.FocusPlan{}, ErrNoHerdr
	}
	// A failed focus still shows the session by attaching, just maybe on another tab.
	_ = r.Tabs.FocusPane(ctx, pane)
	return r.Planner.Plan(agent.Attach{Argv: r.Tabs.AttachCommand()})
}

func (r *Resumer) isDir(path string) bool {
	if r.IsDir != nil {
		return r.IsDir(path)
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
