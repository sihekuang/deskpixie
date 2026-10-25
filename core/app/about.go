package app

// Settings ▸ About (#295): which DeskPixie, core and pix are running. The shell says what only
// it knows (its own version, the OS, where its pix is, the login shell's PATH); the core finds
// the pix the terminal runs, asks each pix its version and says whether they match.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/buildinfo"
)

// AboutArgs are app.about's arguments.
type AboutArgs struct {
	// App is the shell's version, e.g. "0.19.0 (412)".
	App string `json:"app"`
	// OS is e.g. "macOS 15.1.0".
	OS string `json:"os"`
	// Pix is the pix inside the app.
	Pix string `json:"pix"`
	// Path is the login shell's PATH ("" = unknown: the usual install dirs are searched).
	Path string `json:"path"`
}

// AboutRow is one line of the About page.
type AboutRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// About is the About page: its rows, a note about the terminal's pix (Warning when it isn't
// the app's, which needs fixing; not when the terminal has none), and the text Copy puts on the
// pasteboard.
type About struct {
	Rows    []AboutRow `json:"rows"`
	Note    string     `json:"note,omitempty"`
	Warning bool       `json:"warning,omitempty"`
	Text    string     `json:"text"`
}

// AboutEnv is what BuildAbout looks at; tests fake it.
type AboutEnv struct {
	Home string
	// Core is this core's version (buildinfo.String).
	Core string
	// Exists: a program is there.
	Exists func(path string) bool
	// Resolve is path with its links followed ("" when it can't be).
	Resolve func(path string) string
	// PixVersion is what `pix version` prints after "pix ", or an error.
	PixVersion func(path string) (string, error)
}

// The About page's labels and values.
const (
	AboutApp         = "DeskPixie"
	AboutCore        = "Core"
	AboutPixApp      = "pix in the app"
	AboutPixTerminal = "pix in your terminal"
	aboutUnknown     = "unknown"
	aboutNotFound    = "not found"
	aboutNoRun       = "doesn't run"
)

// BuildAbout is the About page for in.
func BuildAbout(in AboutArgs, env AboutEnv) About {
	app := strings.TrimSpace(in.App)
	if app == "" {
		app = aboutUnknown
	}
	version := func(p string) string {
		if !env.Exists(p) {
			return aboutNotFound
		}
		v, err := env.PixVersion(p)
		if err != nil || v == "" {
			return aboutNoRun
		}
		return v
	}
	bundled := aboutNotFound
	if in.Pix != "" {
		bundled = version(in.Pix)
	}
	var dirs []string
	if in.Path == "" {
		dirs = proc.InstallDirs(env.Home)
	}
	terminal, note, warning := aboutNotFound, "", false
	link := filepath.Join(env.Home, ".local/bin/pix")
	if found, ok := proc.FindProgram("pix", "", in.Path, dirs, env.Exists); !ok {
		note = "Your terminal doesn't find pix. To start sessions with it, install the pix command on the Sessions page."
	} else {
		v := version(found)
		terminal = v + ", at " + tilde(found, env.Home)
		same := in.Pix != "" && env.Resolve(found) != "" && env.Resolve(found) == env.Resolve(in.Pix)
		where := tilde(found, env.Home)
		fix := "Install the pix command again on the Sessions page."
		if found != link {
			fix = "Remove it, or put ~/.local/bin first on your PATH, and install the pix command on the Sessions page."
		}
		switch {
		// Nothing to compare with: a dev build run from outside its bundle.
		case same || v == bundled || bundled == aboutNotFound || bundled == aboutNoRun:
		case v == aboutNoRun:
			warning = true
			note = "Your terminal's pix, at " + where + ", doesn't run. " + fix
		case found == link:
			warning = true
			note = "Your terminal's pix is " + v + ", the app's is " + bundled + ". " + fix
		default:
			warning = true
			note = "Your terminal runs another pix, " + v + " at " + where + "; the app's is " + bundled + ". " + fix
		}
	}
	rows := []AboutRow{
		{AboutApp, app},
		{AboutCore, env.Core},
		{AboutPixApp, bundled},
		{AboutPixTerminal, terminal},
	}
	var text strings.Builder
	for _, r := range rows {
		text.WriteString(r.Label + ": " + r.Value + "\n")
	}
	if osName := strings.TrimSpace(in.OS); osName != "" {
		text.WriteString(osName + "\n")
	}
	if note != "" {
		text.WriteString(note + "\n")
	}
	return About{Rows: rows, Note: note, Warning: warning, Text: text.String()}
}

// tilde is p with home written as ~.
func tilde(p, home string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// realAboutEnv looks at this Mac.
func realAboutEnv() AboutEnv {
	home, _ := os.UserHomeDir()
	return AboutEnv{
		Home:   home,
		Core:   buildinfo.String(),
		Exists: isRegularFile,
		Resolve: func(p string) string {
			r, err := filepath.EvalSymlinks(p)
			if err != nil {
				return ""
			}
			return r
		},
		PixVersion: pixVersion,
	}
}

// pixVersion runs `pix version` ("pix 0.19.0 (abc1234)") and gives what follows "pix ".
func pixVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "pix "), nil
}

func (a *App) registerAbout(r Registrar) {
	r.Register("app.about", func(raw json.RawMessage) (any, error) {
		var in AboutArgs
		if err := decodeArgs("app.about", raw, &in); err != nil {
			return nil, err
		}
		return BuildAbout(in, realAboutEnv()), nil
	})
}
