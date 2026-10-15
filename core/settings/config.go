// Package settings is settings.json: the config model, its schema for the Settings window, the
// save-time rules, and the service that reads and writes it. Ported from ai-mascot
// src-tauri/src/config.rs and src-tauri/src/settings/*.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
)

// MaxScale is the largest window scale. A hand-edited huge value would give an oversized window.
const MaxScale = 16

// SizePreset is one Tray ▸ Size entry.
type SizePreset struct {
	Label string
	Scale uint32
}

// SizePresets are the tray's sizes (Rust tray::SIZES); a custom scale shows as "Custom (×N)".
var SizePresets = []SizePreset{{"Small", 4}, {"Medium", 6}, {"Large", 8}, {"Huge", 12}}

// Config is settings.json. Optional values are pointers (nil = null).
type Config struct {
	Hotkey string `json:"hotkey"`
	Pack   string `json:"pack"`
	Scale  uint32 `json:"scale"`
	// Position is the logical top-left of the pixie window; nil = bottom-right of the primary
	// display. Managed: written by the app.
	Position      *[2]float64          `json:"position"`
	ProximityFade Fade                 `json:"proximityFade"`
	Voice         VoiceSettings        `json:"voice"`
	Canvas        CanvasSettings       `json:"canvas"`
	Agent         AgentSettings        `json:"agent"`
	Notifications NotificationSettings `json:"notifications"`
	// Extra holds top-level keys this version doesn't know, kept so a save never drops them.
	Extra map[string]json.RawMessage `json:"-"`
}

// Fade is proximityFade.
type Fade struct {
	Enabled bool `json:"enabled"`
	// Radius is logical pixels from the window edge where fading starts.
	Radius float64 `json:"radius"`
}

// VoiceSettings is the voice block.
type VoiceSettings struct {
	Provider        string  `json:"provider"`
	Audio           string  `json:"audio"`
	Model           string  `json:"model"`
	Voice           string  `json:"voice"`
	Chimes          bool    `json:"chimes"`
	IdleTimeoutSecs uint64  `json:"idleTimeoutSecs"`
	PersonaPath     *string `json:"personaPath"`
	NoiseFilter     bool    `json:"noiseFilter"`
	NoiseModel      string  `json:"noiseModel"`
	NoisyRoom       bool    `json:"noisyRoom"`
	// LiveBackendModel is the Responses model that runs Pixie's tools on OpenAI GPT-Live.
	LiveBackendModel string  `json:"liveBackendModel"`
	InputDevice      *string `json:"inputDevice"`
	OutputDevice     *string `json:"outputDevice"`
	HomeCity         *string `json:"homeCity"`
	// HandOffWhenUnsure: when Pixie answers a request with "I don't know" and calls no tool, the
	// app hands the request to the agent itself (#71).
	HandOffWhenUnsure bool `json:"handOffWhenUnsure"`
	// CheckClaims: when Pixie says it did something (set a timer, handed a task off) without
	// calling the tool, the app corrects it (#72).
	CheckClaims bool `json:"checkClaims"`
}

// NotificationSettings is the notifications block: how agent updates reach you.
type NotificationSettings struct {
	// DebounceSecs: a job's worker reports and progress go out once it has been quiet this long
	// (at most 1.5× this); 0 lets each one out as it comes.
	DebounceSecs uint64         `json:"debounceSecs"`
	Bubble       BubbleSettings `json:"bubble"`
	// AwayAfterMins: this long with no keyboard or mouse input counts as away.
	AwayAfterMins uint64 `json:"awayAfterMins"`
}

// BubbleSettings is notifications.bubble: the updates bubble beside Pixie.
type BubbleSettings struct {
	Enabled bool   `json:"enabled"`
	PopSecs uint64 `json:"popSecs"`
	Lines   uint64 `json:"lines"`
}

// CanvasSettings is the canvas block.
type CanvasSettings struct {
	AutoOpen bool   `json:"autoOpen"`
	Keep     uint64 `json:"keep"`
	// Window is the last canvas window geometry; nil = centred default size. Managed.
	Window *CanvasWindow `json:"window"`
}

