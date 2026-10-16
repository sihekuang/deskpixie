// Package terminal decides how to show a terminal attached to the agent session (Warp via a
// tab config and warp:// URL, iTerm via AppleScript) as a FocusPlan (#116: the core decides, the
// app shows). Port of ai-mascot crates/terminal-macos.
package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// AttachLine is the shell line a terminal runs: `cd '<dir>' && <argv>` (the folder always
// single-quoted), or argv alone when there is no folder.
func AttachLine(a agent.Attach) string {
	line := ShellLine(a.Argv)
	if a.Dir == "" {
		return line
	}
	return "cd '" + strings.ReplaceAll(a.Dir, "'", `'\''`) + "' && " + line
}

// quoted is a double-quoted literal for TOML and AppleScript. \n \t \r are valid in both;
// other control characters (and U+007F) become \u00XX, which only TOML accepts, so
// ItermPlanner.Plan rejects them before they get here.
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

// WarpPlanner keeps one app-owned Tab Config whose single pane runs the attach command, shown
// with warp://tab_config/<stem>. Deliberate deviation from Rust: no ?new_window=true — a new
// window also gets Warp's default tab, so one click made two tabs; without it the URL adds
// exactly one tab to the active window (and launches Warp when it is not running).
type WarpPlanner struct {
	dir string // ~/.warp/tab_configs (Warp stable on macOS)
}

func NewWarpPlanner(dir string) *WarpPlanner { return &WarpPlanner{dir: dir} }

// WarpTabConfig is the TOML for an Attach. The config name and the tab title are the
// user-visible product name (without a title the tab is named after the herdr path).
// Deviation from Rust: the pane starts in a.Dir, set both as `directory` and as a leading
// `cd` because Warp can ignore `directory` before the commands run (Warp bug #9130).
func WarpTabConfig(a agent.Attach) string {
	var b strings.Builder
	b.WriteString("name = \"DeskPixie\"\ntitle = \"DeskPixie\"\n\n[[panes]]\nid = \"main\"\ntype = \"terminal\"\n")
	if a.Dir != "" {
		fmt.Fprintf(&b, "directory = %s\n", quoted(a.Dir))
	}
	// `exit`: a tab whose attach ends (replaced by a newer one, #110) closes instead of leaving a shell.
	fmt.Fprintf(&b, "commands = [%s]\n", quoted(AttachLine(a)+"; exit"))
	return b.String()
}

func (w *WarpPlanner) Name() string { return "warp" }

// WindowTitle is the attach tab's title, which its window shows while the tab is active.
func (w *WarpPlanner) WindowTitle() string { return "DeskPixie" }

func (w *WarpPlanner) Plan(a agent.Attach) (agent.FocusPlan, error) {
	path := filepath.Join(w.dir, WarpStem+".toml")
	toml := WarpTabConfig(a)
	if cur, err := os.ReadFile(path); err != nil || string(cur) != toml {
		fail := func(e error) (agent.FocusPlan, error) {
			return agent.FocusPlan{}, agent.LaunchError("could not open Warp: " + e.Error())
		}
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
	url := "warp://tab_config/" + WarpStem
	return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "url", URL: url}}, nil
}

// ItermPlanner opens a new iTerm window whose session runs the attach command (in a.Dir).
type ItermPlanner struct{}

func NewItermPlanner() *ItermPlanner { return &ItermPlanner{} }

// ItermScript is the AppleScript, one entry per line (each becomes one -e argument).
func ItermScript(a agent.Attach) []string {
	return []string{
		`tell application "iTerm"`,
		"activate",
		"create window with default profile command " + quoted(AttachLine(a)),
		"end tell",
	}
}

func (i *ItermPlanner) Name() string { return "iterm" }

func (i *ItermPlanner) Plan(a agent.Attach) (agent.FocusPlan, error) {
	// AppleScript string literals have no \u escape, so other control characters cannot be passed on.
	for _, w := range append(slices.Clone(a.Argv), a.Dir) {
		for _, c := range w {
			if unicode.IsControl(c) && c != '\n' && c != '\t' && c != '\r' {
				return agent.FocusPlan{}, agent.LaunchError("could not open iTerm: the command contains control characters")
			}
		}
	}
	return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "applescript", Script: ItermScript(a)}}, nil
}
