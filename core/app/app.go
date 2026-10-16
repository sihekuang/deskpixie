// Package app is DeskPixie's app core: the composition root that turns settings.json into a
// running voice loop, canvas and pixie, and the app.* / pixie.* / canvas.* / audio.* dp_call
// methods Swift uses. Port of ai-mascot src-tauri/src/{lib.rs, actions.rs, settings/live.rs,
// voice/mod.rs, canvas/mod.rs} minus the windows, tray and hotkey (Swift's).
//
// The delegation agent plugs in through Deps.Agent (core/app/agentapp builds it); without it
// the Agent menu is hidden and pixie.click is a logged no-op.
//
// Lock order: settings API lock → app lock (a.mu) → settings store lock. The app never holds
// a.mu while calling the settings API, the canvas controller or the runtime's blocking calls,
// and the Applier (run under the API lock) never calls back into the API.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/audio/denoise"
	"github.com/sihekuang/deskpixie/core/canvas"
	"github.com/sihekuang/deskpixie/core/packs"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Paths is what Swift passes to dp_configure: where things are on disk.
type Paths struct {
	// AppSupport is ~/Library/Application Support/com.sihekuang.deskpixie (settings.json,
	// pixies/, canvas/, recordings/, .env).
	AppSupport string `json:"appSupport"`
	// BundledPixies is the app bundle's pixies folder (Resources/pixies).
	BundledPixies string `json:"bundledPixies"`
	// DesignSystem is the app bundle's design-system folder (Resources/design-system).
	DesignSystem string `json:"designSystem"`
	// DFN3ModelPath is Resources/DeepFilterNet3.tar.gz ("" = DFN3 unavailable; Hush is built in).
	DFN3ModelPath string `json:"dfn3ModelPath"`
	// Platform is the settings platform name ("macos"); "" = this build's platform.
	Platform string `json:"platform"`
}

// ErrNoPack: no valid pixie pack in any root.
var ErrNoPack = errors.New("no valid pixie pack")

// Registrar is the dp_call registry (core/bridge adapts its own).
type Registrar interface {
	Register(method string, fn func(args json.RawMessage) (any, error))
}

// AgentTray is what the agent shows in the tray (Agent.Tray).
type AgentTray struct {
	Enabled bool
	Jobs    []JobRow
	// Status is the runtime's status line; Hint the standing hint (IntegrationHint).
	Status, Hint string
	// Mode is the Manual-mode line ("Coordinator is in Manual mode: <reason>"), "" when no
	// Claude agent is in Manual mode (#97); ModeFix its one-click fix, or nil.
	Mode    string
	ModeFix *TrayFix
}

// Deps are the app's collaborators; zero values pick the real ones.
type Deps struct {
	Paths Paths
	// Emit pushes one encoded event (kind + fields) to Swift. It must not block.
	Emit func(kind string, event []byte)
	// Providers is the voice provider registry; nil = Providers().
	Providers map[string]ModelFactory
	// Secrets finds API keys; nil = Keychain values (secrets.keychain), then env, then .env in
	// the cwd and AppSupport.
	Secrets voice.SecretSource
	// Run starts a program without waiting (open the pages folder, reveal settings.json).
	Run func(program string, args ...string) error
	// OnMicSent is passed to the runtime (latency metrics).
	OnMicSent func(capturedAt time.Time)
	// CanvasTick overrides the canvas loop tick (tests); 0 = CanvasTick.
	CanvasTick time.Duration

	// ExtraTools and ExtraPersona are added to the voice loop's tools and persona.
	ExtraTools   []voice.Tool
	ExtraPersona string
	// LocalTools are the local tools' boundaries (zero = the real ones; tests inject fakes).
	LocalTools LocalToolDeps
	// Agent builds the delegation agent (nil = no agent; the factory returns nil when
	// agent.enabled is false). Its tools and persona join the voice loop's.
	Agent AgentFactory
}

// MouthMeter is the voice loop's view of a pack change.
type MouthMeter interface{ SetMouthLevels(levels int) }

// hotkeyState tracks what Swift has bound, so the Applier knows when to ask it to rebind.
type hotkeyState struct {
	bound   string // what Swift has bound (or is binding)
	prev    string // what was bound before the last `hotkey` event
	pending string // the combo the last `hotkey` event asked for
	refused string // a hand-edited combo Swift could not bind
}

// App is the running app core. Safe for concurrent use.
type App struct {
	deps  Deps
	paths Paths
	roots []string
	store *settings.FileStore
	api   *settings.API

	rt        *voice.Runtime // nil when the voice loop could not be set up
	setupFail *voice.Failure
	cleaner   *denoise.Cleaner
	recordDir string

	agent Agent // nil when delegation is off

	// localTools are built once and shared by the voice loop (voiceTools) and the agent.
	localTools []tools.Tool
	voiceTools *voice.ToolRegistry

	canvas   *canvas.CanvasController // nil when the canvas is unavailable
	resolver *canvas.Resolver
	source   *canvas.PollingSource
	pagesDir string

	talkMu     sync.Mutex
	talkSwitch chan bool
	talkClosed bool

	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once

	mu           sync.Mutex
	pixie        PixieState
	packs        map[string]packs.Pack
	wanted       string            // the chosen pack id (shown when loaded, else a fallback)
	hitMasks     map[string]string // pack id → base64 hit mask
	meter        MouthMeter
	voiceActive  bool
	voiceStatus  string
	hotkeyError  string
	canvasRecent []CanvasPage
	hotkey       hotkeyState
}

