package app

import (
	"os"

	"github.com/sihekuang/deskpixie/core/richtext"
)

// textSpans splits a message shown in Pixie's bubbles into what can be clicked (#277). Paths
// resolve against the folders of the sessions it is about, in order, and open in the editor
// chosen (agent.editor) or the one the first of them runs in; a path that would launch or run
// something has no plan: a click shows it in the file manager.
func (a *App) textSpans(text string, sessions []string) []richtext.Span {
	if text == "" {
		return []richtext.Span{}
	}
	r := richtext.Resolver{}
	r.Home, _ = os.UserHomeDir()
	app := ""
	if a.agent != nil {
		for _, p := range a.agent.SessionPlaces(sessions) {
			if p.Folder != "" {
				r.Folders = append(r.Folders, p.Folder)
			}
			if app == "" {
				app = p.App
			}
		}
	}
	editor := a.store.Current().Agent.Editor
	spans := richtext.Parse(text, r)
	for i, s := range spans {
		if s.Kind == richtext.KindPath && !s.Reveal {
			spans[i].Open = richtext.OpenPlan(s.Path, s.Line, s.Col, s.Dir, app, editor)
		}
	}
	return spans
}
