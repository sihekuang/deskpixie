package agentfs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/agent"
)

// ClaudeTrustStore is agent.TrustStore over Claude Code's ~/.claude.json: it pre-accepts the
// folder-trust dialog for directories the app starts an agent in, and only those under allowed.
type ClaudeTrustStore struct {
	config  string   // ~/.claude.json
	allowed []string // only directories under these roots may be trusted
	// beforeWrite (tests): runs after the merge and before the re-read (simulates Claude Code writing).
	beforeWrite func(target string)
}

var _ agent.TrustStore = (*ClaudeTrustStore)(nil)

// NewClaudeTrustStore: config is `~/.claude.json`; allowed are the roots (the app's agent
// workspace; for workers, the projects root) trust may be granted under.
func NewClaudeTrustStore(config string, allowed []string) *ClaudeTrustStore {
	return &ClaudeTrustStore{config: config, allowed: allowed}
}

func terr(format string, a ...any) error { return agent.TrustError(fmt.Sprintf(format, a...)) }

// Grant marks dir (and its real path, when different) trusted; returns the paths it trusted.
func (s *ClaudeTrustStore) Grant(dir string) ([]string, error) {
	real, err := canonicalize(dir)
	if err != nil {
		return nil, terr("%s: %v", dir, err)
	}
	inside := false
	for _, r := range s.allowed {
		if c, err := canonicalize(r); err == nil && hasPrefix(real, c) {
			inside = true
			break
		}
	}
	if !inside {
		return nil, terr("%s is outside the trusted roots", dir)
	}
	keys := []string{dir}
	if real != dir {
		keys = append(keys, real)
	}
	target, err := s.target()
	if err != nil {
		return nil, err
	}
	// Claude Code rewrites this file itself: merge, then write only if nobody wrote it in
	// between (re-read and compare); on a change merge once more from the new text.
	text, err := readConfig(target)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 2; i++ {
		body, changed, err := merge(text, keys)
		if err != nil {
			return nil, err
		}
		if !changed {
			return keys, nil // already trusted: no write
		}
		if s.beforeWrite != nil {
			s.beforeWrite(target)
		}
		now, err := readConfig(target)
		if err != nil {
			return nil, err
		}
		if now != text {
			text = now
			continue
		}
		if err := writeAtomic(target, body); err != nil {
			return nil, err
		}
		return keys, nil
	}
	return nil, terr("~/.claude.json kept changing; trust not written")
}

// target is the file to write: a symlinked ~/.claude.json is followed to its target (the link
// is kept); a dangling link is an error, never replaced.
func (s *ClaudeTrustStore) target() (string, error) {
	if m, err := os.Lstat(s.config); err == nil && m.Mode()&fs.ModeSymlink != 0 {
		p, err := canonicalize(s.config)
		if err != nil {
			return "", terr("~/.claude.json is a link to a missing file: %v", err)
		}
		return p, nil
	}
	return s.config, nil
}

// readConfig is the config's text; `{}` only when it doesn't exist. Any other read failure (no
// permission, not UTF-8, a directory) is an error, so the file is never replaced.
func readConfig(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "{}", nil
		}
		return "", terr("cannot read ~/.claude.json: %v", err)
	}
	if !utf8.Valid(b) {
		return "", terr("~/.claude.json is not UTF-8")
	}
	return string(b), nil
}

// merge returns the text with every key trusted; changed is false if nothing was missing.
func merge(text string, keys []string) (string, bool, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	if err == nil {
		if _, e2 := dec.Token(); e2 != io.EOF {
			err = errors.New("trailing characters")
		}
	}
	if err != nil {
		return "", false, terr("~/.claude.json is not JSON: %v", err)
	}
	root, ok := v.(map[string]any)
	if !ok {
		return "", false, terr("~/.claude.json is not an object")
	}
	rawProjects, present := root["projects"]
	if !present {
		rawProjects = map[string]any{}
		root["projects"] = rawProjects
	}
	projects, ok := rawProjects.(map[string]any)
	if !ok {
		return "", false, terr("~/.claude.json projects is not an object")
	}
	changed := false
	for _, k := range keys {
		e, present := projects[k]
		if !present {
			e = map[string]any{}
			projects[k] = e
		}
		obj, isObj := e.(map[string]any)
		if accepted, _ := obj["hasTrustDialogAccepted"].(bool); !accepted && isObj {
			obj["hasTrustDialogAccepted"] = true
			changed = true
		}
	}
	if !changed {
		return "", false, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", false, terr("%v", err)
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), true, nil
}

// writeAtomic writes body to a temp file created with the target's mode (0600 for a new file),
// then renames it over the target. The temp file is removed on any failure.
func writeAtomic(target, body string) error {
	mode := os.FileMode(0o600)
	if m, err := os.Stat(target); err == nil {
		mode = m.Mode().Perm()
	}
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".claude.json.deskpixie-%d.tmp", os.Getpid()))
	_ = os.Remove(tmp) // a leftover from a crash
	err := func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		// The umask may have cleared bits: set the exact mode before anything is renamed.
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return err
		}
		if _, err := f.WriteString(body); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, target)
	}()
	if err != nil {
		_ = os.Remove(tmp)
		return terr("cannot write ~/.claude.json: %v", err)
	}
	return nil
}
