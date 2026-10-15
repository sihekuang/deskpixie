package canvas

import (
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// Presenter is the window as the controller sees it. Implementations must not call back into
// the controller (it holds its lock while calling).
type Presenter interface {
	// Show opens the canvas window on this page, or reloads it if open. Must not steal focus.
	Show(page PageRef)
	// ShowEmpty opens the canvas window on the built-in empty-state page.
	ShowEmpty()
	// RecentChanged reports the recent list (most recent first, at most RecentLen).
	RecentChanged(recent []PageRef)
}

// ControllerSettings tunes the controller.
type ControllerSettings struct {
	// AutoOpen shows new and changed pages automatically (settings.json canvas.autoOpen).
	AutoOpen bool
	// Debounce is how long a page must be quiet before it is shown (spec: 300 ms).
	Debounce time.Duration
	// RecentLen is the tray Canvas > Recent length (spec: 10).
	RecentLen int
}

func DefaultControllerSettings() ControllerSettings {
	return ControllerSettings{AutoOpen: true, Debounce: 300 * time.Millisecond, RecentLen: 10}
}

// OutcomeKind says what happened to a page during a Tick.
type OutcomeKind int

const (
	// OutcomeShown: shown automatically; Pixie plays happy.
	OutcomeShown OutcomeKind = iota
	// OutcomeUpdated: changed while AutoOpen is off; only the recent list moved.
	OutcomeUpdated
	// OutcomeLint: linter warnings for a page that was still shown or updated.
	OutcomeLint
	// OutcomeRejected: not shown (too large or unreadable), with the reason.
	OutcomeRejected
)

// Outcome is what happened during a Tick, for the composition root to log and react to.
// Shown/Updated set Page; Lint sets ID and Warnings; Rejected sets ID and Reason.
type Outcome struct {
	Kind     OutcomeKind
	Page     PageRef
	ID       PageID
	Warnings []Warning
	Reason   string
}

type pendingEntry struct {
	at    time.Time
	stamp Stamp
}

// CanvasController turns page events into "show this page", with a debounce, content dedupe
// and the recent list. It never sleeps or reads the clock (callers pass now). It is safe for
// concurrent use (tick loop plus tray actions).
type CanvasController struct {
	mu        sync.Mutex
	store     *PageStore
	presenter Presenter
	settings  ControllerSettings
	// pending: page -> time and file stamp of its latest event; shown once quiet for Debounce
	// and the file's stamp has not moved since (polls are slower than the debounce).
	pending map[PageID]pendingEntry
	recent  []PageRef
	// seen: hash of the content last shown or updated per page (dedupe).
	seen    map[PageID]uint64
	current *PageID
}

// NewCanvasController seeds the recent list from the store (newest first) and reports it once.
func NewCanvasController(store *PageStore, presenter Presenter, settings ControllerSettings) *CanvasController {
	var recent []PageRef
	if scan, err := store.Scan(); err == nil {
		recent = scan.Pages
	}
	if len(recent) > settings.RecentLen {
		recent = recent[:settings.RecentLen]
	}
	presenter.RecentChanged(recent)
	return &CanvasController{
		store: store, presenter: presenter, settings: settings,
		pending: map[PageID]pendingEntry{}, recent: recent, seen: map[PageID]uint64{},
	}
}

// Recent returns a copy of the recent list.
func (c *CanvasController) Recent() []PageRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]PageRef(nil), c.recent...)
}

// Current is the page last shown in the window, if any.
func (c *CanvasController) Current() *PageID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return nil
	}
	id := *c.current
	return &id
}

// SetAutoOpen applies canvas.autoOpen changing while the app runs. Pages still waiting for
// their debounce follow the new value.
func (c *CanvasController) SetAutoOpen(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.AutoOpen = on
}

// OnEvents records events; each one restarts that page's debounce.
func (c *CanvasController) OnEvents(events []PageEvent, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range events {
		c.pending[e.ID] = pendingEntry{at: now, stamp: c.store.Stamp(e.ID)}
	}
}

// Tick processes every page that has been quiet for Debounce, in slug order.
func (c *CanvasController) Tick(now time.Time) []Outcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tick(now)
}

func (c *CanvasController) tick(now time.Time) []Outcome {
	var quiet []PageID
	for id, p := range c.pending {
		if now.Sub(p.at) >= c.settings.Debounce {
			quiet = append(quiet, id)
		}
	}
	sort.Slice(quiet, func(i, j int) bool { return quiet[i].slug < quiet[j].slug })
	var outcomes []Outcome
	for _, id := range quiet {
		current := c.store.Stamp(id)
		if current != c.pending[id].stamp {
			// Written again since the last event: wait for another quiet period.
			c.pending[id] = pendingEntry{at: now, stamp: current}
			continue
		}
		delete(c.pending, id)
		outcomes = c.settle(id, outcomes)
	}
	return outcomes
}

// Step is Poll + OnEvents + Tick in one call.
func (c *CanvasController) Step(source PageSource, now time.Time) []Outcome {
	events := source.Poll()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range events {
		c.pending[e.ID] = pendingEntry{at: now, stamp: c.store.Stamp(e.ID)}
	}
	return c.tick(now)
}

// OpenLast is tray > Open canvas: the most recent page, or the empty state.
func (c *CanvasController) OpenLast() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recent) == 0 {
		c.presenter.ShowEmpty()
		return nil
	}
	return c.open(c.recent[0].ID)
}

// Open is tray > Recent > page. A page that can no longer be loaded is dropped from the recent list.
func (c *CanvasController) Open(id PageID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open(id)
}

func (c *CanvasController) open(id PageID) error {
	page, b, err := c.store.Get(id)
	if err != nil {
		before := len(c.recent)
		kept := c.recent[:0:0]
		for _, p := range c.recent {
			if p.ID != id {
				kept = append(kept, p)
			}
		}
		c.recent = kept
		if len(c.recent) != before {
			c.presenter.RecentChanged(c.recent)
		}
		return fmt.Errorf("%s: %w", id, err)
	}
	c.seen[id] = hashBytes(b)
	c.presenter.Show(page)
	c.current = &id
	return nil
}

func (c *CanvasController) settle(id PageID, outcomes []Outcome) []Outcome {
	page, b, err := c.store.Get(id)
	if err != nil {
		return append(outcomes, Outcome{Kind: OutcomeRejected, ID: id, Reason: err.Error()})
	}
	digest := hashBytes(b)
	if prev, ok := c.seen[id]; ok && prev == digest {
		return outcomes
	}
	c.seen[id] = digest
	if warnings := Lint(lossy(b)); len(warnings) > 0 {
		outcomes = append(outcomes, Outcome{Kind: OutcomeLint, ID: id, Warnings: warnings})
	}
	recent := []PageRef{page}
	for _, p := range c.recent {
		if p.ID != id {
			recent = append(recent, p)
		}
	}
	if len(recent) > c.settings.RecentLen {
		recent = recent[:c.settings.RecentLen]
	}
	c.recent = recent
	c.presenter.RecentChanged(c.recent)
	if c.settings.AutoOpen {
		c.presenter.Show(page)
		c.current = &id
		return append(outcomes, Outcome{Kind: OutcomeShown, Page: page, ID: id})
	}
	return append(outcomes, Outcome{Kind: OutcomeUpdated, Page: page, ID: id})
}

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}