// setupFailureHold is how long the hotkey's setup-failure error state shows (Rust 2 s).
var setupFailureHold = 2 * time.Second

// New loads settings and packs, builds the voice loop and canvas, and starts the background
// loops. It fails only without AppSupport or without any valid pixie pack.
func New(d Deps) (*App, error) {
	p := d.Paths
	if p.AppSupport == "" {
		return nil, errors.New("app: no appSupport path")
	}
	if d.Emit == nil {
		d.Emit = func(string, []byte) {}
	}
	if d.Providers == nil {
		d.Providers = Providers()
	}
	if d.Secrets == nil {
		d.Secrets = voice.NewEnvSecrets(voice.DotenvPaths(p.AppSupport))
	}
	if d.Run == nil {
		d.Run = startAndReap
	}
	if d.CanvasTick <= 0 {
		d.CanvasTick = CanvasTick
	}
	userRoots := UserPackRoots(p.AppSupport)
	a := &App{
		deps:       d,
		paths:      p,
		store:      settings.OpenFileStore(filepath.Join(p.AppSupport, "settings.json")),
		talkSwitch: make(chan bool),
		stop:       make(chan struct{}),
		pixie:      NewPixieState(),
		hitMasks:   map[string]string{},
	}
	if p.BundledPixies != "" {
		a.roots = append(a.roots, p.BundledPixies)
	}
	a.roots = append(a.roots, userRoots...)
	a.packs, _ = packs.Load(a.roots...) // rejected packs are logged by Load
	if len(a.packs) == 0 {
		return nil, fmt.Errorf("%w in %q", ErrNoPack, a.roots)
	}
	cfg := a.store.Current()
	a.wanted = cfg.Pack
	a.hotkey.bound = cfg.Hotkey

	platform := settings.Platform(p.Platform)
	if platform == "" {
		platform = settings.CurrentPlatform()
	}
	a.api = settings.NewAPI(settings.APIDeps{
		Store:        a.store,
		Applier:      applier{a},
		Env:          func() settings.Env { return a.settingsEnv(platform) },
		Secrets:      secretStatus{d.Secrets},
		SettingsPath: a.store.Path(),
		Platform:     platform,
		Restart:      func() { a.emit("restart", nil) },
		Run:          func(program string, args []string) error { return d.Run(program, args...) },
	})

	if err := a.setupCanvas(cfg.Canvas.Keep, cfg.Canvas.AutoOpen); err != nil {
		log.Printf("canvas: unavailable: %v", err)
		a.canvas = nil
	}
	// Before the agent and the voice loop, which both hold these instances. The alarm reaches
	// the voice loop lazily (a.voiceLink), so it works with no voice loop at all.
	a.localTools = NewLocalTools(homeCity(cfg), &TimerAlarm{Voice: a.voiceLink, Notify: a.notify}, d.LocalTools)
	if d.Agent != nil {
		a.agent = d.Agent(cfg, p.AppSupport, AgentHooks{
			LocalTools:  a.localTools,
			Celebrate:   a.Celebrate,
			FlashError:  a.FlashError,
			RefreshTray: a.pushTray,
			// Action emits settings.changed after the menu id that calls this.
			ClearModel: func() { a.save(func(c *settings.Config) { c.Agent.Model = nil }) },
			Notify:     func(title, body string) { a.emit("notify", map[string]any{"title": title, "body": body}) },
			Updates:    func(items any) { a.emit("updates", map[string]any{"lines": items}) },
			Roster:     func(view any) { a.emit("agents", map[string]any{"roster": view}) },
			ShowPane:   func(job, worker string) { a.emit("showPane", map[string]any{"job": job, "worker": worker}) },
		})
	}
	a.buildVoice(cfg)

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		RunTalkSim(a.talkSwitch, appMouth{a}, TalkTick)
	}()
	if a.canvas != nil {
		a.wg.Add(1)
		go a.canvasLoop(d.CanvasTick)
	}
	a.emitDevices(cfg.Voice)
	a.pushTray()
	return a, nil
}

