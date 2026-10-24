package richtext

import (
	"net/url"
	"strconv"
)

// Editors a path can open in (agent.editor).
const (
	EditorAuto     = "auto" // the IDE the session runs in, else the file's default app
	EditorVSCode   = "vscode"
	EditorCursor   = "cursor"
	EditorWindsurf = "windsurf"
	EditorZed      = "zed"
	EditorDefault  = "default" // the file's default app
)

// Editors is every editor choice.
var Editors = []string{EditorAuto, EditorVSCode, EditorCursor, EditorWindsurf, EditorZed, EditorDefault}

// ideSchemes are the editors a session can run in (its terminal app's bundle id), by the URL
// scheme that opens a file in them. JetBrains IDEs each have their own scheme.
var ideSchemes = map[string]string{
	"com.microsoft.VSCode":          EditorVSCode,
	"com.microsoft.VSCodeInsiders":  "vscode-insiders",
	"com.todesktop.230313mzl4w4u92": EditorCursor,
	"com.exafunction.windsurf":      EditorWindsurf,
	"dev.zed.Zed":                   EditorZed,
	"com.jetbrains.intellij":        "idea",
	"com.jetbrains.intellij.ce":     "idea",
	"com.jetbrains.WebStorm":        "webstorm",
	"com.jetbrains.goland":          "goland",
	"com.jetbrains.pycharm":         "pycharm",
	"com.jetbrains.pycharm.ce":      "pycharm",
	"com.jetbrains.PhpStorm":        "phpstorm",
	"com.jetbrains.rubymine":        "rubymine",
	"com.jetbrains.CLion":           "clion",
	"com.jetbrains.rider":           "rider",
}

// OpenPlan is how a path opens: addresses for the shell to try in order, the first it has an
// app for wins. A file opens at its line in the chosen editor (editor), or with EditorAuto in
// the IDE its session runs in (app); the file's default app is always last. A folder opens in
// the file manager.
func OpenPlan(path string, line, col int, dir bool, app, editor string) []string {
	plain := (&url.URL{Scheme: "file", Path: path}).String()
	if dir {
		return []string{plain}
	}
	scheme := editor
	switch editor {
	case EditorAuto:
		scheme = ideSchemes[app]
	case EditorDefault:
		scheme = ""
	}
	if scheme == "" {
		return []string{plain}
	}
	return []string{editorURL(scheme, path, line, col), plain}
}

// editorURL opens path at line and col in the editor that handles scheme.
func editorURL(scheme, path string, line, col int) string {
	switch scheme {
	case EditorVSCode, "vscode-insiders", EditorCursor, EditorWindsurf, EditorZed:
		at := path
		if line > 0 {
			at += ":" + strconv.Itoa(line)
			if col > 0 {
				at += ":" + strconv.Itoa(col)
			}
		}
		return (&url.URL{Scheme: scheme, Host: "file", Path: at}).String()
	}
	// JetBrains IDEs
	q := url.Values{"file": {path}}
	if line > 0 {
		q.Set("line", strconv.Itoa(line))
		if col > 0 {
			q.Set("column", strconv.Itoa(col))
		}
	}
	return scheme + "://open?" + q.Encode()
}
