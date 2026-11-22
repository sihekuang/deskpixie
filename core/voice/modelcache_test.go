package voice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type keys map[string]string

func (k keys) APIKey(p string) (Secret, bool) {
	v, ok := k[p]
	return NewSecret(v), ok
}
func (keys) KeyHint(string) string { return "" }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeCatalog answers with models or err; with gate set, each list waits for it.
type fakeCatalog struct {
	mu     sync.Mutex
	models []string
	err    error
	calls  int
	keys   []string
	gate   chan struct{}
}

func (f *fakeCatalog) ListModels(_ context.Context, apiKey string) ([]string, error) {
	f.mu.Lock()
	f.calls++
	f.keys = append(f.keys, apiKey)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, f.err
}

func (f *fakeCatalog) set(models []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models, f.err = models, err
}

func (f *fakeCatalog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type rig struct {
	dir     string
	clock   *fakeClock
	cat     *fakeCatalog
	r       *Refresher
	mu      sync.Mutex
	changed []string
}

func newRig(t *testing.T, k keys) *rig {
	t.Helper()
	g := &rig{dir: t.TempDir(), clock: &fakeClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}, cat: &fakeCatalog{}}
	g.r = &Refresher{
		Cache:   NewModelCache(g.dir),
		Secrets: k,
		Catalog: func(p string) ModelCatalog {
			if p == "p" {
				return g.cat
			}
			return nil
		},
		OnChange: func(p string) { g.mu.Lock(); g.changed = append(g.changed, p); g.mu.Unlock() },
		Now:      g.clock.Now,
	}
	return g
}

func (g *rig) refresh() { g.r.Refresh("p"); g.r.Wait() }

func (g *rig) changes() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.changed)
}

func TestARefreshStoresTheListAndItSurvivesARestart(t *testing.T) {
	g := newRig(t, keys{"p": "sk-1"})
	g.cat.set([]string{"m1", "m2"}, nil)
	g.refresh()
	want := ModelList{Models: []string{"m1", "m2"}, Fetched: g.clock.Now()}
	if got := g.r.Cache.Get("p"); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if g.changes() != 1 || !reflect.DeepEqual(g.cat.keys, []string{"sk-1"}) {
		t.Fatal(g.changed, g.cat.keys)
	}
	if got := NewModelCache(g.dir).Get("p"); !got.Fetched.Equal(want.Fetched) || !reflect.DeepEqual(got.Models, want.Models) {
		t.Fatal("not saved:", got)
	}
	if _, err := os.Stat(filepath.Join(g.dir, "models.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("the temp file is left:", err)
	}
	if got := g.r.Cache.Get("other"); !reflect.DeepEqual(got, ModelList{}) {
		t.Fatal(got)
	}
}

func TestAFreshListIsNotLoadedAgainForAnHour(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	g.clock.add(59 * time.Minute)
	g.refresh()
	if g.cat.count() != 1 {
		t.Fatal(g.cat.count())
	}
	g.clock.add(time.Minute)
	g.refresh()
	if g.cat.count() != 2 {
		t.Fatal(g.cat.count())
	}
	if g.changes() != 1 {
		t.Fatal("the same list again isn't a change:", g.changed)
	}
}

// A failure keeps the models from before, says why, and may be tried again after a minute.
func TestAFailedLoadKeepsTheListAndRetriesAfterAMinute(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	fetched := g.clock.Now()
	g.clock.add(2 * time.Hour)
	g.cat.set(nil, errors.New("openai models: http 500"))
	g.refresh()
	got := g.r.Cache.Get("p")
	if !reflect.DeepEqual(got.Models, []string{"m1"}) || got.Error != "openai models: http 500" || !got.Fetched.Equal(fetched) {
		t.Fatal(got)
	}
	if g.changes() != 2 {
		t.Fatal(g.changed)
	}
	g.clock.add(59 * time.Second)
	g.refresh()
	if g.cat.count() != 2 {
		t.Fatal("retried too soon:", g.cat.count())
	}
	g.clock.add(time.Second)
	g.cat.set([]string{"m1", "m2"}, nil)
	g.refresh()
	if got := g.r.Cache.Get("p"); g.cat.count() != 3 || got.Error != "" || len(got.Models) != 2 {
		t.Fatal(g.cat.count(), got)
	}
	if g.changes() != 3 {
		t.Fatal(g.changed)
	}
}

func TestWithoutAKeyNothingIsFetched(t *testing.T) {
	k := keys{}
	g := newRig(t, k)
	g.refresh()
	g.refresh()
	if got := g.r.Cache.Get("p"); g.cat.count() != 0 || got.Error != ModelsNoKey {
		t.Fatal(g.cat.count(), got)
	}
	if g.changes() != 1 {
		t.Fatal("told once:", g.changed)
	}
	k["p"] = "k" // a key typed in: loads at once, without waiting out the retry
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	if got := g.r.Cache.Get("p"); g.cat.count() != 1 || got.Error != "" {
		t.Fatal(g.cat.count(), got)
	}
}

func TestOneLoadAtATimePerProvider(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	gate := make(chan struct{})
	g.cat.gate = gate
	g.cat.set([]string{"m1"}, nil)
	for range 5 {
		g.r.Refresh("p")
	}
	close(gate)
	g.r.Wait()
	if g.cat.count() != 1 {
		t.Fatal(g.cat.count())
	}
}

func TestAProviderWithoutACatalogIsIgnored(t *testing.T) {
	g := newRig(t, keys{"other": "k"})
	g.r.Refresh("other")
	g.r.Wait()
	if got := g.r.Cache.Get("other"); !reflect.DeepEqual(got, ModelList{}) || g.changes() != 0 {
		t.Fatal(got, g.changed)
	}
}

func TestADamagedFileIsSetAsideAndTheCacheStartsEmpty(t *testing.T) {
	for name, body := range map[string]string{"garbage": "{not json", "wrong shape": `["a"]`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "models.json")
		os.WriteFile(path, []byte(body), 0o600)
		c := NewModelCache(dir)
		if got := c.Get("p"); !reflect.DeepEqual(got, ModelList{}) {
			t.Fatal(name, got)
		}
		if b, err := os.ReadFile(path + ".bad"); err != nil || string(b) != body {
			t.Fatal(name, "not set aside:", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(name, err)
		}
	}
	// A folder in the way can't be read either: it is set aside too.
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "models.json"), 0o700)
	if got := NewModelCache(dir).Get("p"); !reflect.DeepEqual(got, ModelList{}) {
		t.Fatal(got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "models.json.bad")); err != nil || !fi.IsDir() {
		t.Fatal(err)
	}
}

func TestAMissingFolderIsMadeOnSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "voice")
	g := newRig(t, keys{"p": "k"})
	g.r.Cache = NewModelCache(dir)
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	if got := NewModelCache(dir).Get("p"); !reflect.DeepEqual(got.Models, []string{"m1"}) {
		t.Fatal(got)
	}
}

// A folder that can't be written keeps the lists in memory, and says so once.
func TestAnUnwritableFolderKeepsTheListsInMemory(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "file")
	os.WriteFile(blocker, nil, 0o600)
	g := newRig(t, keys{"p": "k"})
	g.r.Cache = NewModelCache(filepath.Join(blocker, "voice")) // a file in the way of the folder
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	if got := g.r.Cache.Get("p"); !reflect.DeepEqual(got.Models, []string{"m1"}) || g.changes() != 1 {
		t.Fatal(got, g.changed)
	}
	if !g.r.Cache.warnedWrite {
		t.Fatal("not logged")
	}
}

func TestGetReturnsACopy(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	g.r.Cache.Get("p").Models[0] = "x"
	if g.r.Cache.Get("p").Models[0] != "m1" {
		t.Fatal("the cache was changed through Get")
	}
}

// OnChange runs off the caller's goroutine, so a caller holding a lock OnChange takes (the
// settings snapshot does) doesn't deadlock, the no-key path included.
func TestOnChangeNeverRunsOnTheCallersGoroutine(t *testing.T) {
	var mu sync.Mutex
	g := newRig(t, keys{})
	told := make(chan string, 4)
	g.r.OnChange = func(p string) { mu.Lock(); mu.Unlock(); told <- p }
	done := make(chan struct{})
	go func() {
		mu.Lock()
		g.r.Refresh("p")
		mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Refresh waited for OnChange")
	}
	g.r.Wait()
	if p := <-told; p != "p" {
		t.Fatal(p)
	}
}

// ctxCatalog waits for its context, as a slow network does.
type ctxCatalog struct{ started chan struct{} }

func (c ctxCatalog) ListModels(ctx context.Context, _ string) ([]string, error) {
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCloseCancelsALoadAndStopsRefreshing(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	cat := ctxCatalog{started: make(chan struct{})}
	g.r.Catalog = func(string) ModelCatalog { return cat }
	g.r.Refresh("p")
	<-cat.started
	closed := make(chan struct{})
	go func() { g.r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close didn't cancel the load")
	}
	if got := g.r.Cache.Get("p"); !reflect.DeepEqual(got, ModelList{}) || g.changes() != 0 {
		t.Fatal("a cancelled load stores nothing:", got, g.changed)
	}
	g.r.Catalog = func(string) ModelCatalog { return g.cat }
	g.r.Refresh("p")
	g.r.Wait()
	if g.cat.count() != 0 {
		t.Fatal("refreshed after Close")
	}
}

func TestAFetchedTimeInTheFutureIsStale(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	g.r.Cache.set("p", ModelList{Models: []string{"old"}, Fetched: g.clock.Now().Add(24 * time.Hour)})
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	if g.cat.count() != 1 {
		t.Fatal(g.cat.count())
	}
}

// RefreshNow (a key saved or removed) loads even a fresh list again, and right after a failure.
func TestRefreshNowIgnoresTheWaits(t *testing.T) {
	g := newRig(t, keys{"p": "k"})
	g.cat.set([]string{"m1"}, nil)
	g.refresh()
	g.r.RefreshNow("p")
	g.r.Wait()
	g.cat.set(nil, errors.New("x"))
	g.r.RefreshNow("p")
	g.r.Wait()
	g.r.RefreshNow("p")
	g.r.Wait()
	if g.cat.count() != 4 {
		t.Fatal(g.cat.count())
	}
}

// Without a key while a load runs, the load's result stands.
func TestNoKeyDoesntOverwriteALoadInFlight(t *testing.T) {
	k := keys{"p": "k"}
	g := newRig(t, k)
	gate := make(chan struct{})
	g.cat.gate = gate
	g.cat.set([]string{"m1"}, nil)
	g.r.Refresh("p")
	delete(k, "p")
	g.r.Refresh("p")
	close(gate)
	g.r.Wait()
	if got := g.r.Cache.Get("p"); got.Error != "" || len(got.Models) != 1 {
		t.Fatal(got)
	}
}

func TestAFileThatCantBeReadIsSetAside(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	os.WriteFile(path, []byte(`{"p":{"models":["m1"]}}`), 0o000)
	if got := NewModelCache(dir).Get("p"); !reflect.DeepEqual(got, ModelList{}) {
		t.Fatal(got)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatal(err)
	}
}
