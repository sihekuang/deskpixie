package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sync"
)

// Registrar is the bridge's dp_call registry (core/bridge implements it; P14 wires it).
type Registrar interface {
	Register(method string, fn func(args json.RawMessage) (any, error))
}

// SecretSource says where each API key comes from; it never hands out a key.
type SecretSource interface {
	// Source names where the key for a provider registry name ("openai-realtime",
	// "gemini-live") comes from: "env", "keychain" or "dotenv"; "" when there is none.
	Source(provider string) string
}

// KeyStatus is found or missing; never the key.
type KeyStatus string

const (
	KeyFound   KeyStatus = "found"
	KeyMissing KeyStatus = "missing"
)

// KeySources is where each key comes from: "env", "keychain", "dotenv", or "" (missing).
type KeySources struct {
	OpenAI string `json:"openai"`
	Gemini string `json:"gemini"`
}

// Secrets is which API keys exist and where they come from.
type Secrets struct {
	OpenAI  KeyStatus  `json:"openai"`
	Gemini  KeyStatus  `json:"gemini"`
	Sources KeySources `json:"sources"`
}

// SecretStatus is found or missing, and the source, per provider; a nil source has none.
func SecretStatus(src SecretSource) Secrets {
	source := func(provider string) string {
		if src == nil {
			return ""
		}
		return src.Source(provider)
	}
	status := func(s string) KeyStatus {
		if s == "" {
			return KeyMissing
		}
		return KeyFound
	}
	o, g := source(ProviderOpenAI), source(ProviderGemini)
	return Secrets{OpenAI: status(o), Gemini: status(g), Sources: KeySources{OpenAI: o, Gemini: g}}
}

// RevealCommand is how each platform shows a file in its file manager.
func RevealCommand(p Platform, file string) (string, []string) {
	switch p {
	case Macos:
		return "open", []string{"-R", file}
	case Windows:
		return "explorer", []string{"/select," + file}
	}
	return "xdg-open", []string{filepath.Dir(file)}
}

// APIDeps are what the dp_call methods need.
type APIDeps struct {
	Store   Store
	Applier Applier
	// Env is the home folder, platform, loaded packs and registry names. Device lists come
	// from settings.choices and are merged in by API.Env.
	Env          func() Env
	Secrets      SecretSource
	SettingsPath string
	// Platform picks the reveal command; zero means CurrentPlatform.
	Platform Platform
	// Restart quits through the normal quit path and relaunches (P14: the `restart` event).
	Restart func()
	// Run starts program and returns once it started; nil runs it with os/exec, reaping it in
	// the background.
	Run func(program string, args []string) error
}

// API is the settings.* dp_call methods over one Service. Calls are serialized; the lock order
// is the API lock, then the store's.
type API struct {
	mu      sync.Mutex
	deps    APIDeps
	svc     *Service
	devices map[string][]Pair
}

// NewAPI builds the service; the store's current config is the startup config.
func NewAPI(d APIDeps) *API {
	if d.Platform == "" {
		d.Platform = CurrentPlatform()
	}
	if d.Run == nil {
		d.Run = startAndReap
	}
	if d.Env == nil {
		d.Env = func() Env { return Env{Platform: d.Platform} }
	}
	return &API{deps: d, svc: NewService(d.Store, d.Applier), devices: map[string][]Pair{}}
}

func startAndReap(program string, args []string) error {
	cmd := exec.Command(program, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("settings: %s did not finish: %v", program, err)
		}
	}()
	return nil
}

// Env is the base env with the device lists from settings.choices, each plus the saved device
// when it isn't connected. Read fresh on every call.
func (a *API) Env() Env {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.env()
}

func (a *API) env() Env {
	env := a.deps.Env()
	cur := a.deps.Store.Current()
	env.Sources.Inputs = WithSaved(a.devices["voice.inputDevice"], cur.Voice.InputDevice, MissingMic)
	env.Sources.Outputs = WithSaved(a.devices["voice.outputDevice"], cur.Voice.OutputDevice, MissingSpeaker)
	return env
}

