package app

// Composition of the canvas: the ONLY app code that names concrete canvas types (DirFS,
// PollingSource). Port of ai-mascot src-tauri/src/canvas/mod.rs; the window is Swift's (S5),
// reached through the `canvas.open` event and the canvas.resolve / canvas.geometry methods.
//
// Lock order: the controller calls the presenter with its lock held, and the presenter takes
// the app lock. So the app never calls the controller while holding its own lock.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/canvas"
)

// Canvas loop timing (Rust canvas/mod.rs): tick every 100 ms, list the folder every 5 ticks.
const (
	CanvasTick      = 100 * time.Millisecond
	CanvasPollEvery = 5
)

// EmptyPage is the canvas window's empty state.
const EmptyPage = "canvas://localhost/ds/empty.html"

// PageURL is a page's canvas:// URL.
func PageURL(id canvas.PageID) string { return "canvas://localhost" + id.URLPath() }

// InstallGuide copies CANVAS.md to <pages>/../CANVAS.md and the design system's example pages
// to <pages>/../examples/, so an agent finds them next to the pages folder. Rewritten only when
// the bundled copy differs.
func InstallGuide(ds, pages string) {
	canvasDir := filepath.Dir(pages)
	copyIfChanged(filepath.Join(ds, "CANVAS.md"), filepath.Join(canvasDir, "CANVAS.md"))
	examples := filepath.Join(canvasDir, "examples")
	if err := os.MkdirAll(examples, 0o755); err != nil {
		log.Printf("canvas: cannot create %s: %v", examples, err)
		return
	}
	entries, err := os.ReadDir(filepath.Join(ds, "examples"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		copyIfChanged(filepath.Join(ds, "examples", e.Name()), filepath.Join(examples, e.Name()))
	}
}

func copyIfChanged(src, dest string) {
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if old, err := os.ReadFile(dest); err == nil && bytes.Equal(old, b) {
		return
	}
	if err := os.WriteFile(dest, b, 0o644); err != nil {
		log.Printf("canvas: cannot write %s: %v", dest, err)
	}
}

// canvasPresenter is the controller's window: it tells Swift through events.
type canvasPresenter struct{ a *App }

func (p canvasPresenter) Show(page canvas.PageRef) {
	p.a.emit("canvas.open", map[string]any{"url": PageURL(page.ID), "title": page.Title})
}

func (p canvasPresenter) ShowEmpty() {
	p.a.emit("canvas.open", map[string]any{"url": EmptyPage, "title": ""})
}

func (p canvasPresenter) RecentChanged(recent []canvas.PageRef) {
	items := make([]CanvasPage, 0, len(recent))
	for _, r := range recent {
		items = append(items, CanvasPage{Slug: r.ID.Slug(), Title: r.Title})
	}
	p.a.mu.Lock()
	p.a.canvasRecent = items
	p.a.mu.Unlock()
	p.a.pushTray()
}

// setupCanvas creates the pages folder, installs the guide, prunes, and builds the controller.
// On failure the rest of the app runs without a canvas.
func (a *App) setupCanvas(keep uint64, autoOpen bool) error {
	ds := a.paths.DesignSystem
	if _, err := os.Stat(filepath.Join(ds, "tokens.css")); err != nil {
		log.Printf("canvas: design system missing at %s", ds)
	}
	pages := filepath.Join(a.paths.AppSupport, "canvas", "pages")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		return err
	}
	a.pagesDir = pages
	InstallGuide(ds, pages)
	fs := canvas.NewDirFS(pages)
	store := canvas.NewPageStore(fs)
	if removed, err := store.Prune(int(max(keep, 1))); err != nil {
		log.Printf("canvas: cannot prune pages: %v", err)
	} else if len(removed) > 0 {
		log.Printf("canvas: pruned %d old page(s)", len(removed))
	}
	settings := canvas.DefaultControllerSettings()
	settings.AutoOpen = autoOpen
	a.resolver = canvas.NewResolver(ds, pages)
	a.canvas = canvas.NewCanvasController(store, canvasPresenter{a}, settings)
	a.source = canvas.NewPollingSource(fs)
	log.Printf("canvas: watching %s", pages)
	return nil
}

// canvasLoop polls the pages folder and ticks the controller until stop closes.
func (a *App) canvasLoop(tickEvery time.Duration) {
	defer a.wg.Done()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	var tick uint64
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
		var events []canvas.PageEvent
		if tick%CanvasPollEvery == 0 {
			events = a.source.Poll()
		}
		tick++
		for _, name := range a.source.TakeInvalid() {
			log.Printf("canvas: ignoring %q: page files are named [a-z0-9-]{1,64}.html", name)
		}
		if e, ok := a.source.TakeError(); ok {
			log.Printf("canvas: cannot list the pages folder: %s", e)
		}
		now := time.Now()
		a.canvas.OnEvents(events, now)
		for _, o := range a.canvas.Tick(now) {
			a.reportCanvas(o)
		}
	}
}

func (a *App) reportCanvas(o canvas.Outcome) {
	switch o.Kind {
	case canvas.OutcomeShown:
		log.Printf("canvas: showing %s (%s)", o.Page.ID, o.Page.Title)
		a.Celebrate()
	case canvas.OutcomeUpdated:
		log.Printf("canvas: updated %s (autoOpen is off)", o.Page.ID)
	case canvas.OutcomeLint:
		for _, w := range o.Warnings {
			log.Printf("canvas: %s: %s", o.ID, w)
		}
	case canvas.OutcomeRejected:
		log.Printf("canvas: %s not shown: %s", o.ID, o.Reason)
	}
}
