package voice

// The providers' model lists, as last loaded from their APIs (#477): ModelCache keeps them in
// a file, Refresher loads one again in the background.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	// ModelListFresh: a list loaded this recently isn't loaded again.
	ModelListFresh = time.Hour
	// ModelListRetry: after a failed load, the next try waits this long.
	ModelListRetry = time.Minute
	// ModelsNoKey is the error of a provider whose key isn't set (nothing is fetched).
	ModelsNoKey = "no API key"
)

// ModelList is one provider's models as last loaded. A failed load keeps the models from
// before and says why in Error.
type ModelList struct {
	Models []string `json:"models,omitempty"`
	// Fetched is when Models were loaded (zero: never).
	Fetched time.Time `json:"fetched,omitzero"`
	// Error is why the last load failed, short ("" when it worked).
	Error string `json:"error,omitempty"`
}

// ModelCache is the model lists in <dir>/models.json. A damaged or unreadable file is set aside
// (.bad) and the cache starts empty; a failed write is logged once and the lists stay in memory.
type ModelCache struct {
	path string

	mu          sync.Mutex
	lists       map[string]ModelList
	warnedWrite bool
}

// NewModelCache is the cache kept in dir, loaded from it now.
func NewModelCache(dir string) *ModelCache {
	c := &ModelCache{path: filepath.Join(dir, "models.json"), lists: map[string]ModelList{}}
	b, err := os.ReadFile(c.path)
	if err == nil {
		if err = json.Unmarshal(b, &c.lists); err != nil || c.lists == nil {
			c.lists = map[string]ModelList{}
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("voice: models: cannot load %s (%v); starting over", c.path, err)
		os.Rename(c.path, c.path+".bad")
	}
	return c
}

// Get is provider's list (zero when there is none).
func (c *ModelCache) Get(provider string) ModelList {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.lists[provider]
	l.Models = slices.Clone(l.Models)
	return l
}

// set stores provider's list and saves, and reports whether its models or error changed.
func (c *ModelCache) set(provider string, l ModelList) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, had := c.lists[provider]
	c.lists[provider] = l
	if had && old.Fetched.Equal(l.Fetched) && old.Error == l.Error && slices.Equal(old.Models, l.Models) {
		return false
	}
	c.saveLocked()
	return !had || old.Error != l.Error || !slices.Equal(old.Models, l.Models)
}

// saveLocked writes the lists atomically (c.mu held).
func (c *ModelCache) saveLocked() {
	b, err := json.MarshalIndent(c.lists, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(c.path), 0o700)
	}
	if err == nil {
		tmp := c.path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			if err = os.Rename(tmp, c.path); err != nil {
				os.Remove(tmp)
			}
		}
	}
	if err != nil && !c.warnedWrite {
		c.warnedWrite = true
		log.Printf("voice: models: cannot save %s (%v); keeping them in memory", c.path, err)
	}
}

// Refresher loads a provider's model list into Cache in the background: one load at a time
// per provider, none within ModelListFresh of a good one, a failed one tried again after
// ModelListRetry. The zero value needs Cache, Secrets and Catalog; Close ends it.
type Refresher struct {
	Cache   *ModelCache
	Secrets SecretSource
	// Catalog is provider's lister; nil when it lists nothing.
	Catalog func(provider string) ModelCatalog
	// OnChange is told when a provider's models or error changed (optional). It runs on a
	// goroutine of its own, never the caller's, so a caller may hold a lock it takes.
	OnChange func(provider string)
	// Now is the clock (nil: time.Now).
	Now func() time.Time

	mu       sync.Mutex
	ctx      context.Context // ends at Close; the loads run under it
	cancel   context.CancelFunc
	closed   bool
	inFlight map[string]bool
	tried    map[string]time.Time // the last failed load, per provider
	wg       sync.WaitGroup       // the loads and OnChange calls running
}

// Refresh loads provider's list in the background, unless it is loading, fresh, or failed
// less than ModelListRetry ago. Without a key nothing is fetched and the error is ModelsNoKey.
func (r *Refresher) Refresh(provider string) { r.refresh(provider, false) }

// RefreshNow is Refresh without the waits (a key was saved or removed); one load still runs
// at a time.
func (r *Refresher) RefreshNow(provider string) { r.refresh(provider, true) }

func (r *Refresher) refresh(provider string, force bool) {
	cat := r.Catalog(provider)
	if cat == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.inFlight[provider] {
		return
	}
	if r.ctx == nil {
		r.ctx, r.cancel = context.WithCancel(context.Background())
		r.inFlight, r.tried = map[string]bool{}, map[string]time.Time{}
	}
	now := r.now()
	cur := r.Cache.Get(provider)
	key, ok := r.Secrets.APIKey(provider)
	if !ok {
		cur.Error = ModelsNoKey
		if r.Cache.set(provider, cur) {
			r.notifyLocked(provider)
		}
		return
	}
	fresh := cur.Error == "" && !cur.Fetched.IsZero() && !cur.Fetched.After(now) && now.Sub(cur.Fetched) < ModelListFresh
	retryLater := cur.Error != "" && now.Sub(r.tried[provider]) < ModelListRetry
	if !force && (fresh || retryLater) {
		return
	}
	r.inFlight[provider] = true
	r.wg.Add(1)
	go r.load(r.ctx, provider, cat, key)
}

func (r *Refresher) load(ctx context.Context, provider string, cat ModelCatalog, key Secret) {
	defer r.wg.Done()
	models, err := cat.ListModels(ctx, key.Expose())
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, provider)
	if ctx.Err() != nil {
		return // closed: what a cancelled load says isn't the provider's answer
	}
	l := r.Cache.Get(provider)
	if err != nil {
		l.Error = err.Error()
		r.tried[provider] = r.now()
	} else {
		l = ModelList{Models: models, Fetched: r.now()}
	}
	if r.Cache.set(provider, l) {
		r.notifyLocked(provider)
	}
}

// notifyLocked tells OnChange about provider on a goroutine of its own (r.mu held).
func (r *Refresher) notifyLocked(provider string) {
	if r.OnChange == nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.OnChange(provider)
	}()
}

// Wait returns when no load or OnChange call is running (tests).
func (r *Refresher) Wait() { r.wg.Wait() }

// Close cancels the loads running, waits for them and stops refreshing.
func (r *Refresher) Close() {
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
