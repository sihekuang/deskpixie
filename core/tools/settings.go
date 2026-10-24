package tools

import (
	"context"
	"encoding/json"
)

// SettingMatch is a setting the settings search found: its path, its name and the Settings
// page it is on, as the window shows them.
type SettingMatch struct {
	Path  string
	Label string
	Page  string
}

// SettingsWindow is what open_settings needs of the app: the settings search and the window.
type SettingsWindow interface {
	// Find is what query matches, best first.
	Find(query string) []SettingMatch
	// Open shows the Settings window; with a match, at that setting, pointed out.
	Open(m *SettingMatch)
}

// MaxOtherSettings is the most other matches open_settings names besides the one it shows.
const MaxOtherSettings = 3

// OpenSettings opens DeskPixie's Settings window, at the setting the user asked about (#288).
type OpenSettings struct{ Window SettingsWindow }

func (OpenSettings) Name() string { return "open_settings" }

func (OpenSettings) Description() string {
	return "Open DeskPixie's Settings window, at the setting the user asks about (\"where do I change the " +
		"microphone\", \"show me the noise filter setting\", \"open settings\"). Pass the setting in a few " +
		"words as query, or leave it out to just open Settings. It shows the setting and points it out; " +
		"it changes nothing. Then tell the user which page it is on in one short sentence."
}

func (OpenSettings) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"query":{"type":"string","description":"The setting in a few words, e.g. \"microphone\", \"noise filter\", \"gemini key\"."}},` +
		`"additionalProperties":false}`)
}

func (o OpenSettings) Invoke(_ context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	query, asked := parseArgs(raw).text("query")
	if !asked {
		o.Window.Open(nil)
		return reply(map[string]any{"opened": true})
	}
	found := o.Window.Find(query)
	if len(found) == 0 {
		o.Window.Open(nil)
		return reply(map[string]any{"opened": true, "found": false,
			"note": "No setting matches; Settings is open on its last page."})
	}
	best := found[0]
	o.Window.Open(&best)
	out := map[string]any{"opened": true, "found": true, "setting": best.Label, "page": best.Page}
	var others []string
	for _, m := range found[1:] {
		if len(others) == MaxOtherSettings {
			break
		}
		others = append(others, m.Label+" ("+m.Page+")")
	}
	if len(others) > 0 {
		out["others"] = others
	}
	return reply(out)
}