// CanvasWindow is a canvas window frame in logical pixels.
type CanvasWindow struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// AgentSettings is the agent block.
type AgentSettings struct {
	Enabled     bool    `json:"enabled"`
	SessionHost string  `json:"sessionHost"`
	Session     string  `json:"session"`
	Terminal    string  `json:"terminal"`
	HerdrPath   *string `json:"herdrPath"`
	// TmuxPath is the tmux program for sessionHost "tmux" (nil finds it).
	TmuxPath              *string `json:"tmuxPath"`
	Model                 *string `json:"model"`
	Warm                  bool    `json:"warm"`
	Port                  uint16  `json:"port"`
	KeepPanesOnQuit       bool    `json:"keepPanesOnQuit"`
	HoldOpenSecs          uint64  `json:"holdOpenSecs"`
	PermissionTimeoutSecs uint64  `json:"permissionTimeoutSecs"`
	NotifyPerMinute       uint64  `json:"notifyPerMinute"`
	Narration             bool    `json:"narration"`
	// OpenConversationForUpdates: a job finishing or failing, an agent question or a spoken
	// worker report that comes while no conversation is open makes Pixie open one and say it
	// (off by default: a feature flag).
	OpenConversationForUpdates bool           `json:"openConversationForUpdates"`
	ProjectsRoot               string         `json:"projectsRoot"`
	Workers                    WorkerSettings `json:"workers"`
}

// WorkerSettings is agent.workers.
type WorkerSettings struct {
	Enabled bool     `json:"enabled"`
	Max     uint64   `json:"max"`
	Kinds   []string `json:"kinds"`
	// SpeakResults: Pixie says a worker's own done/failed/blocked report as soon as it comes
	// (off by default: a feature flag).
	SpeakResults bool `json:"speakResults"`
}

