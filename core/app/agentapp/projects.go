package agentapp

// The projects folder the agent uses (#216): see settings.CheckProjectsFolder.

import (
	"log"
	"os"
	"path/filepath"

	"github.com/sihekuang/deskpixie/core/settings"
)

// ProjectsFallback is DeskPixie's own projects folder: <app data>/agent/projects.
func ProjectsFallback(p Paths) string { return filepath.Join(p.Root, "projects") }

// UsedFolder is the projects folder the agent uses, decided at start.
type UsedFolder struct {
	// Root is canonical and exists; "" when neither folder could be had.
	Root string
	// Fallback: Root is DeskPixie's own folder, not the setting's.
	Fallback bool
	// Why the setting's folder wasn't used ("" when it was).
	Why string
}

// UseProjectsFolder is the setting's folder (created when missing), else DeskPixie's own
// (created too). Each step is logged.
func UseProjectsFolder(raw, home, fallback string) UsedFolder {
	c, err := settings.CreateProjectsFolder(raw, home, fallback)
	if err == nil && c.State == settings.FolderReady {
		return UsedFolder{Root: c.Path}
	}
	why := c.Detail
	if err != nil {
		why = "could not create " + c.Path + ": " + err.Error()
	}
	log.Printf("agent: projects folder not usable (%s); using %s", why, fallback)
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		log.Printf("agent: could not create %s: %v", fallback, err)
		return UsedFolder{Why: why}
	}
	root, err := canonical(fallback)
	if err != nil {
		return UsedFolder{Why: why}
	}
	return UsedFolder{Root: root, Fallback: true, Why: why}
}
