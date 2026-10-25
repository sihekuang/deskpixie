// Package agentapp is the composition root for delegation (slice 4 spec §7): the ONLY app code
// that names concrete agent types (herdr, the hook server, agent-fs, the terminals). Everything
// is picked from registries and injected into the agent runtime; the app sees it through
// app.Agent. Port of ai-mascot src-tauri/src/agent/{mod.rs, files.rs, workers.rs, registry.rs,
// notify.rs, echo.rs, store.rs, voice_link.rs}.
//
// Logging rule (R-l): never log a Notice, VoiceSignal, HookEvent, token or agent text.
package agentapp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/agent/terminal"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/pix/wire"
	"github.com/sihekuang/deskpixie/core/pixhost"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux"
	"github.com/sihekuang/deskpixie/core/voice"
)

// IntegrationHint is the tray line when Herdr's Claude integration is missing (R-h).
// Delegation still works.
const IntegrationHint = app.IntegrationHint

// serverWait bounds how long Shutdown waits for the hook server and runtime goroutines.
const serverWait = 2 * time.Second

// systemClock is the wall clock in Unix ms.
type systemClock struct{}

func (systemClock) NowMs() agent.Millis { return agent.Millis(time.Now().UnixMilli()) }

// RandomIDs are job ids from the OS random source.
type RandomIDs struct{}

func (RandomIDs) JobID() agent.JobID {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("the OS random source works: " + err.Error())
	}
	return agent.JobIDFromBits(binary.LittleEndian.Uint64(b[:]))
}

// DelegationTools are the voice tools over one handle.
func DelegationTools(h *agent.AgentHandle) []voice.Tool {
	return []voice.Tool{agent.Delegate{H: h}, agent.Jobs{H: h}, agent.AnswerJob{H: h}, agent.FocusSessionTool{H: h}}
}

// PermissionTimeoutMs is agent.permissionTimeoutSecs in ms, kept under the hook server's own
// PermissionLimit (122 s) so the controller's decision (a deny on timeout) is the one Claude
// Code gets.
func PermissionTimeoutMs(secs uint64) agent.Millis {
	limit := uint64(httpserver.PermissionLimit/time.Second) - 2
	return agent.Millis(min(max(secs, 1), limit) * 1000)
}

// UpdateQuietMs is notifications.debounceSecs in ms.
func UpdateQuietMs(n settings.NotificationSettings) agent.Millis {
	return agent.Millis(n.DebounceSecs) * 1000
}

// HostProblem is the tray status line when the configured session host can't be built (a
// sticky setup disable).
func HostProblem(err error) string {
	var he agent.HostError
	if errors.As(err, &he) && he.Kind == agent.HostNotFound {
		return "Coordinator: " + he.HostName() + " not found"
	}
	return "Coordinator: invalid settings (see log)"
}

// TerminalFailureStatus is the tray status line for an OpenTerminal failure.
func TerminalFailureStatus(err string) string {
	if what, _, ok := strings.Cut(err, ":"); ok && strings.HasPrefix(what, "could not open ") {
		return "Coordinator: " + what
	}
	return "Coordinator: " + err
}

// Config is what Build needs. The zero test seams pick the real adapters.
type Config struct {
	Settings      settings.AgentSettings
	Notifications settings.NotificationSettings
	// ReplyHistoryMins is how long the reply deck keeps Pixie's earlier replies (replyHistoryMins).
	ReplyHistoryMins uint32
	// AppSupport is the app-data folder; Home the user's home folder.
	AppSupport, Home string
	Hooks            app.AgentHooks

	Runner    agent.CommandRunner        // nil = proc.ProcessRunner{}
	Environ   []string                   // nil = os.Environ()
	Hosts     map[string]HostFactory     // nil = SessionHosts()
	Terminals map[string]TerminalFactory // nil = Terminals()
	Clock     agent.Clock                // nil = the wall clock
	IDs       agent.IdSource             // nil = RandomIDs
	// Listen binds the hook/MCP server; nil = httpserver.Listen (127.0.0.1:port).
	Listen func(port uint16) (net.Listener, error)
	// Pix is the pix this app ships, whose build a session's pix must be to be current (#189);
	// "" = BundledPix of the running app. A path with no file marks no session outdated.
	Pix string
	// PixSocket is where sessions you start with pix connect (#89); "" = wire.SocketPath
	// under AppSupport.
	PixSocket string
}

