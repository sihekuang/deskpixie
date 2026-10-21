package app

import (
	"log"
	"os"

	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux"
)

// SetupPrograms is what the onboarding window checks for (#149): the path each program was
// found at, "" when it wasn't.
type SetupPrograms struct {
	Herdr  string `json:"herdr"`
	Tmux   string `json:"tmux"`
	Claude string `json:"claude"`
	Codex  string `json:"codex"`
}

// FindSetupPrograms looks the programs up the way the session host and claudeAuto do: a
// configured herdr or tmux path only, else pathVar, else the usual install dirs under home.
func FindSetupPrograms(home, pathVar, herdrPath, tmuxPath string, exists func(string) bool) SetupPrograms {
	found := func(p string, ok bool) string {
		if !ok {
			return ""
		}
		return p
	}
	dirs := proc.InstallDirs(home)
	return SetupPrograms{
		Herdr:  found(herdr.FindHerdr(herdrPath, pathVar, home, exists)),
		Tmux:   found(tmux.FindTmux(tmuxPath, pathVar, exists)),
		Claude: found(proc.FindProgram("claude", "", pathVar, dirs, exists)),
		Codex:  found(proc.FindProgram("codex", "", pathVar, dirs, exists)),
	}
}

// setupPrograms is FindSetupPrograms for this process and the current settings.
func (a *App) setupPrograms() SetupPrograms {
	cfg := a.store.Current()
	home, _ := os.UserHomeDir()
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return FindSetupPrograms(home, os.Getenv("PATH"), deref(cfg.Agent.HerdrPath), deref(cfg.Agent.TmuxPath), isRegularFile)
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// projectsFolder: setup.projectsFolder (#216).
func (a *App) projectsFolder() settings.ProjectsFolder {
	home, _ := os.UserHomeDir()
	return settings.CheckProjectsFolder(a.store.Current().Agent.ProjectsRoot, home, settings.ProjectsFallback(a.paths.AppSupport))
}

// createProjectsFolder: setup.createProjectsFolder (#216). A folder that can't be made stays
// missing, its detail saying so; the agent picks a new folder up at its next start.
func (a *App) createProjectsFolder() (settings.ProjectsFolder, error) {
	home, _ := os.UserHomeDir()
	f, err := settings.CreateProjectsFolder(a.store.Current().Agent.ProjectsRoot, home, settings.ProjectsFallback(a.paths.AppSupport))
	if err != nil {
		log.Printf("setup: could not create the projects folder: %v", err)
		f.Detail = settings.CouldNotCreate
	}
	return f, nil
}