// Snapshot picks up any hand edit of settings.json, then gives the current values.
func (a *API) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.svc.Reload()
	return a.svc.Snapshot(a.env())
}

// Reload picks up hand edits (the window got focus). changed is false when nothing on disk
// changed since the last look.
func (a *API) Reload() (Snapshot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := a.svc.Reload() != ReloadUnchanged
	return a.svc.Snapshot(a.env()), changed
}

// Set merges any hand edit, then saves one field. The snapshot carries SetError when nothing
// was saved.
func (a *API) Set(field string, value json.RawMessage) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.svc.Reload()
	env := a.env()
	err := a.svc.SetField(env, field, value)
	snap := a.svc.Snapshot(a.env())
	var se *SetError
	if errors.As(err, &se) {
		snap.SetError = se
	} else if err != nil {
		snap.SetError = &SetError{Kind: SetSave, Message: err.Error()}
	}
	return snap
}

// SetChoices records the options Swift found for a device field (voice.inputDevice or
// voice.outputDevice).
func (a *API) SetChoices(field string, choices []Pair) error {
	if field != "voice.inputDevice" && field != "voice.outputDevice" {
		return fmt.Errorf("settings.choices: %s takes no runtime choices", field)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.devices[field] = append([]Pair{}, choices...)
	return nil
}

// Secrets is which API keys exist.
func (a *API) Secrets() Secrets { return SecretStatus(a.deps.Secrets) }

// Search is the settings and extra rows matching query, for this platform and the current
// values (Search).
func (a *API) Search(query string) []Hit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Search(query, a.deps.Platform, a.deps.Store.Current())
}

// OpenFile shows settings.json in the file manager.
func (a *API) OpenFile() error {
	program, args := RevealCommand(a.deps.Platform, a.deps.SettingsPath)
	if err := a.deps.Run(program, args); err != nil {
		log.Printf("settings: could not run %s: %v", program, err)
		return errors.New("Couldn't open the settings file")
	}
	return nil
}

// Restart quits and relaunches.
func (a *API) Restart() {
	if a.deps.Restart != nil {
		a.deps.Restart()
	}
}

// Register adds the settings.* methods: snapshot, set, reload, secrets, choices (the shared
// contract), search (the window's search field) and openFile, restart (the window's Open Settings File and Restart buttons).
func (a *API) Register(r Registrar) {
	r.Register("settings.snapshot", func(json.RawMessage) (any, error) { return a.Snapshot(), nil })
	r.Register("settings.reload", func(json.RawMessage) (any, error) {
		snap, _ := a.Reload()
		return snap, nil
	})
	r.Register("settings.set", func(args json.RawMessage) (any, error) {
		var in struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("settings.set: %w", err)
		}
		if in.Field == "" {
			return nil, errors.New("settings.set: missing field")
		}
		if in.Value == nil {
			in.Value = json.RawMessage("null")
		}
		return a.Set(in.Field, in.Value), nil
	})
	r.Register("settings.secrets", func(json.RawMessage) (any, error) { return a.Secrets(), nil })
	r.Register("settings.choices", func(args json.RawMessage) (any, error) {
		var in struct {
			Field   string `json:"field"`
			Choices []Pair `json:"choices"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("settings.choices: %w", err)
		}
		if err := a.SetChoices(in.Field, in.Choices); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	})
	r.Register("settings.search", func(args json.RawMessage) (any, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("settings.search: %w", err)
		}
		return a.Search(in.Query), nil
	})
	r.Register("settings.openFile", func(json.RawMessage) (any, error) {
		if err := a.OpenFile(); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	})
	r.Register("settings.restart", func(json.RawMessage) (any, error) {
		a.Restart()
		return struct{}{}, nil
	})
}
