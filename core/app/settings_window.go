package app

import (
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
)

// coordinatorTools are the local tools the coordinator gets: Pixie's (the same instances), and
// open_settings, which is the coordinator's alone (#288): Pixie hands settings questions to it.
func (a *App) coordinatorTools() []tools.Tool {
	return append(append([]tools.Tool{}, a.localTools...), tools.OpenSettings{Window: settingsWindow{a}})
}

// settingsWindow is open_settings' window (#288): the settings search, and the settings.open
// event that has Swift show the window at a setting.
type settingsWindow struct{ a *App }

func (w settingsWindow) Find(query string) []tools.SettingMatch {
	var out []tools.SettingMatch
	for _, h := range w.a.api.Search(query) {
		out = append(out, tools.SettingMatch{Path: h.Path, Label: h.Label, Page: h.Section.Title()})
	}
	return out
}

// Open emits settings.open: with a match, its hit (path, section, label, help), which the
// window reveals like a picked search result; without, no fields.
func (w settingsWindow) Open(m *tools.SettingMatch) {
	if m != nil {
		if h, ok := settings.HitFor(m.Path); ok {
			w.a.emit("settings.open", map[string]any{"path": h.Path, "section": h.Section, "group": h.Group, "label": h.Label, "help": h.Help})
			return
		}
	}
	w.a.emit("settings.open", nil)
}
