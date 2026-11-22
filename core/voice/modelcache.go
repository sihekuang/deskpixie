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
// ModelListRetry. The zero value needs Cache, Secrets and Catalog.
type Refresher struct {
	Cache   *ModelCache
	Secrets SecretSource
	// Catalog is provider's lister; nil when it lists nothing.
	Catalog func(provider string) ModelCatalog
	// OnChange is told when a provider's models or error changed (optional).
	OnChange func(provider string)
	// Now is the clock (nil: time.Now).
	Now func() time.Time

	mu       sync.Mutex
	inFlight map[string]bool
	tried    map[string]time.Time // the last failed load, per provider
	wg       sync.WaitGroup
}

// Refresh loads provider's list in the background, unless it is loading, fresh, or failed
// less than ModelListRetry ago. Without a key nothing is fetched and the error is ModelsNoKey.
func (r *Refresher) Refresh(provider string) {
	cat := r.Catalog(provider)
	if cat == nil {
		return
	}
	now := r.now()
	cur := r.Cache.Get(provider)
	key, ok := r.Secrets.APIKey(provider)
	if !ok {
		cur.Error = ModelsNoKey
		r.store(provider, cur)
		return
	}
	r.mu.Lock()
	switch {
	case r.inFlight[provider]:
	case cur.Error == "" && !cur.Fetched.IsZero() && now.Sub(cur.Fetched) < ModelListFresh:
	case cur.Error != "" && now.Sub(r.tried[provider]) < ModelListRetry:
	default:
		if r.inFlight == nil {
			r.inFlight, r.tried = map[string]bool{}, map[string]time.Time{}
		}
		r.inFlight[provider] = true
		r.wg.Add(1)
		go r.load(provider, cat, key)
	}
	r.mu.Unlock()
}

func (r *Refresher) load(provider string, cat ModelCatalog, key Secret) {
	defer r.wg.Done()
	models, err := cat.ListModels(context.Background(), key.Expose())
	l := r.Cache.Get(provider)
	if err != nil {
		l.Error = err.Error()
	} else {
		l = ModelList{Models: models, Fetched: r.now()}
	}
	r.store(provider, l) // before the load ends, so a Refresh meanwhile doesn't load again
	r.mu.Lock()
	delete(r.inFlight, provider)
	if err != nil {
		r.tried[provider] = r.now()
	}
	r.mu.Unlock()
}

// store puts l in the cache and tells OnChange when that changed anything shown.
func (r *Refresher) store(provider string, l ModelList) {
	if r.Cache.set(provider, l) && r.OnChange != nil {
		r.OnChange(provider)
	}
}

// Wait returns when no load is running (tests, shutdown).
func (r *Refresher) Wait() { r.wg.Wait() }

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