// Agent is the running delegation agent (app.Agent).
type Agent struct {
	handle  *agent.AgentHandle
	link    *VoiceLink
	session agent.SessionHost
	pixSock string // where pix sessions connect; "" when not listening
	paths   Paths
	hooks   app.AgentHooks
	addr    string
	attach  *attach.Store
	feed    *threadFeed // the chat thread's feed (#302); nil until built

	// toAgent receives the voice signals the echo gate admits (handle.Voice; tests replace it).
	toAgent func(voice.VoiceSignal)

	echoMu sync.Mutex
	echo   EchoGate

	mu           sync.Mutex
	status, hint string
	// Manual mode (#97): which agents are flagged, and why (set once per change, off the
	// controller's goroutine, since the probes run a program).
	manual map[string]bool
	causes map[string]agent.ManualCause // per flagged agent, computed from the model it started with
	gens   map[string]int               // per agent: invalidates a probe still running for an older flag

	// model is the coordinator's --model ("" = Claude Code's default). The probes are seams:
	// claudeListsAuto runs `claude --help` (unknown when it can't tell), managedOff reads the
	// managed settings file.
	model           string
	kind            agent.AgentKind   // the coordinator's agent
	coordModel      string            // the --model the Claude coordinator was started with
	workerModel     map[string]string // the --model each Claude worker was started with
	recipes         agent.WorkerRecipes
	claudeListsAuto func() agent.ClaudeAuto
	managedOff      func() bool
	restart         func() // the coordinator restart (tests replace it)

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	shutOnce sync.Once
}

var _ app.Agent = (*Agent)(nil)

// Factory is app.AgentFactory: nil (a nil interface) when agent.enabled is false.
func Factory(cfg settings.Config, appSupport string, hooks app.AgentHooks) app.Agent {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Print("agent: no home dir; delegation unavailable")
		return nil
	}
	a := Build(Config{Settings: cfg.Agent, Notifications: cfg.Notifications, ReplyHistoryMins: cfg.ReplyHistoryMins, AppSupport: appSupport, Home: home, Hooks: hooks})
	if a == nil {
		return nil
	}
	return a
}