// buildVoice builds the voice loop from settings. A setup failure (unknown provider) leaves
// a.rt nil; the hotkey then shows the failure.
func (a *App) buildVoice(cfg settings.Config) {
	a.cleaner = denoise.New(denoise.Config{
		Model:         NoiseModel(cfg.Voice.NoiseModel),
		Enabled:       cfg.Voice.NoiseFilter,
		DFN3ModelPath: a.paths.DFN3ModelPath,
	})
	model, fail := PickModel(a.deps.Providers, cfg.Voice)
	if fail != nil {
		log.Printf("voice loop unavailable: %s", fail.Status())
		a.setupFail = fail
		return
	}
	home := homeCity(cfg)
	reg := VoiceTools(a.localTools)
	a.voiceTools = reg
	extra, extraPersona := a.voiceExtras()
	for _, t := range extra {
		reg.Register(t)
	}
	persona := ""
	if cfg.Voice.PersonaPath != nil {
		persona = *cfg.Voice.PersonaPath
	}
	idle, hold := Timeouts(cfg)
	a.recordDir = voice.RecordingDir(a.paths.AppSupport)
	levels, ok := a.currentLevels()
	a.rt = voice.NewRuntime(model, a.cleaner, voice.RuntimeConfig{
		Session:      SessionConfig(cfg.Voice, Instructions(voice.Instructions(persona), extraPersona)),
		Secrets:      a.deps.Secrets,
		Tools:        reg,
		Facts:        a.sessionFacts(home),
		Chimes:       cfg.Voice.Chimes,
		RecordDir:    a.recordDir,
		MouthLevels:  MouthLevels(levels, ok),
		Conversation: voice.ConversationConfig{IdleTimeout: idle, HoldTimeout: hold, HandOff: cfg.Voice.HandOffWhenUnsure, CheckClaims: cfg.Voice.CheckClaims},
		OnMicSent:    a.deps.OnMicSent,
		Log:          voice.NewVoiceLog(voice.VoiceLogPath(a.paths.AppSupport)),
	}, a.onVoiceEvent)
	a.mu.Lock()
	a.meter = a.rt
	a.mu.Unlock()
	if a.agent != nil {
		a.agent.AttachVoice(a.rt)
	}
	signals := a.rt.Subscribe()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for s := range signals {
			switch s.Kind {
			case voice.SigOpened:
				a.setVoiceActive(true)
			case voice.SigClosed:
				a.setVoiceActive(false)
			}
		}
	}()
}

// homeCity is voice.homeCity ("" when unset).
func homeCity(cfg settings.Config) string {
	if cfg.Voice.HomeCity != nil {
		return *cfg.Voice.HomeCity
	}
	return ""
}

// voiceExtras are the tools and persona section added to the voice loop's: Deps.ExtraTools /
// ExtraPersona, then the agent's (Rust: persona + "\n\n" + DELEGATION_PERSONA, only when the
// agent was built).
func (a *App) voiceExtras() ([]voice.Tool, string) {
	tools := append([]voice.Tool{}, a.deps.ExtraTools...)
	persona := strings.TrimSpace(a.deps.ExtraPersona)
	if a.agent != nil {
		tools = append(tools, a.agent.Tools()...)
		if persona == "" {
			persona = a.agent.Persona()
		} else {
			persona = Instructions(persona, a.agent.Persona())
		}
	}
	return tools, persona
}

// sessionFacts are the facts each voice session starts with: the local ones, then, when the
// agent has them, its recent jobs (Pixie's memory comes from the coordinator side).
func (a *App) sessionFacts(home string) voice.SessionFacts {
	var facts voice.SessionFacts = tools.LocalFacts{Clock: tools.SystemClock{}, Home: home}
	if f, ok := a.agent.(AgentFacts); ok {
		facts = joinedFacts{facts, f}
	}
	return facts
}

// joinedFacts joins several SessionFacts with blank lines, skipping blank ones.
type joinedFacts []voice.SessionFacts

func (s joinedFacts) Facts() string {
	var parts []string
	for _, f := range s {
		if t := strings.TrimSpace(f.Facts()); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (a *App) setVoiceActive(on bool) {
	a.mu.Lock()
	a.voiceActive = on
	a.mu.Unlock()
}

// voiceLink is the runtime for the timer alarm, or nil.
func (a *App) voiceLink() VoiceLink {
	if a.rt == nil {
		return nil
	}
	return a.rt
}

// Runtime is the voice loop (nil when it could not be set up). The bridge feeds it audio.
func (a *App) Runtime() *voice.Runtime { return a.rt }

// Close stops the agent (Rust agent::shutdown on exit: pending permissions denied, panes
// stopped within 3 s unless keepPanesOnQuit, MCP configs deleted, the session host stopped),
// then every loop and the voice runtime. Idempotent.
func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.agent != nil {
			a.agent.Shutdown()
		}
		close(a.stop)
		a.talkMu.Lock()
		a.talkClosed = true
		close(a.talkSwitch)
		a.talkMu.Unlock()
		if a.rt != nil {
			a.rt.Close() // closes the cleaner and the signal channel
		} else if a.cleaner != nil {
			_ = a.cleaner.Close()
		}
		a.wg.Wait()
	})
}

// --- events ---

func (a *App) emit(kind string, fields map[string]any) {
	m := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		m[k] = v
	}
	m["kind"] = kind
	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("app: encoding %s event: %v", kind, err)
		return
	}
	a.deps.Emit(kind, b)
}

func (a *App) pushTray() { a.emit("tray", nil) }

func (a *App) notify(title, body string) {
	a.emit("notify", map[string]any{"title": title, "body": body, "sound": "glass"})
}

