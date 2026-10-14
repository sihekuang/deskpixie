// Package agentfs holds the filesystem adapters for delegation (port of ai-mascot
// crates/agent-fs): Claude Code's folder trust in ~/.claude.json, git worktrees for workers,
// worker launch files, and the app's private (0600) files.
package agentfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WritePrivate writes text to path with mode 0600 (its folder is created 0700 if missing),
// through a temp file renamed over it, so no reader ever sees a partial or world-readable file.
func WritePrivate(path, text string) error {
	dir := filepath.Dir(path)
	if path == "" || dir == path {
		return errors.New("no parent folder")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Base(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", name, os.Getpid()))
	_ = os.Remove(tmp)
	err := func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return err
		}
		if _, err := f.WriteString(text); err != nil {
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
		return os.Rename(tmp, path)
	}()
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// RemoveQuietly removes path if it exists; any other failure is logged, never fatal.
func RemoveQuietly(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "agent: cannot remove %s: %v\n", path, err)
	}
}

// canonicalize is Rust's fs::canonicalize: absolute, symlinks resolved, must exist.
func canonicalize(p string) (string, error) {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return onDiskCase(r), nil
}

// hasPrefix is Path::starts_with: whole components.
func hasPrefix(p, prefix string) bool {
	if p == prefix {
		return true
	}
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return strings.HasPrefix(p, prefix)
	}
	return strings.HasPrefix(p, prefix+string(filepath.Separator))
}

// joinPath is Path::join: an absolute p replaces base; the result is lexically cleaned.
func joinPath(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// rustLines is str::lines: split at \n, a trailing \r dropped, no final empty line.
func rustLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, l := range parts {
		parts[i] = strings.TrimSuffix(l, "\r")
	}
	return parts
}

func firstLine(s string) string {
	if l := rustLines(s); len(l) > 0 {
		return l[0]
	}
	return ""
}

func isDir(p string) bool {
	m, err := os.Stat(p)
	return err == nil && m.IsDir()
}
