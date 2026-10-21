package settings

// The projects folder on disk (#216): agent.projectsRoot, created when it is missing; when it
// can't be used or created, workers use DeskPixie's own folder instead. The agent decides at
// start (agentapp.UseProjectsFolder); the window checks and creates it here.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Projects folder states, as setup.projectsFolder reports them.
const (
	// FolderReady: the setting's folder exists and can be used.
	FolderReady = "ready"
	// FolderMissing: it doesn't exist yet; it can be created (and is, at start).
	FolderMissing = "missing"
	// FolderUnusable: it can't be used or created; workers use the fallback.
	FolderUnusable = "unusable"
)

// CouldNotCreate is a missing folder's detail once creating it failed.
const CouldNotCreate = "Couldn't create it (no permission?). Workers use DeskPixie's own folder instead."

// ProjectsFolder is the projects folder setting checked on disk (setup.projectsFolder).
type ProjectsFolder struct {
	State string `json:"state"`
	// Path is the setting's folder as a full path ("" when the setting can't be read as one).
	Path string `json:"path"`
	// Fallback is DeskPixie's own projects folder, used when the setting's can't be.
	Fallback string `json:"fallback"`
	// Detail is one line for the window.
	Detail string `json:"detail"`
}

// ProjectsFallback is DeskPixie's own projects folder: <app data>/agent/projects (the agent's
// folder; agentapp.Paths.Root).
func ProjectsFallback(appSupport string) string {
	return filepath.Join(appSupport, "agent", "projects")
}

// CheckProjectsFolder reads the setting's folder without changing anything.
func CheckProjectsFolder(raw, home, fallback string) ProjectsFolder {
	out := ProjectsFolder{Fallback: fallback}
	if root, err := ProjectsRoot(raw, home); err == nil {
		out.State, out.Path, out.Detail = FolderReady, root, "Found"
		return out
	}
	joined, err := ExpandProjectsRoot(raw, home)
	if err != nil {
		why := "Only ~/… paths are understood, not ~name."
		if strings.TrimSpace(raw) == "" {
			why = "No folder is set."
		}
		out.State, out.Detail = FolderUnusable, unusableDetail(why)
		return out
	}
	out.Path = joined
	switch _, lerr := os.Lstat(joined); {
	case !BelowHome(joined, home):
		out.State, out.Detail = FolderUnusable, unusableDetail("It must be inside your home folder.")
	case errors.Is(lerr, fs.ErrNotExist):
		out.State, out.Detail = FolderMissing, "Doesn't exist yet. DeskPixie creates it when it starts, or create it now."
	default:
		out.State, out.Detail = FolderUnusable, unusableDetail("Something else is at that path, or it leads outside your home folder.")
	}
	return out
}

func unusableDetail(why string) string {
	return why + " Workers use DeskPixie's own folder instead."
}

// CreateProjectsFolder creates the setting's folder when it is missing, and reads it again.
// Anything else is left as it is.
func CreateProjectsFolder(raw, home, fallback string) (ProjectsFolder, error) {
	c := CheckProjectsFolder(raw, home, fallback)
	if c.State != FolderMissing {
		return c, nil
	}
	if err := os.MkdirAll(c.Path, 0o755); err != nil {
		return c, err
	}
	return CheckProjectsFolder(raw, home, fallback), nil
}
