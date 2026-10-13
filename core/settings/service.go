package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
)

// Messages the service gives.
const (
	InUse      = "That shortcut is in use"
	FileBroken = "settings.json has a syntax error; fix it or reset it"
	Whole      = "Enter a whole number"
	Empty      = "Can't be empty"
	unknown    = "Unknown setting"
	managed    = "This setting is managed by the app"
)

// Store is settings.json plus the config the app runs with.
type Store interface {
	// Current is the config the app runs with now.
	Current() Config
	// Load is settings.json as it is on disk: (nil, nil) when missing, an error when unreadable
	// or not valid settings JSON. Never writes.
	Load() (*Config, error)
	// Save writes c atomically and makes it current, keeping the live geometry
	// (Config.WithManagedFrom). On error nothing changed.
	Save(c Config) error
	// Adopt makes c (read from disk) current without writing it, keeping the live geometry.
	Adopt(c Config)
}

// Applier makes the running app follow a changed Live setting (P14 implements it; RouteFor
// says which app code path a field takes). It is called after the save, except for hotkey:
// that one is applied first, so a shortcut that is in use is never saved. It is called with
// the settings lock held: it must not call back into the API.
type Applier interface {
	Apply(field string, cfg Config) error
}

// SetKind tells why a set changed nothing.
type SetKind string

const (
	// SetInvalid: nothing was saved; each error belongs under its field.
	SetInvalid SetKind = "invalid"
	// SetSave: settings.json could not be written (or is broken); the old values stand.
	SetSave SetKind = "save"
)

// SetError is why a set changed nothing. It serializes as Rust's SetError:
// {"kind":"invalid","errors":[…]} or {"kind":"save","message":…}.
type SetError struct {
	Kind    SetKind
	Errors  []FieldError
	Message string
}

func (e *SetError) Error() string {
	if e.Kind == SetSave {
		return e.Message
	}
	var parts []string
	for _, fe := range e.Errors {
		parts = append(parts, fe.Path+": "+fe.Message)
	}
	return strings.Join(parts, "; ")
}

// MarshalJSON writes the tagged form.
func (e *SetError) MarshalJSON() ([]byte, error) {
	if e.Kind == SetSave {
		return json.Marshal(struct {
			Kind    SetKind `json:"kind"`
			Message string  `json:"message"`
		}{e.Kind, e.Message})
	}
	errs := e.Errors
	if errs == nil {
		errs = []FieldError{}
	}
	return json.Marshal(struct {
		Kind   SetKind      `json:"kind"`
		Errors []FieldError `json:"errors"`
	}{e.Kind, errs})
}

func invalid(path, message string) *SetError {
	return &SetError{Kind: SetInvalid, Errors: []FieldError{{path, message}}}
}

// Reload is what a reload found.
type Reload int

const (
	ReloadUnchanged Reload = iota
	ReloadChanged
	ReloadFileError
)

// Snapshot is what the Settings window shows.
type Snapshot struct {
	Platform Platform `json:"platform"`
	// Values is Config as JSON, without unknown keys. It holds no secrets.
	Values map[string]any `json:"values"`
	Fields []FieldView    `json:"fields"`
	// RestartPending are the Restart fields whose value differs from the one the app started with.
	RestartPending []string `json:"restartPending"`
	// FileError is why settings.json can't be read, while it can't.
	FileError *string `json:"fileError"`
	// Warning: the last change was saved but the running app could not follow all of it.
	Warning *string `json:"warning"`
	// Seq increases with every snapshot, so the window can drop one older than what it shows.
	Seq uint64 `json:"seq"`
	// SetError is set on the snapshot settings.set returns when the set changed nothing
	// (Rust returned it as the command's Err).
	SetError *SetError `json:"setError,omitempty"`
}

var snapshotSeq atomic.Uint64

// Service reads and writes settings over two boundaries: the Store and the Applier. It is not
// safe for concurrent use; API serializes calls.
type Service struct {
	store   Store
	applier Applier
	// startup is what the app started with: Restart fields that differ from it are pending.
	startup Config
	fileErr *string
	warning *string
	// reloadWarned: the last Reload produced warning; the save SetField makes right after keeps it.
	reloadWarned bool
	// refusedHotkey is a hand-edited hotkey that could not be bound: not retried until
	// settings.json changes it.
	refusedHotkey *string
}

// NewService takes the store's current config as the startup config.
func NewService(store Store, applier Applier) *Service {
	s := &Service{store: store, applier: applier, startup: store.Current()}
	// A settings.json that was broken at launch is still broken (Load left it alone): show the
	// banner and block saves from the first snapshot on, as Reload does.
	if _, err := store.Load(); err != nil {
		reason := err.Error()
		s.fileErr = &reason
	}
	return s
}

