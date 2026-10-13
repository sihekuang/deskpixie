package canvas

import (
	"errors"
	"io/fs"
	"sort"
	"sync"
	"time"
)

// Test doubles, exported so the composition root's tests (P14) can use them too.

type memFile struct {
	body []byte
	at   time.Time
}

// MemFS is an in-memory pages folder. Each Write is one second newer than the previous one.
type MemFS struct {
	mu        sync.Mutex
	files     map[string]memFile
	clock     int64
	failList  bool
	bytesRead int64
}

func NewMemFS() *MemFS { return &MemFS{files: map[string]memFile{}} }

func (m *MemFS) Write(name string, body []byte) {
	m.mu.Lock()
	m.clock++
	secs := m.clock
	m.mu.Unlock()
	m.WriteAt(name, body, secs)
}

// WriteAt writes with an explicit modification time (seconds after the epoch).
func (m *MemFS) WriteAt(name string, body []byte, secs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = memFile{body: append([]byte(nil), body...), at: time.Unix(secs, 0)}
}

// BytesRead is the total bytes handed out by ReadLimited so far.
func (m *MemFS) BytesRead() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytesRead
}

// Names lists the file names, sorted.
func (m *MemFS) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for n := range m.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SetListError makes List fail (as if the folder were deleted) until called with false.
func (m *MemFS) SetListError(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failList = fail
}

func (m *MemFS) List() ([]FileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failList {
		return nil, errors.New("pages folder missing")
	}
	out := make([]FileEntry, 0, len(m.files))
	for n, f := range m.files {
		out = append(out, FileEntry{Name: n, Modified: f.at, Len: int64(len(f.body))})
	}
	return out, nil
}

func (m *MemFS) Read(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), f.body...), nil
}

func (m *MemFS) ReadLimited(name string, max int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	n := int64(len(f.body))
	if max+1 < n {
		n = max + 1
	}
	m.bytesRead += n
	return append([]byte(nil), f.body[:n]...), nil
}

func (m *MemFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[name]; !ok {
		return fs.ErrNotExist
	}
	delete(m.files, name)
	return nil
}

// ScriptedSource is a PageSource that returns pre-scripted batches, one per Poll, then nothing.
type ScriptedSource struct{ batches [][]PageEvent }

func NewScriptedSource(batches [][]PageEvent) *ScriptedSource {
	return &ScriptedSource{batches: batches}
}

func (s *ScriptedSource) Poll() []PageEvent {
	if len(s.batches) == 0 {
		return nil
	}
	b := s.batches[0]
	s.batches = s.batches[1:]
	return b
}

// PresenterCallKind identifies a recorded Presenter call.
type PresenterCallKind int

const (
	CallShow PresenterCallKind = iota
	CallShowEmpty
	CallRecent
)

// PresenterCall is one recorded call: Show(Slug, Title), ShowEmpty, or Recent(Slugs, most recent first).
type PresenterCall struct {
	Kind  PresenterCallKind
	Slug  string
	Title string
	Slugs []string
}

// RecordingPresenter is a Presenter that records calls.
type RecordingPresenter struct {
	mu    sync.Mutex
	calls []PresenterCall
}

// Take returns and clears the calls so far.
func (r *RecordingPresenter) Take() []PresenterCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls
	r.calls = nil
	return c
}

func (r *RecordingPresenter) add(c PresenterCall) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
}

func (r *RecordingPresenter) Show(p PageRef) {
	r.add(PresenterCall{Kind: CallShow, Slug: p.ID.Slug(), Title: p.Title})
}
func (r *RecordingPresenter) ShowEmpty() { r.add(PresenterCall{Kind: CallShowEmpty}) }
func (r *RecordingPresenter) RecentChanged(recent []PageRef) {
	slugs := make([]string, 0, len(recent))
	for _, p := range recent {
		slugs = append(slugs, p.ID.Slug())
	}
	r.add(PresenterCall{Kind: CallRecent, Slugs: slugs})
}
