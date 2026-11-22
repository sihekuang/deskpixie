package voice

// The providers' model lists, as last loaded from their APIs (#477): ModelCache keeps them in
// a file, Refresher loads one again in the background.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

	mu      sync.Mutex
	ctx     context.Context // ends at Close; the loads run under it
	cancel  context.CancelFunc
	closed  bool
	loading map[string]keySum    // the loads running: provider → the key it uses
	pending map[string]bool      // a key changed while its provider was loading: load again after
	keyOf   map[string]keySum    // the key each provider's list is from (in memory only)
	tried   map[string]time.Time // the last failed load, per provider
	wg      sync.WaitGroup       // the loads and OnChange calls running
}

// keySum stands for a key, so the Refresher can tell keys apart without keeping them.
type keySum [sha256.Size]byte

// Refresh loads provider's list in the background, unless it is loading, fresh, or failed
// less than ModelListRetry ago. Without a key nothing is fetched and the error is ModelsNoKey.
func (r *Refresher) Refresh(provider string) { r.refresh(provider, false) }

// RefreshNow is for a key saved or removed: when provider's key isn't the one its list is
// from, it loads without the waits (after the load running, if any); the same key is a
// Refresh. A key first seen over a fresh list (one loaded in an earlier run) is taken as the
// one it is from.
func (r *Refresher) RefreshNow(provider string) { r.refresh(provider, true) }

func (r *Refresher) refresh(provider string, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshLocked(provider, force)
}

func (r *Refresher) refreshLocked(provider string, force bool) {
	cat := r.Catalog(provider)
	if cat == nil || r.closed {
		return
	}
	if r.ctx == nil {
		r.ctx, r.cancel = context.WithCancel(context.Background())
		r.loading, r.pending, r.keyOf, r.tried = map[string]keySum{}, map[string]bool{}, map[string]keySum{}, map[string]time.Time{}
	}
	key, ok := r.Secrets.APIKey(provider)
	var sum keySum
	if ok {
		sum = sha256.Sum256([]byte(key.Expose()))
	}
	if running, busy := r.loading[provider]; busy {
		if force && (!ok || sum != running) {
			r.pending[provider] = true
		}
		return
	}
	cur := r.Cache.Get(provider)
	if !ok {
		delete(r.keyOf, provider)
		delete(r.tried, provider) // no key isn't a failed load: a key typed in loads at once
		cur.Error = ModelsNoKey
		if r.Cache.set(provider, cur) {
			r.notifyLocked(provider)
		}
		return
	}
	now := r.now()
	fresh := cur.Error == "" && !cur.Fetched.IsZero() && !cur.Fetched.After(now) && now.Sub(cur.Fetched) < ModelListFresh
	retryLater := cur.Error != "" && now.Sub(r.tried[provider]) < ModelListRetry
	known, has := r.keyOf[provider]
	if !has && fresh {
		r.keyOf[provider], known, has = sum, sum, true
	}
	if force && has && known != sum {
		fresh, retryLater = false, false
	}
	if fresh || retryLater {
		return
	}
	r.loading[provider] = sum
	r.wg.Add(1)
	go r.load(r.ctx, provider, cat, key, sum)
}

func (r *Refresher) load(ctx context.Context, provider string, cat ModelCatalog, key Secret, sum keySum) {
	defer r.wg.Done()
	models, err := cat.ListModels(ctx, key.Expose())
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.loading, provider)
	if ctx.Err() != nil {
		return // closed: what a cancelled load says isn't the provider's answer
	}
	r.keyOf[provider] = sum
	l := r.Cache.Get(provider)
	if err != nil {
		l.Error = blankKeys(err.Error())
		r.tried[provider] = r.now()
	} else {
		l = ModelList{Models: models, Fetched: r.now()}
	}
	if r.Cache.set(provider, l) {
		r.notifyLocked(provider)
	}
	if r.pending[provider] {
		delete(r.pending, provider)
		r.refreshLocked(provider, true) // with the key as it is now
	}
}

// keyLike is a token shaped like a provider's key: OpenAI's sk-… (masked in its errors:
// sk-abc1****wxyz) and Google's AIza….
var keyLike = regexp.MustCompile(`(sk-|AIza)[A-Za-z0-9_*\-]+`)

// blankKeys is msg with every key-shaped token replaced by ***.
func blankKeys(msg string) string { return keyLike.ReplaceAllString(msg, "***") }

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
