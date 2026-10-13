// Package terminal opens a terminal window attached to the agent session (Warp via a tab
// config and warp:// URL, iTerm via AppleScript). Port of ai-mascot crates/terminal-macos.
// Each launcher runs its commands through an injected agent.CommandRunner.
package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/sihekuang/deskpixie/core/agent"
)

// WarpStem is the app-owned Tab Config file stem (spec §6: was ai_mascot_attach).
const WarpStem = "deskpixie_attach"

// baseEnv is the environment for open / osascript (nothing else is passed on).
func baseEnv() []agent.EnvVar {
	return []agent.EnvVar{{Key: "PATH", Value: "/usr/bin:/bin:/usr/sbin:/sbin"}}
}

func safeWord(s string) bool {
	if s == "" || s[0] == '=' {
		return false
	}
	for _, c := range s {
		ok := c < 128 && (c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune("-_./:=@+,", c))
		if !ok {
			return false
		}
	}
	return true
}

// ShellLine is argv as one shell command line: plain words stay as they are, anything else
// is single-quoted (' becomes '\”).
func ShellLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if safeWord(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// quoted is a double-quoted literal for TOML and AppleScript. \n \t \r are valid in both;
// other control characters (and U+007F) become \u00XX, which only TOML accepts, so
// ItermLauncher.Open rejects them before they get here.
func quoted(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case unicode.IsControl(c):
			fmt.Fprintf(&b, `\u%04X`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func run(r agent.CommandRunner, argv []string, what string) error {
	out, err := r.Run(argv, baseEnv())
	if err != nil {
		return agent.LaunchError(fmt.Sprintf("could not open %s: %v", what, err))
	}
	if out.Status != 0 {
		return agent.LaunchError(fmt.Sprintf("could not open %s: %s", what, strings.TrimSpace(out.Stderr)))
	}
	return nil
}

// WarpLauncher keeps one app-owned Tab Config whose single pane runs the attach command,
// opened with warp://tab_config/<stem>?new_window=true.
type WarpLauncher struct {
	runner agent.CommandRunner
	dir    string // ~/.warp/tab_configs (Warp stable on macOS)
}

func NewWarpLauncher(runner agent.CommandRunner, dir string) *WarpLauncher {
	return &WarpLauncher{runner: runner, dir: dir}
}

// WarpTabConfig is the TOML for argv. The tab name is the user-visible product name.
func WarpTabConfig(argv []string) string {
	return fmt.Sprintf("name = \"DeskPixie\"\n\n[[panes]]\nid = \"main\"\ntype = \"terminal\"\ncommands = [%s]\n", quoted(ShellLine(argv)))
}

func (w *WarpLauncher) Name() string { return "warp" }

func (w *WarpLauncher) Open(argv []string) error {
	path := filepath.Join(w.dir, WarpStem+".toml")
	toml := WarpTabConfig(argv)
	if cur, err := os.ReadFile(path); err != nil || string(cur) != toml {
		fail := func(e error) error { return agent.LaunchError("could not open Warp: " + e.Error()) }
		if err := os.MkdirAll(w.dir, 0o755); err != nil {
			return fail(err)
		}
		// Atomic: a temp file in the same directory, then rename over the target.
		tmp := filepath.Join(w.dir, fmt.Sprintf(".%s.toml.%d.tmp", WarpStem, os.Getpid()))
		if err := os.WriteFile(tmp, []byte(toml), 0o644); err != nil {
			return fail(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return fail(err)
		}
	}
	url := "warp://tab_config/" + WarpStem + "?new_window=true"
	return run(w.runner, []string{"/usr/bin/open", url}, "Warp")
}

// ItermLauncher opens a new iTerm window whose session runs the attach command.
type ItermLauncher struct{ runner agent.CommandRunner }

func NewItermLauncher(runner agent.CommandRunner) *ItermLauncher { return &ItermLauncher{runner} }

// ItermScript is the AppleScript, one entry per line (each becomes one -e argument).
func ItermScript(argv []string) []string {
	return []string{
		`tell application "iTerm"`,
		"activate",
		"create window with default profile command " + quoted(ShellLine(argv)),
		"end tell",
	}
}

func (i *ItermLauncher) Name() string { return "iterm" }

func (i *ItermLauncher) Open(argv []string) error {
	// AppleScript string literals have no \u escape, so other control characters cannot be passed on.
	for _, a := range argv {
		for _, c := range a {
			if unicode.IsControl(c) && c != '\n' && c != '\t' && c != '\r' {
				return agent.LaunchError("could not open iTerm: the command contains control characters")
			}
		}
	}
	cmd := []string{"/usr/bin/osascript"}
	for _, l := range ItermScript(argv) {
		cmd = append(cmd, "-e", l)
	}
	return run(i.runner, cmd, "iTerm")
}