// Build builds and starts delegation from settings; nil when agent.enabled is false. Call it
// once, before the voice loop (whose tools and persona come from here).
func Build(c Config) *Agent {
	s := c.Settings
	if !s.Enabled {
		return nil
	}
	if c.AppSupport == "" || c.Home == "" {
		log.Print("agent: no app data or home dir; delegation unavailable")
		return nil
	}
	if c.Runner == nil {
		c.Runner = proc.ProcessRunner{}
	}
	if c.Hosts == nil {
		c.Hosts = SessionHosts()
	}
	if c.Terminals == nil {
		c.Terminals = Terminals()
	}
	if c.Clock == nil {
		c.Clock = systemClock{}
	}
	if c.IDs == nil {
		c.IDs = RandomIDs{}
	}
	if c.Listen == nil {
		c.Listen = func(port uint16) (net.Listener, error) { return httpserver.Listen(int(port)) }
	}
	a := &Agent{paths: NewPaths(c.AppSupport), hooks: c.Hooks, manual: map[string]bool{}, causes: map[string]agent.ManualCause{},
		gens: map[string]int{}, workerModel: map[string]string{},
		managedOff: managedOff}
	home, environ := c.Home, c.Environ
	if environ == nil {
		environ = os.Environ()
	}
	a.claudeListsAuto = func() agent.ClaudeAuto { return claudeAuto(home, environ, isFile, runHelp) }
	a.link = &VoiceLink{refresh: c.Hooks.RefreshTray}
	paths := a.paths

	// Settings, CLAUDE.md and MCP configs live only under app data (R-k); trust is granted only
	// for the app's own workspace (and, for workers, their worktrees under the projects root).
	problem := ""
	setProblem := func(line string) {
		if problem == "" {
			problem = line
		}
	}
	// A workers problem turns only workers off (a tray notice): the coordinator still works solo.
	// With workers on, the projects folder is created when missing, else DeskPixie's own is
	// used (#216). With them off nothing is created (~/Documents is privacy-protected, and a
	// folder nobody asked for is clutter): the terminal starts there only if it already exists.
	folder := UsedFolder{}
	if s.Workers.Enabled {
		folder = UseProjectsFolder(s.ProjectsRoot, c.Home, ProjectsFallback(paths))
	} else if root, err := ProjectsRoot(s.ProjectsRoot, c.Home); err == nil {
		folder = UsedFolder{Root: root}
	}
	ws := SetupWorkers(s.Workers, folder)
	workersOn := ws.Policy.Enabled
	// tmux detects no agent status: Claude agents report theirs through hooks.
	if err := InstallFor(paths, s.Port, ws.Root, s.SessionHost == tmux.Name); err != nil {
		log.Printf("agent: invalid settings: %v", err)
		setProblem("Coordinator: invalid settings (see log)")
	}
	// MCP configs left by an earlier run hold its (now dead) tokens.
	agentfs.ClearMcpDir(paths.McpDir)
	trust := agentfs.NewClaudeTrustStore(filepath.Join(c.Home, ".claude.json"), []string{paths.Workspace})
	if _, err := trust.Grant(paths.Workspace); err != nil {
		log.Printf("agent: could not pre-trust the workspace: %v", err)
	}

	hd := HostDeps{Settings: s, Home: c.Home, Runner: c.Runner, Hint: a.setHint, Environ: c.Environ}
	var session agent.SessionHost
	if factory, ok := c.Hosts[s.SessionHost]; ok {
		h, err := factory(hd)
		if err != nil {
			log.Printf("agent: %s: %v", s.SessionHost, err)
			// herdr missing or a bad setting (e.g. the session name) turns delegation off at once.
			setProblem(HostProblem(err))
			h = Unavailable{Err: err}
		}
		session = h
	} else {
		log.Printf("agent: unknown sessionHost %q", s.SessionHost)
		setProblem("Coordinator: invalid settings (see log)")
		session = Unavailable{Err: agent.HostError{Kind: agent.HostOther, Detail: "unknown sessionHost " + s.SessionHost}}
	}
	// Sessions you start yourself with pix (#89) sit next to the primary host's agents.
	if c.PixSocket == "" {
		c.PixSocket = wire.SocketPath(c.AppSupport, os.Getuid(), os.TempDir())
	}
	primary := session
	if c.Pix == "" {
		if exe, err := os.Executable(); err == nil {
			c.Pix = BundledPix(exe)
		}
	}
	ph := pixhost.New(c.PixSocket, func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var names []string
		// Passive: a stopped primary has no agents whose names could clash, and a pix hello
		// must not start it.
		if list, err := agent.ListRunning(ctx, primary); err == nil {
			for _, ag := range list {
				names = append(names, ag.Name)
			}
		}
		return names
	})
	ph.SetBuild(wire.BuildOf(c.Pix))
	ph.SetQuestionWait(time.Duration(s.QuestionWaitSecs) * time.Second)
	if err := ph.Listen(); err != nil {
		log.Printf("agent: pix: %v", err) // delegation still works without it
	} else {
		session = agent.NewCompositeHost(primary, ph)
		a.pixSock = c.PixSocket
	}
	a.session = session
	termFactory, ok := c.Terminals[s.Terminal]
	if !ok {
		log.Printf("agent: unknown terminal %q; using warp", s.Terminal)
		termFactory = c.Terminals["warp"]
	}

	// What the last quit left running (keepPanesOnQuit): read once, whatever the setting is now.
	kept := LoadKept(paths.Kept())
	if !s.KeepPanesOnQuit {
		kept = agent.KeptAgents{}
	}
	if !workersOn {
		kept.Workers = nil
	}
	// The coordinator's agent (#87): a kept coordinator of the other kind is replaced.
	kind := CoordinatorKind(s.CoordinatorKind)
	model, codexModel := "", ""
	if s.Model != nil {
		model = *s.Model
	}
	if s.CodexModel != nil {
		codexModel = *s.CodexModel
	}
	// tmux detects no agent status: the coordinator reports its own through hooks.
	recipe := CoordinatorRecipeFor(kind, paths, model, codexModel, s.Port, s.SessionHost == tmux.Name)
	recipe.Args = append(recipe.Args, EffortArgs(kind, s.CoordinatorEffort)...)
	if kind == agent.Codex {
		recipe.Stamp = CoordinatorStamp(recipe, filepath.Join(paths.Workspace, "AGENTS.md"))
	} else {
		recipe.Stamp = CoordinatorStamp(recipe, filepath.Join(paths.Workspace, "CLAUDE.md"), paths.Settings)
	}
	kept = KeptFor(kept, kind, recipe.Stamp)
	tokens := httpserver.NewTokens()
	coordinatorToken := kept.CoordinatorToken
	if coordinatorToken != "" {
		// The kept coordinator's hooks and MCP calls still carry it.
		tokens.Restore(agent.Coordinator, coordinatorToken)
	} else {
		coordinatorToken = tokens.Issue(agent.Coordinator)
	}
	// Always: besides the worker tools (workers on), the pixie MCP server serves the coordinator
	// Pixie's local tools (timer, weather, music, time, math). Codex takes it by -c overrides
	// with the bearer from its env: no file.
	if kind == agent.Claude {
		if err := WriteCoordinatorMcp(paths, s.Port, coordinatorToken); err != nil {
			log.Printf("agent: cannot write the coordinator's MCP config: %v", err)
			setProblem("Coordinator: invalid settings (see log)")
		}
	}
	recipe.Env = []agent.EnvVar{{Key: agent.TokenEnv, Value: coordinatorToken}}
	// A coordinator found running is the kept one: adopted as it is.
	recipe.AdoptEarlier = kept.CoordinatorToken != ""
	recipe.ResumeLast = true
	feed := newThreadFeed(c.Hooks)
	a.feed = feed
	deps := agent.Deps{
		Host:        agent.NewAgentHost(session, c.Clock, recipe),
		Voice:       a.link,
		Notifier:    NewNotifier(c.Runner, c.Hooks.Notify).WithUpdates(c.Hooks.NotifyUpdate),
		Pixie:       pixieCue{c.Hooks},
		Status:      statusSink{a},
		Planner:     termFactory(c.Runner, c.Home),
		Sessions:    terminal.NewSessionPlanner(c.Runner),
		Focus:       focusSink{c.Hooks},
		TerminalDir: TerminalDir(folder, c.Home),
		Store:       NewJsonlStore(paths.Root),
		Clock:       c.Clock,
		IDs:         c.IDs,
		Kept:        KeptFile(paths.Kept()),
		Updates:     updatesSink{hooks: c.Hooks, feed: feed},
		Lines:       lineSink{feed},
		Delivered:   lineSink{feed},
		Replies:     replySink{c.Hooks},
		Panes:       paneSink{c.Hooks},
		Roster:      rosterSink{hooks: c.Hooks, feed: feed},
		Modes:       a,
	}
	a.attach = attach.New(paths.Attachments, filepath.Join(paths.Root, "attachment-copies.jsonl"), time.Now)
	a.attach.Sweep()
	deps.Attachments = a.attach
	if c.Hooks.Chat != nil {
		deps.Chat = chatSink{hooks: c.Hooks, feed: feed}
	}
	// Exactly 127.0.0.1 (the hook server refuses any non-loopback listener). Bound before the
	// runtime is built, so a busy port is known before a warm start could launch anything.
	ln, err := c.Listen(s.Port)
	if err != nil {
		log.Printf("agent: cannot listen on 127.0.0.1:%d: %v", s.Port, err)
		setProblem("Coordinator: hook port busy")
		ln = nil
	}
	spoken := agent.SpokenPaths{Home: c.Home}
	if workersOn {
		spoken.ProjectsRoot = ws.Root
	}
	cfg := agent.RuntimeConfig{
		Settings: agent.Settings{
			NotifyPerMinute:     int(max(s.NotifyPerMinute, 1)),
			PermissionTimeoutMs: PermissionTimeoutMs(s.PermissionTimeoutSecs),
			Narration:           s.Narration,
			UpdateQuietMs:       UpdateQuietMs(c.Notifications),
			KeepPanesOnQuit:     s.KeepPanesOnQuit,
			Workers:             ws.Policy,
			SpeakWorkerReports:  s.Workers.SpeakResults,
			ReplyHistoryMs:      agent.Millis(c.ReplyHistoryMins) * 60_000,
			SessionTurns:        c.Notifications.SessionTurns,
			SessionTurnsMinMs:   agent.Millis(c.Notifications.SessionTurnsMinSeconds) * 1000,
			OpenConversation:    s.OpenConversationForUpdates,
			AlwaysForceSend:     s.AlwaysForceSend,
			CoordinatorKind:     kind,
			SpokenPaths:         spoken,
		},
		Warm: s.Warm,
		// Stepped before the warm start: a disabled app never starts the coordinator.
		SetupProblem: problem,
		Notice:       ws.Notice,
		Kept:         kept,
	}
	var workerDeps *agent.WorkerDeps
	if workersOn {
		workerDeps = WorkerAdapters(paths, ws.Root, c.Home, s.Port, a.currentModel, tokens)
		workerDeps.Recipes = &modelRecipes{WorkerRecipes: workerDeps.Recipes, a: a}
		a.recipes = workerDeps.Recipes
	}
	a.model, a.kind = model, kind
	if kind == agent.Claude {
		a.coordModel = model
	}
	a.handle = agent.NewRuntimeWithWorkers(deps, cfg, workerDeps)
	a.restart = a.handle.Restart
	a.toAgent = a.handle.Voice
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.wg.Go(func() { a.handle.Run(a.ctx) })
	if ln != nil {
		a.addr = ln.Addr().String()
		a.wg.Go(func() {
			opts := httpserver.Options{CoordinatorTools: CoordinatorLocalTools(c.Hooks.LocalTools), NoWorkerTools: !workersOn}
			if err := httpserver.ServeWith(a.ctx, ln, tokens, a.handle, opts); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("agent: hook server stopped: %v", err)
			}
		})
	}
	return a
}