func (a *App) emitDevices(v settings.VoiceSettings) {
	f := map[string]any{}
	if v.InputDevice != nil {
		f["input"] = *v.InputDevice
	}
	if v.OutputDevice != nil {
		f["output"] = *v.OutputDevice
	}
	a.emit("devices", f)
}

// onVoiceEvent is the runtime's sink (runtime goroutine; never blocks for long).
func (a *App) onVoiceEvent(ev voice.UIEvent) {
	switch ev.Kind {
	case "state":
		a.setState(ev.State)
		a.pushTray()
		return
	case "status":
		a.mu.Lock()
		changed := a.voiceStatus != ev.Text
		a.voiceStatus = ev.Text
		a.mu.Unlock()
		a.emitRaw(ev)
		if changed {
			a.pushTray()
		}
		return
	case "mouth":
		// Non-zero only while Pixie's reply is audibly playing: feeds the agent's echo gate.
		if a.agent != nil {
			a.agent.OnMouth(ev.Level)
		}
	}
	a.emitRaw(ev)
}

func (a *App) emitRaw(ev voice.UIEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	a.deps.Emit(ev.Kind, b)
}

// --- pixie state ---

// currentPackLocked is the pack shown: the chosen one, else bit, else the first. Needs a.mu.
func (a *App) currentPackLocked() (*packs.Pack, bool) {
	p, ok := packs.Select(a.packs, a.wanted)
	if !ok {
		return nil, false
	}
	return &p, true
}

func (a *App) currentLevels() (uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.currentPackLocked()
	if !ok {
		return 0, false
	}
	return p.Mouth.Levels, true
}

// setState switches the pixie's state and tells the player; false for an unknown name.
func (a *App) setState(name string) bool {
	a.mu.Lock()
	ok := a.pixie.SetState(name)
	a.mu.Unlock()
	if ok {
		a.announce(name)
	}
	return ok
}

// announce tells the player about a state change. A non-looping state (e.g. happy) ends on its
// own in the player; the app mirrors the return to idle so the tray stays true.
func (a *App) announce(name string) {
	a.emit("state", map[string]any{"state": name})
	a.mu.Lock()
	p, _ := a.currentPackLocked()
	seq, after, ok := a.pixie.OneShot(p)
	a.mu.Unlock()
	if !ok {
		return
	}
	time.AfterFunc(after, func() {
		a.mu.Lock()
		done := a.pixie.FinishOneShot(seq)
		a.mu.Unlock()
		if done {
			a.emit("state", map[string]any{"state": "idle"})
			a.pushTray()
		}
	})
}

// Celebrate: Pixie plays happy (a new canvas page), only from idle. Safe from any goroutine.
func (a *App) Celebrate() {
	a.mu.Lock()
	p, _ := a.currentPackLocked()
	h, ok := a.pixie.Celebrate(p)
	a.mu.Unlock()
	a.showHeld("happy", h, ok)
}

// FlashError: Pixie shows error (a delegated job failed), then returns to where it was.
func (a *App) FlashError() {
	a.mu.Lock()
	p, _ := a.currentPackLocked()
	h, ok := a.pixie.FlashError(p)
	a.mu.Unlock()
	a.showHeld("error", h, ok)
}

func (a *App) showHeld(name string, h HeldState, ok bool) {
	if !ok {
		return
	}
	a.emit("state", map[string]any{"state": name})
	a.pushTray()
	time.AfterFunc(h.After, func() {
		a.mu.Lock()
		ended := a.pixie.EndHold(h)
		a.mu.Unlock()
		if ended {
			a.announce(h.Then)
			a.pushTray()
		}
	})
}

// Hotkey starts or ends a conversation; when the voice loop failed to build it shows why.
func (a *App) Hotkey() {
	if a.rt != nil {
		a.rt.Hotkey()
		return
	}
	if a.setupFail == nil {
		a.mu.Lock()
		name := a.pixie.ToggleListening()
		a.mu.Unlock()
		a.announce(name)
		a.pushTray()
		return
	}
	line := a.setupFail.Status()
	a.mu.Lock()
	a.voiceStatus = line
	a.mu.Unlock()
	a.emit("status", map[string]any{"text": line})
	a.setState("error")
	a.pushTray()
	time.AfterFunc(setupFailureHold, func() {
		a.setState("idle")
		a.pushTray()
	})
}

// talk sim port
type appMouth struct{ a *App }

func (m appMouth) Levels() uint32 {
	levels, ok := m.a.currentLevels()
	if !ok {
		return 2
	}
	return levels
}

func (m appMouth) VoiceActive() bool {
	m.a.mu.Lock()
	defer m.a.mu.Unlock()
	return m.a.voiceActive
}

func (m appMouth) Mouth(level uint32) { m.a.emit("mouth", map[string]any{"level": level}) }

// --- packs ---

