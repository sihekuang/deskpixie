// Package logfile keeps DeskPixie's logs bounded on disk and exports them (#225).
//
// A log is a file plus one older one (path.1). Each holds at most half the limit, so a log
// never takes more than the limit (Settings ▸ Advanced ▸ Log size), read on every write.
package logfile

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMax is the Log size setting's default (2 MB), for a log made before settings are read.
const DefaultMax int64 = 2 << 20

// Writer appends to a log, rotating it to path.1 when the next write would pass half the
// limit. A write that can't land is dropped: a log never stops what is logging.
type Writer struct {
	mu   sync.Mutex
	path string
	max  func() int64
}

// New logs to path, keeping at most max() bytes on disk.
func New(path string, max func() int64) *Writer {
	return &Writer{path: path, max: max}
}

// Path is the current file.
func (w *Writer) Path() string { return w.path }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return len(p), nil
	}
	half := w.max() / 2
	if fi, err := os.Stat(w.path); err == nil && fi.Size()+int64(len(p)) > half {
		_ = os.Rename(w.path, w.path+".1")
	}
	if old, err := os.Stat(w.path + ".1"); err == nil && old.Size() > half {
		// Over the half (a log from before the cap, or a smaller setting): it keeps its
		// newest whole lines.
		keepTail(w.path+".1", half)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return len(p), nil
	}
	_, _ = f.Write(p)
	_ = f.Close()
	return len(p), nil
}

// keepTail cuts path down to its last max bytes, from the first line that starts in them.
func keepTail(path string, max int64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if int64(len(b)) > max {
		b = b[int64(len(b))-max:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

// Files are a log's files that exist, older first.
func Files(path string) []string {
	var out []string
	for _, p := range []string{path + ".1", path} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}

// Export writes a zip at dst holding each log's files (by base name) and about.txt. A file
// that disappears meanwhile is skipped; on failure nothing is left at dst.
func Export(dst string, logs []string, about string) (err error) {
	if dst == "" {
		return errors.New("no file to export to")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".deskpixie-logs-*.zip")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	z := zip.NewWriter(tmp)
	w, err := z.CreateHeader(&zip.FileHeader{Name: "about.txt", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	if _, err = io.WriteString(w, about); err != nil {
		return err
	}
	for _, log := range logs {
		for _, p := range Files(log) {
			if err = add(z, p); err != nil {
				return err
			}
		}
	}
	if err = z.Close(); err != nil {
		return err
	}
	if err = tmp.Chmod(0o644); err != nil { // a file to share, like any the user saves
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func add(z *zip.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // rotated or removed meanwhile
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	h, err := zip.FileInfoHeader(fi) // keeps the log's time
	if err != nil {
		return err
	}
	h.Method = zip.Deflate
	w, err := z.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}