// Snapshot is the window's view now.
func (s *Service) Snapshot(env Env) Snapshot {
	current := s.store.Current()
	values := tree(current)
	// Unknown keys stay in the file but are not the window's business.
	for k := range current.Extra {
		if !knownKeys[k] {
			delete(values, k)
		}
	}
	return Snapshot{
		Platform:       env.Platform,
		Values:         values,
		Fields:         Views(env.Sources),
		RestartPending: s.RestartPending(),
		FileError:      clonePtr(s.fileErr),
		Warning:        clonePtr(s.warning),
		Seq:            snapshotSeq.Add(1),
	}
}

// RestartPending are the Restart fields that differ from the startup config.
func (s *Service) RestartPending() []string {
	now, then := tree(s.store.Current()), tree(s.startup)
	out := []string{}
	for _, f := range Fields {
		if f.Apply != ApplyRestart || f.Kind.Type == KindManaged {
			continue
		}
		a, _ := lookup(now, f.Path)
		b, _ := lookup(then, f.Path)
		if !reflect.DeepEqual(a, b) {
			out = append(out, f.Path)
		}
	}
	return out
}

// SetField parses value by the field's kind, applies it to a copy, validates, then saves and
// applies it (Live) or leaves it pending (Restart). A failure changes nothing and returns a
// *SetError. Call Reload first, so a hand edit made since the last look is merged rather than
// overwritten.
func (s *Service) SetField(env Env, path string, value json.RawMessage) error {
	v, err := decodeValue(value)
	if err != nil {
		return invalid(path, "Not a valid value for this setting")
	}
	if path == "position" {
		return s.resetPosition(v)
	}
	if s.fileErr != nil {
		return &SetError{Kind: SetSave, Message: FileBroken}
	}
	field := FieldByPath(path)
	if field == nil {
		return invalid(path, unknown)
	}
	if field.Kind.Type == KindManaged {
		return invalid(path, managed)
	}
	parsed, msg := parse(field.Kind, v)
	if msg != "" {
		return invalid(path, msg)
	}
	current := s.store.Current()
	candidate, msg := withValue(current, field, parsed)
	if msg != "" {
		return invalid(path, msg)
	}
	// Problems already in the saved config don't block other edits.
	before := Validate(current, env)
	var errs []FieldError
	for _, e := range Validate(candidate, env) {
		if e.Path == path || !containsError(before, e) {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		return &SetError{Kind: SetInvalid, Errors: errs}
	}
	if candidate.Equal(current) {
		return nil
	}
	hotkey := field.Kind.Type == KindHotkey
	if hotkey && s.applier.Apply(path, candidate) != nil {
		return invalid(path, InUse)
	}
	if err := s.store.Save(candidate); err != nil {
		if hotkey && s.applier.Apply(path, current) != nil {
			log.Printf("settings: could not restore %s", path)
		}
		return &SetError{Kind: SetSave, Message: err.Error()}
	}
	if s.reloadWarned {
		s.reloadWarned = false
	} else {
		s.warning = nil
	}
	if field.Apply == ApplyLive && !hotkey {
		if err := s.applier.Apply(path, candidate); err != nil {
			log.Printf("settings: could not apply %s live: %v", path, err)
			line := notApplied(field.Label, err.Error())
			if s.warning != nil {
				line = *s.warning + " " + line
			}
			s.warning = &line
		}
	}
	return nil
}

func containsError(list []FieldError, e FieldError) bool {
	for _, x := range list {
		if x == e {
			return true
		}
	}
	return false
}

// resetPosition: position = null runs the tray's Reset position; nothing else is allowed.
func (s *Service) resetPosition(v any) error {
	if v != nil {
		return invalid("position", managed)
	}
	if s.fileErr != nil {
		return &SetError{Kind: SetSave, Message: FileBroken}
	}
	if err := s.applier.Apply("position", s.store.Current()); err != nil {
		return invalid("position", err.Error())
	}
	return nil
}

// Reload picks up hand edits of settings.json. It never writes the file. A hotkey that can't
// be bound is not adopted: the old one stays, and the warning says so.
func (s *Service) Reload() Reload {
	s.reloadWarned = false
	hadError := s.fileErr != nil
	loaded, err := s.store.Load()
	if err != nil {
		reason := err.Error()
		if s.fileErr != nil && *s.fileErr == reason {
			return ReloadUnchanged
		}
		s.fileErr = &reason
		return ReloadFileError
	}
	s.fileErr = nil
	if loaded == nil {
		if hadError {
			return ReloadChanged
		}
		return ReloadUnchanged
	}
	current := s.store.Current()
	disk := loaded.Normalized().WithManagedFrom(current)
	var problems []string
	if disk.Hotkey != current.Hotkey {
		switch {
		case s.refusedHotkey != nil && *s.refusedHotkey == disk.Hotkey:
			disk.Hotkey = current.Hotkey
		case s.applier.Apply("hotkey", disk) != nil:
			log.Printf("settings: the hotkey %s from settings.json can't be bound", disk.Hotkey)
			problems = append(problems, fmt.Sprintf("The hotkey in settings.json (%s) can't be used; keeping %s.", disk.Hotkey, current.Hotkey))
			refused := disk.Hotkey
			s.refusedHotkey = &refused
			disk.Hotkey = current.Hotkey
		default:
			s.refusedHotkey = nil
		}
	} else {
		s.refusedHotkey = nil
	}
	if disk.Equal(current) && len(problems) == 0 {
		if hadError {
			return ReloadChanged
		}
		return ReloadUnchanged
	}
	s.store.Adopt(disk)
	old, next := tree(current), tree(disk)
	for _, path := range LivePaths() {
		if path == "hotkey" {
			continue
		}
		a, _ := lookup(old, path)
		b, _ := lookup(next, path)
		if reflect.DeepEqual(a, b) {
			continue
		}
		if err := s.applier.Apply(path, disk); err != nil {
			log.Printf("settings: could not apply %s from settings.json: %v", path, err)
			label := path
			if f := FieldByPath(path); f != nil {
				label = f.Label
			}
			problems = append(problems, notApplied(label, err.Error()))
		}
	}
	s.reloadWarned = len(problems) > 0
	if len(problems) == 0 {
		s.warning = nil
	} else {
		w := strings.Join(problems, " ")
		s.warning = &w
	}
	return ReloadChanged
}

func notApplied(label, reason string) string {
	return fmt.Sprintf("%s couldn't be applied now (%s); it applies after a restart.", label, reason)
}

func decodeValue(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// parse turns the window's value into the JSON the field stores. Ranges and choices are
// Validate's job. A non-empty message is the error.
func parse(kind Kind, value any) (any, string) {
	switch kind.Type {
	case KindBool:
		if b, ok := value.(bool); ok {
			return b, ""
		}
		return nil, "Expected on or off"
	case KindInt:
		switch v := value.(type) {
		case json.Number:
			if _, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
				return v, ""
			}
			if _, err := strconv.ParseUint(v.String(), 10, 64); err == nil {
				return v, ""
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return json.Number(strconv.FormatInt(n, 10)), ""
			}
		}
		return nil, Whole
	case KindChoice, KindHotkey:
		// "System Default" is sent as "" (or null) and stored as null.
		if kind.Type == KindChoice && kind.Choices.Nullable() {
			switch v := value.(type) {
			case nil:
				return nil, ""
			case string:
				if t := strings.TrimSpace(v); t != "" {
					return t, ""
				}
				return nil, ""
			}
			return nil, "Expected text"
		}
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s), ""
		}
		return nil, "Expected text"
	case KindMultiChoice:
		items, ok := value.([]any)
		if !ok {
			return nil, "Expected a list"
		}
		out := []any{}
		seen := map[string]bool{}
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, "Expected a list of names"
			}
			s = strings.TrimSpace(s)
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out, ""
	case KindText, KindPath:
		var text string
		switch v := value.(type) {
		case nil:
		case string:
			text = strings.TrimSpace(v)
		default:
			return nil, "Expected text"
		}
		switch {
		case text == "" && kind.Optional:
			return nil, ""
		case text == "":
			return nil, Empty
		}
		return text, ""
	}
	return nil, managed
}

// withValue is current with field set to value. A value the field's Go type can't hold (e.g.
// 70000 for a uint16, -1 for a uint64) gets the field's range message.
func withValue(current Config, field *FieldSpec, value any) (Config, string) {
	t := tree(current)
	if !assign(t, field.Path, value) {
		return Config{}, unknown
	}
	b, err := json.Marshal(t)
	if err == nil {
		var c Config
		if c, err = ParseConfig(b); err == nil {
			return c, ""
		}
	}
	if field.Kind.Type == KindInt {
		return Config{}, RangeMessage(field.Kind.Min, field.Kind.Max)
	}
	return Config{}, "Not a valid value for this setting"
}