// Default is the config with every default (spec: inventory "settings.json schema").
func Default() Config {
	return Config{
		Hotkey:        "ctrl+alt+Space",
		Pack:          "bit",
		Scale:         8,
		ProximityFade: Fade{Enabled: true, Radius: 150},
		Voice: VoiceSettings{
			Provider: "openai-realtime", Audio: "macos-vpio", Model: "gpt-realtime", Voice: "marin",
			Chimes: true, IdleTimeoutSecs: 7, NoiseModel: "hush", LiveBackendModel: "gpt-6-luna",
			HandOffWhenUnsure: true, CheckClaims: true,
		},
		Canvas:        CanvasSettings{AutoOpen: true, Keep: 50},
		Notifications: NotificationSettings{DebounceSecs: 20, Bubble: BubbleSettings{Enabled: true, PopSecs: 6, Lines: 5}, AwayAfterMins: 5},
		Agent: AgentSettings{
			Enabled: true, SessionHost: "herdr", Session: "deskpixie", Terminal: "warp",
			Port: 48217, HoldOpenSecs: 180, PermissionTimeoutSecs: 120, NotifyPerMinute: 4,
			Narration: true, ProjectsRoot: "~/Documents/Projects",
			// Handed-off work keeps running when the app quits; the next start adopts it.
			KeepPanesOnQuit: true,
			Workers:         WorkerSettings{Max: 4, Kinds: []string{"claude", "codex"}},
		},
	}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Clone is a deep copy.
func (c Config) Clone() Config {
	out := c
	out.Position = clonePtr(c.Position)
	out.Voice.PersonaPath = clonePtr(c.Voice.PersonaPath)
	out.Voice.InputDevice = clonePtr(c.Voice.InputDevice)
	out.Voice.OutputDevice = clonePtr(c.Voice.OutputDevice)
	out.Voice.HomeCity = clonePtr(c.Voice.HomeCity)
	out.Canvas.Window = clonePtr(c.Canvas.Window)
	out.Agent.HerdrPath = clonePtr(c.Agent.HerdrPath)
	out.Agent.TmuxPath = clonePtr(c.Agent.TmuxPath)
	out.Agent.Model = clonePtr(c.Agent.Model)
	if c.Agent.Workers.Kinds != nil {
		out.Agent.Workers.Kinds = append([]string{}, c.Agent.Workers.Kinds...)
	}
	if c.Extra != nil {
		out.Extra = make(map[string]json.RawMessage, len(c.Extra))
		for k, v := range c.Extra {
			out.Extra[k] = append(json.RawMessage{}, v...)
		}
	}
	return out
}

// Equal compares as JSON values do (Rust's derived PartialEq over serde types).
func (c Config) Equal(o Config) bool {
	return reflect.DeepEqual(tree(c), tree(o))
}

// configFields is Config without Extra or custom marshalling.
type configFields struct {
	Hotkey        string               `json:"hotkey"`
	Pack          string               `json:"pack"`
	Scale         uint32               `json:"scale"`
	Position      *[2]float64          `json:"position"`
	ProximityFade Fade                 `json:"proximityFade"`
	Voice         VoiceSettings        `json:"voice"`
	Canvas        CanvasSettings       `json:"canvas"`
	Agent         AgentSettings        `json:"agent"`
	Notifications NotificationSettings `json:"notifications"`
}

var knownKeys = map[string]bool{
	"hotkey": true, "pack": true, "scale": true, "position": true, "proximityFade": true,
	"voice": true, "canvas": true, "agent": true, "notifications": true,
}

// MarshalJSON writes the known fields in declaration order, then unknown keys (sorted).
func (c Config) MarshalJSON() ([]byte, error) {
	kinds := c.Agent.Workers.Kinds
	if kinds == nil {
		kinds = []string{}
	}
	f := configFields{c.Hotkey, c.Pack, c.Scale, c.Position, c.ProximityFade, c.Voice, c.Canvas, c.Agent, c.Notifications}
	f.Agent.Workers.Kinds = kinds
	b, err := json.Marshal(f)
	if err != nil || len(c.Extra) == 0 {
		return b, err
	}
	names := make([]string, 0, len(c.Extra))
	for k := range c.Extra {
		if !knownKeys[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	for _, k := range names {
		key, _ := json.Marshal(k)
		buf.WriteByte(',')
		buf.Write(key)
		buf.WriteByte(':')
		if err := json.Compact(&buf, c.Extra[k]); err != nil {
			return nil, fmt.Errorf("settings: unknown key %s: %w", k, err)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON fills c's fields from the JSON object, leaving fields the JSON lacks as they
// are (start from Default for serde's `#[serde(default)]`). Top-level keys match exactly, like
// serde; unknown ones go to Extra. Nested blocks use encoding/json's case-insensitive matching
// (serde would drop a differently cased nested key; accepted deviation).
func (c *Config) UnmarshalJSON(b []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	if top == nil {
		return errors.New("invalid type: null, expected struct Config")
	}
	targets := map[string]any{
		"hotkey": &c.Hotkey, "pack": &c.Pack, "scale": &c.Scale, "position": &c.Position,
		"proximityFade": &c.ProximityFade, "voice": &c.Voice, "canvas": &c.Canvas, "agent": &c.Agent,
		"notifications": &c.Notifications,
	}
	c.Extra = nil
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := top[k]
		target, ok := targets[k]
		if !ok {
			if c.Extra == nil {
				c.Extra = map[string]json.RawMessage{}
			}
			c.Extra[k] = append(json.RawMessage{}, v...)
			continue
		}
		if err := json.Unmarshal(v, target); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// ParseConfig reads settings.json text; absent fields get their defaults.
func ParseConfig(b []byte) (Config, error) {
	c := Default()
	if err := json.Unmarshal(b, &c); err != nil {
		return Default(), withLine(b, err)
	}
	return c, nil
}

// withLine adds "at line L column C" to a syntax error, as serde_json's messages have.
func withLine(b []byte, err error) error {
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		return err
	}
	off := int(syn.Offset)
	if off > len(b) {
		off = len(b)
	}
	line, col := 1, 0
	for _, ch := range b[:off] {
		if ch == '\n' {
			line, col = line+1, 0
		} else {
			col++
		}
	}
	return fmt.Errorf("%v at line %d column %d", err, line, col)
}

// Load reads settings.json and never fails: a missing file gives the defaults, written so the
// user can edit them. A file that exists but can't be read or parsed also gives the defaults,
// but is left untouched (Rust rewrote it, losing every setting over one typo): the settings
// service reports it as broken and blocks saves until it is fixed or reset.
func Load(path string) Config {
	c, _ := LoadFile(path)
	return c
}

// LoadFile is Load plus why settings.json could not be used (nil when it was, or was missing).
func LoadFile(path string) (Config, error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c := Default()
		if err := c.Save(path); err != nil {
			log.Printf("settings: could not write default settings to %s: %v", path, err)
		}
		return c, nil
	}
	if err == nil {
		c, perr := ParseConfig(text)
		if perr == nil {
			return c.Normalized(), nil
		}
		err = perr
	}
	log.Printf("settings: %s can't be used, running with defaults and leaving it as is: %v", path, err)
	return Default(), err
}

// Normalized applies load's lenient fixes: a zero scale becomes the default, a huge one is
// capped, and a blank optional path or home city means "not set" (the city is trimmed).
func (c Config) Normalized() Config {
	out := c.Clone()
	if out.Scale == 0 {
		out.Scale = Default().Scale
	}
	if out.Scale > MaxScale {
		out.Scale = MaxScale
	}
	blankToNil(&out.Voice.PersonaPath)
	if out.Voice.HomeCity != nil {
		city := strings.TrimSpace(*out.Voice.HomeCity)
		out.Voice.HomeCity = &city
		blankToNil(&out.Voice.HomeCity)
	}
	blankToNil(&out.Agent.HerdrPath)
	blankToNil(&out.Agent.TmuxPath)
	return out
}

func blankToNil(p **string) {
	if *p != nil && strings.TrimSpace(**p) == "" {
		*p = nil
	}
}

// WithManagedFrom is c with the geometry the app writes itself (position, canvas.window)
// taken from live, so a settings write never undoes a drag that landed in between.
func (c Config) WithManagedFrom(live Config) Config {
	out := c.Clone()
	out.Position = clonePtr(live.Position)
	out.Canvas.Window = clonePtr(live.Canvas.Window)
	return out
}

var saveSeq atomic.Uint64

// Save writes a temporary file in the same folder, then renames it over path, keeping the
// file's permissions, so a crash mid-write never leaves half a settings.json. A symlinked
// settings.json stays a symlink: its target is what gets replaced.
func (c Config) Save(path string) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	text, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(target), os.Getpid(), saveSeq.Add(1)-1))
	if err := writeThenRename(text, tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// writeThenRename writes text to tmp (with path's permissions when it exists), syncs it, then
// renames it over path.
func writeThenRename(text []byte, tmp, path string) error {
	mode := fs.FileMode(0o644)
	st, statErr := os.Stat(path)
	if statErr == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(text); err != nil {
		f.Close()
		return err
	}
	if statErr == nil {
		// The umask may have narrowed the mode given at creation.
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// tree is c as a JSON value (numbers as json.Number), including unknown keys.
func tree(c Config) map[string]any {
	b, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("settings: config serializes: %v", err))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		panic(fmt.Sprintf("settings: config decodes: %v", err))
	}
	return m
}

// lookup follows a dotted path (e.g. agent.workers.max) into a JSON tree.
func lookup(v any, path string) (any, bool) {
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[part]; !ok {
			return nil, false
		}
	}
	return v, true
}

// assign sets the value at a dotted path that already exists.
func assign(v map[string]any, path string, value any) bool {
	parts := strings.Split(path, ".")
	m := v
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			return false
		}
		m = next
	}
	last := parts[len(parts)-1]
	if _, ok := m[last]; !ok {
		return false
	}
	m[last] = value
	return true
}