// packPayload is the `pack` event's fields for the pack shown at scale (nil without a pack).
func (a *App) packPayload(scale uint32) map[string]any {
	a.mu.Lock()
	p, ok := a.currentPackLocked()
	var mask string
	var cached bool
	if ok {
		mask, cached = a.hitMasks[p.ID]
	}
	a.mu.Unlock()
	if !ok {
		return nil
	}
	if !cached {
		m, err := packs.HitMaskBase64(*p)
		if err != nil {
			log.Printf("packs: hit mask of %s: %v", p.ID, err)
		}
		mask = m
		a.mu.Lock()
		a.hitMasks[p.ID] = m
		a.mu.Unlock()
	}
	f := map[string]any{"pack": p, "sheetPath": p.SheetPath, "scale": scale}
	if mask != "" {
		f["hitMask"] = mask
	}
	return f
}

// showPack tells the voice loop and the pixie window about the pack shown now.
func (a *App) showPack() {
	f := a.packPayload(a.store.Current().Scale)
	if f == nil {
		return
	}
	levels, ok := a.currentLevels()
	a.mu.Lock()
	meter := a.meter
	a.mu.Unlock()
	if meter != nil {
		meter.SetMouthLevels(MouthLevels(levels, ok))
	}
	a.emit("pack", f)
}

// setMeter replaces the voice loop's mouth meter (tests).
func (a *App) setMeter(m MouthMeter) {
	a.mu.Lock()
	a.meter = m
	a.mu.Unlock()
}

// PackJSON is dp_pack_json: {"pack","sheetPath","scale","hitMask"} for the pack shown.
func (a *App) PackJSON() []byte {
	f := a.packPayload(a.store.Current().Scale)
	if f == nil {
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	return b
}

// selectPack: Tray ▸ Pixie. Saves the choice.
func (a *App) selectPack(id string) error {
	a.mu.Lock()
	_, ok := a.packs[id]
	if ok {
		a.wanted = id
	}
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pack %q is loaded", id)
	}
	a.save(func(c *settings.Config) { c.Pack = id })
	a.showPack()
	return nil
}

// reloadPacks: Tray ▸ Reload packs. Never saves: a missing chosen pack only falls back for now.
func (a *App) reloadPacks() {
	found, _ := packs.Load(a.roots...)
	a.mu.Lock()
	a.packs = found
	a.hitMasks = map[string]string{}
	a.mu.Unlock()
	a.showPack()
}

// setScale: Tray ▸ Size.
func (a *App) setScale(n uint32) {
	if n == 0 || a.store.Current().Scale == n {
		return
	}
	a.save(func(c *settings.Config) { c.Scale = n })
	if f := a.packPayload(n); f != nil {
		a.emit("pack", f)
	}
}

func (a *App) resetPosition() {
	a.save(func(c *settings.Config) { c.Position = nil })
	a.emit("position", nil)
}

// save is an app write (tray, drag, canvas geometry). The store merges it onto settings.json as
// it is on disk, so hand edits survive; a broken file is never overwritten (the change is kept
// in memory only). Callers outside Action emit settings.changed so the window re-reads.
func (a *App) save(fn func(*settings.Config)) {
	if err := a.store.Update(fn); err != nil {
		log.Printf("saving settings: %v", err)
	}
}

// --- tray ---

