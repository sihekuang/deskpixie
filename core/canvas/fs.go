package canvas

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileEntry is one regular file directly inside the pages folder.
type FileEntry struct {
	Name     string
	Modified time.Time
	Len      int64
}

// PageFS is the file-system seam for the pages folder. DirFS is the real implementation;
// tests use MemFS.
type PageFS interface {
	// List returns regular files directly inside the folder (no recursion, no symlinks).
	// Order is unspecified.
	List() ([]FileEntry, error)
	// Read reads one file by plain name (no separators).
	Read(name string) ([]byte, error)
	// ReadLimited reads at most max+1 bytes of a regular file (never a symlink). A result
	// longer than max means the file is over the limit; nothing further is read.
	ReadLimited(name string, max int64) ([]byte, error)
	// Remove deletes one file by plain name.
	Remove(name string) error
}

// DirFS is a PageFS over a real directory.
type DirFS struct{ dir string }

func NewDirFS(dir string) *DirFS { return &DirFS{dir: dir} }

func (d *DirFS) Dir() string { return d.dir }

func (d *DirFS) path(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("not a plain file name: %q: %w", name, os.ErrInvalid)
	}
	return filepath.Join(d.dir, name), nil
}

func (d *DirFS) List() ([]FileEntry, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var out []FileEntry
	for _, e := range entries {
		// DirEntry.Info does not follow symlinks, so a symlink is never a page.
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Modified: info.ModTime(), Len: info.Size()})
	}
	return out, nil
}

func (d *DirFS) Read(name string) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (d *DirFS) ReadLimited(name string, max int64) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	notRegular := fmt.Errorf("not a regular file: %w", os.ErrInvalid)
	// Refuse a symlink up front, then check that the opened handle is the same regular file
	// (closes the swap-after-check window).
	before, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, notRegular
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, notRegular
	}
	limit := max
	if limit < 0 || limit == 1<<63-1 {
		return io.ReadAll(f)
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

func (d *DirFS) Remove(name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}
