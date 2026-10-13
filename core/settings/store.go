package settings

import (
	"errors"
	"io/fs"
	"os"
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

// Update changes the running config with fn and saves it: the tray's and the app's own writes
// (pack, size, fade, position, canvas window). Managed fields set by fn are kept. On error
// nothing changed.
func (s *FileStore) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	fn(&next)
	if err := next.Save(s.path); err != nil {
		return err
	}
	s.cfg = next
	return nil
}
