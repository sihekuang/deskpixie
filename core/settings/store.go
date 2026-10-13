package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sync"
)

// ReadConfig is settings.json as it is on disk: (nil, nil) when there is none, an error
// (with line and column for a syntax error) when it can't be read or isn't valid settings
// JSON. Never writes.
func ReadConfig(path string) (*Config, error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := ParseConfig(text)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// FileStore is the real Store: settings.json plus the running config. A save writes the file
// atomically and updates the running config under one lock, so the tray and the Settings
// window never overwrite each other.
type FileStore struct {
	path string
	mu   sync.Mutex
	cfg  Config
}

// OpenFileStore loads settings.json (never failing; see Load) as the running config.
func OpenFileStore(path string) *FileStore {
	return &FileStore{path: path, cfg: Load(path)}
}

// Path is settings.json's path.
func (s *FileStore) Path() string { return s.path }

// Current is a copy of the running config.
func (s *FileStore) Current() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Load reads settings.json without writing it.
func (s *FileStore) Load() (*Config, error) { return ReadConfig(s.path) }

// Save writes c, keeping the live geometry, and makes it current.
func (s *FileStore) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := c.WithManagedFrom(s.cfg)
	if err := next.Save(s.path); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

// Adopt makes c current without writing it, keeping the live geometry.
func (s *FileStore) Adopt(c Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c.WithManagedFrom(s.cfg)
}

// ErrFileBroken: settings.json exists but can't be read or parsed, so an app write was kept
// in memory only rather than written over the user's file.
var ErrFileBroken = errors.New("settings.json can't be read; the change was not saved")

// Update changes the running config with fn and saves it: the tray's and the app's own writes
// (pack, size, fade, position, canvas window). It never loses a hand edit: settings.json is
// re-read under the store lock and only the fields fn changed are written onto it, so values
// edited by hand since the last reload and unknown keys stay as they are on disk. The running
// config gets only fn's change; the hand edits reach it through the settings service's Reload
// (which applies live fields), not here. When settings.json doesn't parse, nothing is written:
// the change is kept in memory and an error wrapping ErrFileBroken is returned. On any other
// error nothing changed.
//
// Update takes only the store lock, so it is safe from inside an Applier (which runs under the
// settings API lock).
func (s *FileStore) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	fn(&next)
	disk, err := ReadConfig(s.path)
	if err != nil {
		s.cfg = next
		return fmt.Errorf("%w: %v", ErrFileBroken, err)
	}
	out := next
	if disk != nil {
		if out, err = withChanges(*disk, s.cfg, next); err != nil {
			return err
		}
	}
	if err := out.Save(s.path); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

// withChanges is disk with every value that differs between before and after set to after's.
func withChanges(disk, before, after Config) (Config, error) {
	d := tree(disk)
	applyDiff(d, tree(before), tree(after))
	b, err := json.Marshal(d)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(b)
}

func applyDiff(dst, before, after map[string]any) {
	for k, av := range after {
		bv, had := before[k]
		if had && reflect.DeepEqual(av, bv) {
			continue
		}
		am, aok := av.(map[string]any)
		bm, bok := bv.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if aok && bok && dok {
			applyDiff(dm, bm, am)
			continue
		}
		dst[k] = av
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			delete(dst, k)
		}
	}
}