// Addr is the hook server's address ("" when it could not bind).
func (a *Agent) Addr() string { return a.addr }

func (a *Agent) Tools() []voice.Tool { return DelegationTools(a.handle) }
func (a *Agent) Persona() string     { return agent.DelegationPersona }

// Facts is Pixie's memory for each voice session: the coordinator's recent jobs (app.AgentFacts).
func (a *Agent) Facts() string { return a.handle.Facts() }

// Tray is Tray ▸ Coordinator: the open jobs, the runtime's status line and the standing hint.
func (a *Agent) Tray() app.AgentTray {
	var rows []app.JobRow
	for _, j := range a.handle.OpenJobs() {
		rows = append(rows, app.JobRow{ID: j.JobID, Status: j.Status.String(), Task: j.Task, AgeSecs: j.AgeSecs})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t := app.AgentTray{Enabled: true, Jobs: rows, Status: a.status, Hint: a.hint}
	subject, name := "A worker", ""
	if a.manual[agent.Coordinator] {
		subject, name = "Coordinator", agent.Coordinator
	} else {
		for w := range a.manual {
			if _, known := a.causes[w]; known && (name == "" || w < name) {
				name = w
			}
		}
	}
	if cause, ok := a.causes[name]; ok && a.manual[name] {
		t.Mode = subject + " is in Manual mode: " + cause.Reason()
		switch {
		case cause == agent.CauseOldModel && !agent.ModelSupportsAuto(a.model):
			// Offered only while the setting still names the old model: afterwards it is applied.
			t.ModeFix = &app.TrayFix{ID: "agent:fix-model", Title: "Use a supported model"}
		case cause == agent.CauseOldClaude:
			t.ModeFix = &app.TrayFix{Title: "Copy update command", Copy: "claude update"}
		}
	}
	return t
}

// ManualMode is agent.ModeSink: a Claude agent entered or left Manual mode. The flag is kept at
// once; the cause is worked out off the caller's goroutine (the controller's effect loop must
// not wait on `claude --help`) and the tray refreshed when it is known.
func (a *Agent) ManualMode(name string, manual bool) {
	a.mu.Lock()
	if manual == a.manual[name] {
		a.mu.Unlock()
		return
	}
	a.gens[name]++
	gen := a.gens[name]
	if manual {
		a.manual[name] = true
	} else {
		delete(a.manual, name)
		delete(a.causes, name)
	}
	// The cause uses the model this agent was started with, not the setting now.
	model := a.model
	if name == agent.Coordinator {
		model = a.coordModel
	} else if m, ok := a.workerModel[name]; ok {
		model = m
	}
	lists, off := a.claudeListsAuto, a.managedOff
	a.mu.Unlock()
	call(a.hooks.RefreshTray)
	if !manual || a.ctx.Err() != nil {
		return // quitting: no new goroutine beside Shutdown's wait
	}
	a.wg.Go(func() {
		c := agent.ManualCauseOf(model, lists(), off())
		a.mu.Lock()
		if a.gens[name] != gen {
			a.mu.Unlock()
			return // changed since: its own probe answers
		}
		a.causes[name] = c
		a.mu.Unlock()
		call(a.hooks.RefreshTray)
	})
}

// currentModel is agent.model now (read by each Claude worker's spawn).
func (a *Agent) currentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// modelRecipes records the model each Claude worker was started with.
type modelRecipes struct {
	agent.WorkerRecipes
	a *Agent
}

func (m *modelRecipes) Prepare(l agent.WorkerLaunch) (agent.PreparedWorker, error) {
	model := m.a.currentModel()
	p, err := m.WorkerRecipes.Prepare(l)
	if err == nil && l.Kind == agent.Claude {
		m.a.mu.Lock()
		m.a.workerModel[l.Name] = model
		m.a.mu.Unlock()
	}
	return p, err
}

func (m *modelRecipes) Discard(name string) {
	m.a.mu.Lock()
	delete(m.a.workerModel, name)
	m.a.mu.Unlock()
	m.WorkerRecipes.Discard(name)
}

// setHint shows a host hint (IntegrationHint) in the tray, logged once per line.
func (a *Agent) setHint(line string) {
	a.mu.Lock()
	changed := a.hint != line
	a.hint = line
	a.mu.Unlock()
	if changed {
		log.Printf("agent: %s (Herdr's status detection is degraded without it)", line)
		call(a.hooks.RefreshTray)
	}
}

func (a *Agent) setStatus(line string) {
	a.mu.Lock()
	a.status = line
	a.mu.Unlock()
	call(a.hooks.RefreshTray)
}

// statusSink is the runtime's tray status line (the controller's fixed texts, never agent text).
type statusSink struct{ a *Agent }

func (s statusSink) SetStatus(line string) {
	if line != "" {
		log.Printf("agent: status: %s", line)
	}
	s.a.setStatus(line)
}

// Menu runs Tray ▸ Coordinator items; false for ids that are not the agent's.
func (a *Agent) Menu(id string) bool {
	switch id {
	case "agent:open":
		a.OpenTerminal()
	case "agent:restart":
		a.handle.Restart()
	case "agent:fix-model":
		// Inside DeskPixie, so it runs directly (#80): agent.model back to Claude Code's default.
		// New Claude workers read it at spawn; running workers are left alone (their flag clears
		// when they end). The coordinator restarts only if it is a Claude one that is itself in
		// Manual mode.
		a.mu.Lock()
		a.model = ""
		claude := a.kind == agent.Claude
		restart := claude && a.manual[agent.Coordinator]
		if claude {
			a.coordModel = ""
		}
		a.mu.Unlock()
		if claude {
			a.handle.ClearCoordinatorModel()
		}
		call(a.hooks.ClearModel)
		if restart {
			a.restart()
		}
		call(a.hooks.RefreshTray)
	default:
		job, ok := strings.CutPrefix(id, "agent:cancel:")
		if !ok {
			return false
		}
		if _, err := a.handle.Cancel(job); err != nil {
			log.Printf("agent: cancel %s: %v", job, err)
		}
	}
	return true
}

// OpenTerminal is a single click on Pixie / Tray ▸ Open coordinator terminal (async). A failure
// becomes the tray status line.
func (a *Agent) OpenTerminal() {
	if a.ctx.Err() != nil {
		return // quitting
	}
	a.wg.Go(func() {
		if err := a.handle.OpenTerminal(a.ctx); err != nil {
			log.Printf("agent: open terminal: %v", err)
			a.setStatus(TerminalFailureStatus(err.Error()))
		}
	})
}

// Decide: a tap on Allow or Deny in the bubble for the question with this confirm word.
func (a *Agent) Decide(token string, allow bool) { a.handle.Decide(token, allow) }

// Choose: a tap on option (from 1) of a session menu in the bubble (#105).
func (a *Agent) Choose(token string, option int) { a.handle.Choose(token, option) }
func (a *Agent) TypedAnswer(token, text string)  { a.handle.TypedAnswer(token, text) }
func (a *Agent) ChatAbout(token string)          { a.handle.ChatAbout(token) }

// UpdatesSeen: the updates bubble showed these lines long enough to count as heard.
func (a *Agent) UpdatesSeen(ids []uint64) { a.handle.UpdatesSeen(ids) }

// RepliesShown: the reply deck showed these cards, so they count as delivered.
func (a *Agent) RepliesShown(ids []uint64) { a.handle.RepliesShown(ids) }

// PendingReplies is the deck's cards not yet shown ([]agent.ReplyCard, never nil).
func (a *Agent) PendingReplies() any { return a.handle.PendingReplies() }

// UpdatesDismissed: the user clicked these lines away (#199).
func (a *Agent) UpdatesDismissed(ids []uint64) { a.handle.UpdatesDismissed(ids) }

// Updates is the bubble's current list ([]agent.UpdateItem, never nil).
func (a *Agent) Updates() any { return a.handle.Updates() }

// SetAway is the shell's presence (screen locked, displays asleep or idle).
func (a *Agent) SetAway(away bool) { a.handle.SetAway(away) }

// FocusJob is a click on a bubble line (async): that job's pane in a terminal. A failure becomes
// the tray status line, as for OpenTerminal.
func (a *Agent) FocusJob(job, worker string) {
	if a.ctx.Err() != nil {
		return // quitting
	}
	a.wg.Go(func() {
		if err := a.handle.FocusJob(a.ctx, agent.JobID(job), worker); err != nil {
			log.Printf("agent: focus job: %v", err)
			a.setStatus(TerminalFailureStatus(err.Error()))
		}
	})
}

// ChatSend is a message typed in the text box (#221).
// Attachments are the files waiting for the next request (#240).
func (a *Agent) Attachments() *attach.Store { return a.attach }

func (a *Agent) ChatSend(text string) (app.ChatSent, error) {
	s, err := a.handle.ChatMessage(text)
	if err == nil {
		a.feed.sent(s.Job)
	}
	return app.ChatSent{Job: string(s.Job), Answered: s.Answered, Session: s.Session}, err
}

// ChatDelegate is a message typed in the text box that is always a new job (a reply to a
// session, #302).
func (a *Agent) ChatDelegate(text string) (string, error) {
	id, err := a.handle.ChatDelegate(text)
	if err == nil {
		a.feed.sent(id)
	}
	return string(id), err
}

// ChatStop is Stop in the text box.
func (a *Agent) ChatStop() { a.handle.ChatStop() }

// SetTextMode: replyMode is text.
func (a *Agent) SetTextMode(on bool) { a.handle.SetTextMode(on) }

// Roster is the roster beside Pixie (agent.RosterView).
func (a *Agent) Roster() any { return a.handle.Roster() }

// FocusAgent is a click on one of Pixie's agents in the roster (async). A failure becomes the
// tray status line, as for FocusJob.
func (a *Agent) FocusAgent(name string) {
	if a.ctx.Err() != nil {
		return // quitting
	}
	a.wg.Go(func() {
		if err := a.handle.FocusAgent(a.ctx, name); err != nil {
			log.Printf("agent: focus agent: %v", err)
			a.setStatus(TerminalFailureStatus(err.Error()))
		}
	})
}

// FocusSession is a click on one of your own sessions in the roster (async, #148). A failure
// becomes the tray status line, as for FocusAgent.
func (a *Agent) FocusSession(name string) {
	if a.ctx.Err() != nil {
		return // quitting
	}
	a.wg.Go(func() {
		if err := a.handle.FocusSession(a.ctx, name); err != nil {
			log.Printf("agent: focus session: %v", err)
			a.setStatus(TerminalFailureStatus(err.Error()))
		}
	})
}

// interruptLimit: a stop that takes longer says so.
const interruptLimit = 5 * time.Second

// Interrupt is the stop button by a working agent (#309), async like FocusAgent: the core
// decides what it calls, and a failure comes back as the StopFailed hook. An error now: no
// agent was named.
func (a *Agent) Interrupt(name, surface string) error {
	if name == "" {
		return agent.ErrStopNoName
	}
	if a.ctx.Err() != nil {
		return nil // quitting
	}
	a.wg.Go(func() {
		ctx, cancel := context.WithTimeout(a.ctx, interruptLimit)
		defer cancel()
		if _, err := a.handle.Interrupt(ctx, name); err != nil {
			log.Printf("agent: stop %s: %v", name, err)
			if a.ctx.Err() == nil && a.hooks.StopFailed != nil {
				a.hooks.StopFailed(name, surface, err.Error())
			}
		}
	})
	return nil
}

// SessionPlaces: where your sessions named run, for the paths in their replies (#277).
func (a *Agent) SessionPlaces(names []string) []app.SessionPlace {
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	var out []app.SessionPlace
	for _, p := range a.handle.SessionPlaces(ctx, names) {
		out = append(out, app.SessionPlace{Folder: p.Folder, App: p.App})
	}
	return out
}

// ReportFocusOutcome: the shell finished executing a FocusPlan (#116). A failure becomes the
// tray status line through the same path as an OpenTerminal failure, so wording never diverges.
func (a *Agent) ReportFocusOutcome(success bool, detail string) {
	if !success {
		a.setStatus(TerminalFailureStatus(detail))
	}
}

// AttachVoice connects the voice loop once it exists: injections go to it, and its signals
// come here. UserSaid heard while Pixie speaks, or just after, is dropped as possible echo (R-j).
func (a *Agent) AttachVoice(v app.AgentVoice) {
	a.link.attach(v)
	signals := v.Subscribe()
	a.wg.Go(func() {
		for {
			select {
			case <-a.ctx.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					return
				}
				switch sig.Kind {
				case voice.SigOpened:
					a.link.setOpen(true)
				case voice.SigClosed:
					a.link.setOpen(false)
				}
				a.echoMu.Lock()
				admit := a.echo.Admits(sig, time.Now())
				a.echoMu.Unlock()
				if admit {
					a.toAgent(sig)
				} else {
					log.Print("agent: ignored user words heard while Pixie was speaking")
				}
			}
		}
	})
}

// OnMouth feeds the echo gate (each mouth level the voice runtime reports).
func (a *Agent) OnMouth(level int) {
	a.echoMu.Lock()
	a.echo.OnMouth(level, time.Now())
	a.echoMu.Unlock()
}

// Shutdown is the app's quit: deny pending permissions and stop the panes in parallel within
// agent.ShutdownLimit (unless keepPanesOnQuit), re-check the worktrees' git links, then stop
// the runtime and the hook server, delete mcp/*.json (this run's tokens) and stop the session
// host (herdr's event pump). Idempotent.
func (a *Agent) Shutdown() {
	a.shutOnce.Do(func() {
		a.handle.Shutdown()
		a.cancel()
		agentfs.ClearMcpDir(a.paths.McpDir)
		shutdownHost(a.session)
		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(serverWait):
			log.Printf("agent: shutdown left work running after %v", serverWait)
		}
	})
}