// State is the tray model.
func (a *App) State() AppState {
	cfg := a.store.Current()
	var agent AgentTray
	if a.agent != nil {
		agent = a.agent.Tray()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := AppState{
		State:        a.pixie.State,
		Packs:        make([]PackRef, 0, len(a.packs)),
		Scale:        int(cfg.Scale),
		Fade:         cfg.ProximityFade.Enabled,
		Talking:      a.pixie.Talking,
		CanvasRecent: append([]CanvasPage{}, a.canvasRecent...),
		Agent:        AgentState{Enabled: agent.Enabled, Jobs: JobItems(agent.Jobs)},
		StatusLines:  []string{},
		Hotkey:       cfg.Hotkey,
	}
	if agent.Enabled {
		s.AgentModeFix = agent.ModeFix
	}
	for id, p := range a.packs {
		s.Packs = append(s.Packs, PackRef{ID: id, Name: p.Name})
	}
	sort.Slice(s.Packs, func(i, j int) bool { return s.Packs[i].ID < s.Packs[j].ID })
	if p, ok := a.currentPackLocked(); ok {
		s.Pack = p.ID
	}
	for _, line := range []string{a.hotkeyError, a.voiceStatus} {
		if line != "" {
			s.StatusLines = append(s.StatusLines, line)
		}
	}
	if agent.Enabled {
		if line := AgentStatusLine(agent.Status, agent.Hint); line != "" {
			s.StatusLines = append(s.StatusLines, line)
		}
		if agent.Mode != "" {
			s.StatusLines = append(s.StatusLines, agent.Mode)
		}
	}
	return s
}

// Action runs a tray menu id (Rust actions::on_menu), then refreshes the tray and tells the
// Settings window (`settings.changed`), as Rust did after every menu event.
func (a *App) Action(id string) error {
	err := a.action(id)
	a.pushTray()
	a.emit("settings.changed", nil)
	return err
}

func (a *App) action(id string) error {
	switch id {
	case "talk":
		a.toggleTalking()
		return nil
	case "fade":
		a.save(func(c *settings.Config) { c.ProximityFade.Enabled = !c.ProximityFade.Enabled })
		return nil
	case "reload":
		a.reloadPacks()
		return nil
	case "reset-position":
		a.resetPosition()
		return nil
	case "canvas:open":
		if a.canvas == nil {
			return errors.New("the canvas is unavailable")
		}
		return a.canvas.OpenLast()
	case "canvas:folder":
		if a.pagesDir == "" {
			return errors.New("the canvas is unavailable")
		}
		if err := a.deps.Run("open", a.pagesDir); err != nil {
			return fmt.Errorf("canvas: cannot open %s: %w", a.pagesDir, err)
		}
		return nil
	}
	switch {
	case strings.HasPrefix(id, "state:"):
		name := strings.TrimPrefix(id, "state:")
		if !a.setState(name) {
			return fmt.Errorf("unknown state %q", name)
		}
		return nil
	case strings.HasPrefix(id, "pack:"):
		return a.selectPack(strings.TrimPrefix(id, "pack:"))
	case strings.HasPrefix(id, "size:"):
		n, err := strconv.ParseUint(strings.TrimPrefix(id, "size:"), 10, 32)
		if err != nil {
			return fmt.Errorf("bad size %q", id)
		}
		a.setScale(uint32(n))
		return nil
	case strings.HasPrefix(id, "canvas:page:"):
		if a.canvas == nil {
			return errors.New("the canvas is unavailable")
		}
		page, err := canvas.PageIDFromSlug(strings.TrimPrefix(id, "canvas:page:"))
		if err != nil {
			return nil // not a page id: nothing to open (Rust ignores it)
		}
		return a.canvas.Open(page)
	case strings.HasPrefix(id, "agent:"):
		if a.agent == nil {
			log.Printf("app: %s: delegation is off (agent.enabled = false)", id)
			return nil
		}
		if !a.agent.Menu(id) {
			return fmt.Errorf("unknown action %q", id)
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", id)
}

func (a *App) toggleTalking() {
	a.mu.Lock()
	a.pixie.Talking = !a.pixie.Talking
	on := a.pixie.Talking
	a.mu.Unlock()
	a.talkMu.Lock()
	defer a.talkMu.Unlock()
	if !a.talkClosed {
		a.talkSwitch <- on
	}
}

// --- settings ---

func (a *App) settingsEnv(p settings.Platform) settings.Env {
	home, _ := os.UserHomeDir()
	a.mu.Lock()
	pairs := make([]settings.Pair, 0, len(a.packs))
	for id, pk := range a.packs {
		pairs = append(pairs, settings.Pair{Value: id, Label: pk.Name})
	}
	a.mu.Unlock()
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Value < pairs[j].Value })
	return settings.Env{Home: home, Platform: p, Sources: settings.OptionSources{
		Packs:     pairs,
		Providers: ProviderNames(),
		Audio:     AudioBackends(),
		// The session host and terminals are P12/P13's; their names are fixed.
		Hosts:     []string{"herdr", "tmux"},
		Terminals: []string{"warp", "iterm"},
	}}
}

// secretStatus adapts the voice secrets to the settings window's key rows (found/missing and
// where from); the key itself never leaves the voice package.
type secretStatus struct{ s voice.SecretSource }

func (s secretStatus) Source(provider string) string {
	if named, ok := s.s.(interface{ Source(string) string }); ok {
		return named.Source(provider)
	}
	// A source that can't say where its keys come from (tests, embedders) counts as the
	// environment.
	if _, ok := s.s.APIKey(provider); ok {
		return voice.SourceEnv
	}
	return voice.SourceNone
}

// keychainSink takes the Keychain values the app read (voice.EnvSecrets).
type keychainSink interface{ SetKeychain(map[string]string) }

// applier is the settings Applier: a Live change takes the same path as the tray
// (Rust settings/live.rs AppApplier). It runs under the settings API lock.
type applier struct{ a *App }

func (ap applier) Apply(field string, cfg settings.Config) error {
	a := ap.a
	route, ok := settings.RouteFor(field)
	if !ok {
		return fmt.Errorf("%s applies after a restart", field)
	}
	var err error
	switch route {
	case settings.RouteHotkey:
		err = a.applyHotkey(cfg.Hotkey)
	case settings.RoutePack:
		a.mu.Lock()
		_, loaded := a.packs[cfg.Pack]
		if loaded {
			a.wanted = cfg.Pack
		}
		a.mu.Unlock()
		if !loaded {
			err = errors.New("no such pack is loaded")
		} else {
			a.showPack()
		}
	case settings.RouteScale:
		if f := a.packPayload(cfg.Scale); f != nil {
			a.emit("pack", f)
		}
	case settings.RouteReadEachTick, settings.RouteNotifications:
		// Swift's proximity loop and the updates bubble keep their own copy: tell them to re-read.
		a.emit("settings.changed", nil)
	case settings.RouteAutoOpen:
		if a.canvas == nil {
			err = errors.New("the canvas is unavailable")
		} else {
			a.canvas.SetAutoOpen(cfg.Canvas.AutoOpen)
		}
	case settings.RouteResetPosition:
		a.resetPosition()
	case settings.RouteNoiseFilter:
		a.cleaner.SetEnabled(cfg.Voice.NoiseFilter)
	case settings.RouteAudioDevices:
		a.emitDevices(cfg.Voice)
	}
	// The tray shows the size, pack and fade checkmarks and the hotkey line.
	a.pushTray()
	return err
}

// applyHotkey: Swift binds shortcuts. On the settings.set path it has already bound combo
// (bound == combo): nothing to do. On the reload path (a hand edit) Go asks it to with a
// `hotkey` event; if it can't, it calls settings.hotkeyFailed. A combo it refused before is
// refused again here, so the settings service keeps the old one and warns.
func (a *App) applyHotkey(combo string) error {
	a.mu.Lock()
	h := &a.hotkey
	if combo == h.bound {
		a.mu.Unlock()
		return nil
	}
	if combo == h.refused {
		a.mu.Unlock()
		return errors.New("that shortcut can't be bound")
	}
	h.prev, h.bound, h.pending = h.bound, combo, combo
	a.mu.Unlock()
	a.emit("hotkey", map[string]any{"combo": combo})
	return nil
}

// setSetting is settings.set: Swift binds a hotkey before saving it, so the Applier must not
// ask it again; a refused save puts the old binding back in the books.
func (a *App) setSetting(field string, value json.RawMessage) settings.Snapshot {
	var combo string
	isHotkey := field == "hotkey" && json.Unmarshal(value, &combo) == nil
	var old string
	if isHotkey {
		a.mu.Lock()
		old, a.hotkey.bound = a.hotkey.bound, combo
		a.mu.Unlock()
	}
	snap := a.api.Set(field, value)
	if isHotkey {
		a.mu.Lock()
		if snap.SetError != nil {
			if a.hotkey.bound == combo {
				a.hotkey.bound = old
			}
		} else {
			a.hotkeyError = "" // the bind worked (Rust bind_hotkey clears hotkey_error)
			a.hotkey.pending = ""
			if a.hotkey.refused == combo {
				a.hotkey.refused = ""
			}
		}
		a.mu.Unlock()
		a.pushTray()
	}
	return snap
}

// hotkeyFailed is settings.hotkeyFailed: Swift could not bind combo. For a combo Go asked for
// (a hand edit), the old one stays and the Settings window warns. Otherwise it is the startup
// binding: the tray shows "Hotkey unavailable: <combo>".
func (a *App) hotkeyFailed(combo string) {
	a.mu.Lock()
	h := &a.hotkey
	if combo != "" && combo == h.pending && combo == h.bound && h.prev != "" {
		h.refused, h.bound, h.pending = combo, h.prev, ""
		keep := h.bound
		a.mu.Unlock()
		if cur := a.store.Current(); cur.Hotkey == combo {
			cur.Hotkey = keep
			a.store.Adopt(cur)
		}
		a.api.Reload() // the service sees the hand edit again, is refused, and warns
		a.emit("settings.changed", nil)
		a.pushTray()
		return
	}
	a.hotkeyError = "Hotkey unavailable: " + combo
	a.mu.Unlock()
	a.pushTray()
}

// --- dp_call ---

func decodeArgs(method string, args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return nil
}

// Register adds every app method and the settings.* methods.
func (a *App) Register(r Registrar) {
	a.api.Register(r)
	r.Register("settings.set", func(args json.RawMessage) (any, error) {
		var in struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		}
		if err := decodeArgs("settings.set", args, &in); err != nil {
			return nil, err
		}
		if in.Field == "" {
			return nil, errors.New("settings.set: missing field")
		}
		if in.Value == nil {
			in.Value = json.RawMessage("null")
		}
		return a.setSetting(in.Field, in.Value), nil
	})
	r.Register("settings.hotkeyFailed", func(args json.RawMessage) (any, error) {
		var in struct {
			Combo string `json:"combo"`
		}
		if err := decodeArgs("settings.hotkeyFailed", args, &in); err != nil {
			return nil, err
		}
		a.hotkeyFailed(in.Combo)
		return struct{}{}, nil
	})
	// secrets.keychain {"values": {VAR: key}}: the API keys stored in the Keychain, read by the
	// app at startup and after each save/remove. Replaces the previous set; the next connect
	// uses them. Never logged.
	r.Register("secrets.keychain", func(args json.RawMessage) (any, error) {
		var in struct {
			Values map[string]string `json:"values"`
		}
		if err := decodeArgs("secrets.keychain", args, &in); err != nil {
			return nil, err
		}
		sink, ok := a.deps.Secrets.(keychainSink)
		if !ok {
			return nil, errors.New("secrets.keychain: this secret source takes no Keychain values")
		}
		sink.SetKeychain(in.Values)
		return nil, nil
	})
	r.Register("app.state", func(json.RawMessage) (any, error) { return a.State(), nil })
	r.Register("app.action", func(args json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := decodeArgs("app.action", args, &in); err != nil {
			return nil, err
		}
		return struct{}{}, a.Action(in.ID)
	})
	r.Register("pixie.click", func(json.RawMessage) (any, error) {
		a.OpenAgentTerminal()
		return struct{}{}, nil
	})
	r.Register("pixie.moved", func(args json.RawMessage) (any, error) {
		var in struct{ X, Y float64 }
		if err := decodeArgs("pixie.moved", args, &in); err != nil {
			return nil, err
		}
		a.save(func(c *settings.Config) { c.Position = &[2]float64{in.X, in.Y} })
		a.emit("settings.changed", nil)
		return struct{}{}, nil
	})
	r.Register("canvas.resolve", func(args json.RawMessage) (any, error) {
		var in struct {
			Path string `json:"path"`
		}
		if err := decodeArgs("canvas.resolve", args, &in); err != nil {
			return nil, err
		}
		if a.resolver == nil {
			return nil, errors.New("the canvas is unavailable")
		}
		return a.resolver.Resolve(in.Path)
	})
	r.Register("canvas.geometry", func(args json.RawMessage) (any, error) {
		var g settings.CanvasWindow
		if err := decodeArgs("canvas.geometry", args, &g); err != nil {
			return nil, err
		}
		a.save(func(c *settings.Config) { c.Canvas.Window = &g })
		a.emit("settings.changed", nil)
		return struct{}{}, nil
	})
	r.Register("agent.updates", func(json.RawMessage) (any, error) {
		var lines any = []any{}
		if a.agent != nil {
			lines = a.agent.Updates()
		}
		return map[string]any{"lines": lines}, nil
	})
	r.Register("agent.updatesSeen", func(args json.RawMessage) (any, error) {
		var in struct {
			IDs []uint64 `json:"ids"`
		}
		if err := decodeArgs("agent.updatesSeen", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.UpdatesSeen(in.IDs)
		}
		return struct{}{}, nil
	})
	r.Register("agent.decide", func(args json.RawMessage) (any, error) {
		var in struct {
			Token string `json:"token"`
			Allow bool   `json:"allow"`
		}
		if err := decodeArgs("agent.decide", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.Decide(in.Token, in.Allow)
		}
		return struct{}{}, nil
	})
	r.Register("agent.choose", func(args json.RawMessage) (any, error) {
		var in struct {
			Token  string `json:"token"`
			Option int    `json:"option"`
		}
		if err := decodeArgs("agent.choose", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.Choose(in.Token, in.Option)
		}
		return struct{}{}, nil
	})
	r.Register("agent.focusJob", func(args json.RawMessage) (any, error) {
		var in struct {
			Job    string `json:"job"`
			Worker string `json:"worker"`
		}
		if err := decodeArgs("agent.focusJob", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.FocusJob(in.Job, in.Worker)
		}
		return struct{}{}, nil
	})
	r.Register("agent.roster", func(json.RawMessage) (any, error) {
		// A struct, not a map, so the keys keep this order on the wire.
		var view any = struct {
			Agents     []any `json:"agents"`
			Yours      []any `json:"yours"`
			AgentCount int   `json:"agent_count"`
			YourCount  int   `json:"your_count"`
		}{Agents: []any{}, Yours: []any{}}
		if a.agent != nil {
			view = a.agent.Roster()
		}
		return map[string]any{"roster": view}, nil
	})
	r.Register("agent.focusAgent", func(args json.RawMessage) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decodeArgs("agent.focusAgent", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil && in.Name != "" {
			a.agent.FocusAgent(in.Name)
		}
		return struct{}{}, nil
	})
	r.Register("presence.set", func(args json.RawMessage) (any, error) {
		var in struct {
			Away bool `json:"away"`
		}
		if err := decodeArgs("presence.set", args, &in); err != nil {
			return nil, err
		}
		if a.agent != nil {
			a.agent.SetAway(in.Away)
		}
		return struct{}{}, nil
	})
	r.Register("audio.deviceChanged", func(json.RawMessage) (any, error) {
		if a.rt != nil {
			a.rt.AudioFailed(voice.AudioError{Kind: voice.AudioDeviceChanged})
		}
		return struct{}{}, nil
	})
	r.Register("audio.failed", func(args json.RawMessage) (any, error) {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decodeArgs("audio.failed", args, &in); err != nil {
			return nil, err
		}
		if a.rt != nil {
			a.rt.AudioFailed(voice.AudioError{Kind: voice.AudioOther, Msg: in.Reason})
		}
		return struct{}{}, nil
	})
}

func startAndReap(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("app: %s did not finish: %v", program, err)
		}
	}()
	return nil
}

// OpenAgentTerminal is a single click on Pixie (Rust agent::open_terminal): attach a terminal to
// the agent session. With delegation off, nothing opens.
func (a *App) OpenAgentTerminal() {
	if a.agent == nil {
		log.Printf("agent: delegation is off (agent.enabled = false); nothing to open")
		return
	}
	a.agent.OpenTerminal()
}
