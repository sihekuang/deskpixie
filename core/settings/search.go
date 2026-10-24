package settings

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// Extra is a row of the Settings window that isn't a setting: a button, an API key, the pix
// command. The window draws it (mac: SettingsLayout.extras) and finds it by ID; it is here so
// the search finds it like a field. Label and Help are the window's, word for word.
type Extra struct {
	ID      string
	Section Section
	Label   string
	Help    string
	// Title is the row's button title ("Export Logs…"); search matches it like the help.
	Title     string
	Platforms []Platform
}

// Extras are the window's extra rows, in page order within each section.
var Extras = []Extra{
	{"resetPosition", SectionPixie, "Reset position", "Move Pixie back to the bottom-right corner of the main screen.", "Reset Position", AllPlatforms},
	{"key.openai", SectionVoice, "OpenAI key", "Type the key here to keep it in the Keychain (used first), or set OPENAI_API_KEY in your environment or a .env file. Keys are never shown here.", "", AllPlatforms},
	{"key.gemini", SectionVoice, "Gemini key", "Type the key here to keep it in the Keychain (used first), or set GEMINI_API_KEY in your environment or a .env file. Keys are never shown here.", "", AllPlatforms},
	{"pix", SectionAgent, "pix command", "Start Claude Code or Codex with `pix claude` or `pix codex` in any terminal (WebStorm, iTerm, …) and Pixie can see, message and interrupt that session. Quitting DeskPixie never ends it.", "", AllPlatforms},
	{"restartCoordinator", SectionAgent, "Restart coordinator", "Stops the coordinator and starts it again on your next request, with its conversation. Asks first.", "Restart Coordinator…", AllPlatforms},
	{"openFile", SectionAdvanced, "Settings file", "settings.json holds every setting on this page.", "Open Settings File", AllPlatforms},
	{"exportLogs", SectionAdvanced, "Logs", "Saves the app's log, pix's log, the voice log and metrics in one zip to share. It can include what you said to Pixie.", "Export Logs…", AllPlatforms},
}

// Hit is one search result: a setting (Path is its dotted path) or an extra row (Path is
// "extra:" and its ID).
type Hit struct {
	Path    string  `json:"path"`
	Section Section `json:"section"`
	Label   string  `json:"label"`
	Help    string  `json:"help"`
}

// ExtraPrefix starts a Hit's Path when the hit is an extra row.
const ExtraPrefix = "extra:"

// stopWords are left out of a query, so "where do I change the microphone" finds Microphone.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "for": true, "in": true, "on": true,
	"my": true, "i": true, "do": true, "how": true, "where": true, "what": true, "is": true,
	"can": true, "change": true, "set": true, "setting": true, "settings": true, "option": true,
	"options": true, "pixie": true, "pixie's": true, "show": true, "me": true, "find": true,
}

// words is s lowercased and split into runs of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// queryWords are the query's words without stop words; all stop words keeps them all.
func queryWords(query string) []string {
	all := words(query)
	var out []string
	for _, w := range all {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func anyPrefix(ws []string, q string) bool {
	for _, w := range ws {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}

// score is how well label and help match the query's words: 0 unless every word starts a word
// of either. A word in the label counts more than one in the help, and the whole query in the
// label more still.
func score(qs []string, phrase, label, help string) int {
	lw, hw := words(label), words(help)
	total := 0
	for _, q := range qs {
		switch {
		case anyPrefix(lw, q):
			total += 3
		case anyPrefix(hw, q):
			total++
		default:
			return 0
		}
	}
	if phrase != "" && strings.Contains(strings.Join(lw, " "), phrase) {
		total += 5
	}
	return total
}

// Visible reports whether f shows for the config tree values (tree(c)): every VisibleWhen
// condition holds, comparing values as the window shows them.
func Visible(f FieldSpec, values map[string]any) bool {
	for _, c := range f.VisibleWhen {
		v, _ := lookup(values, c.Key)
		shown := ""
		switch x := v.(type) {
		case string:
			shown = x
		case bool, json.Number, float64:
			b, _ := json.Marshal(x)
			shown = string(b)
		}
		found := false
		for _, in := range c.In {
			found = found || in == shown
		}
		if !found {
			return false
		}
	}
	return true
}

func onPlatform(ps []Platform, p Platform) bool {
	for _, x := range ps {
		if x == p {
			return true
		}
	}
	return false
}

// Search is the settings and extra rows that match query on platform p, best first, then in
// page order. A field hidden for c (VisibleWhen) or written by the app is left out. An empty
// query matches nothing.
func Search(query string, p Platform, c Config) []Hit {
	qs := queryWords(query)
	if len(qs) == 0 {
		return []Hit{}
	}
	phrase := strings.Join(qs, " ")
	values := tree(c)
	type scored struct {
		hit   Hit
		score int
	}
	var found []scored
	for _, f := range Fields {
		if f.Kind.Type == KindManaged || !onPlatform(f.Platforms, p) || !Visible(f, values) {
			continue
		}
		if s := score(qs, phrase, f.Label, f.Help); s > 0 {
			found = append(found, scored{Hit{f.Path, f.Section, f.Label, f.Help}, s})
		}
	}
	for _, e := range Extras {
		if !onPlatform(e.Platforms, p) {
			continue
		}
		if s := score(qs, phrase, e.Label, e.Help+" "+e.Title); s > 0 {
			found = append(found, scored{Hit{ExtraPrefix + e.ID, e.Section, e.Label, e.Help}, s})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score > found[j].score })
	out := []Hit{}
	for _, s := range found {
		out = append(out, s.hit)
	}
	return out
}
