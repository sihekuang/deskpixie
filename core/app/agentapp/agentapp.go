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
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/settings"
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
	return []voice.Tool{agent.Delegate{H: h}, agent.Jobs{H: h}, agent.AnswerJob{H: h}, agent.Recall{H: h}}
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
		return "Agent: " + he.HostName() + " not found"
	}
	return "Agent: invalid settings (see log)"
}

// TerminalFailureStatus is the tray status line for an OpenTerminal failure.
func TerminalFailureStatus(err string) string {
	if what, _, ok := strings.Cut(err, ":"); ok && strings.HasPrefix(what, "could not open ") {
		return "Agent: " + what
	}
	return "Agent: " + err
}

// Config is what Build needs. The zero test seams pick the real adapters.
type Config struct {
	Settings      settings.AgentSettings
	Notifications settings.NotificationSettings
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
}

// Agent is the running delegation agent (app.Agent).
type Agent struct {
	handle  *agent.AgentHandle
	link    *VoiceLink
	session agent.SessionHost
	paths   Paths
	hooks   app.AgentHooks
	addr    string

	// toAgent receives the voice signals the echo gate admits (handle.Voice; tests replace it).
	toAgent func(voice.VoiceSignal)

	echoMu sync.Mutex
	echo   EchoGate

	mu           sync.Mutex
	status, hint string

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
	a := Build(Config{Settings: cfg.Agent, Notifications: cfg.Notifications, AppSupport: appSupport, Home: home, Hooks: hooks})
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
	a := &Agent{paths: NewPaths(c.AppSupport), hooks: c.Hooks}
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
	ws := SetupWorkers(s.Workers, s.ProjectsRoot, c.Home)
	workersOn := ws.Policy.Enabled
	if err := Install(paths, s.Port, ws.Root); err != nil {
		log.Printf("agent: invalid settings: %v", err)
		setProblem("Agent: invalid settings (see log)")
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
		setProblem("Agent: invalid settings (see log)")
		session = Unavailable{Err: agent.HostError{Kind: agent.HostOther, Detail: "unknown sessionHost " + s.SessionHost}}
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
	tokens := httpserver.NewTokens()
	coordinatorToken := kept.CoordinatorToken
	if coordinatorToken != "" {
		// The kept coordinator's hooks and MCP calls still carry it.
		tokens.Restore(agent.Coordinator, coordinatorToken)
	} else {
		coordinatorToken = tokens.Issue(agent.Coordinator)
	}
	if workersOn {
		if err := WriteCoordinatorMcp(paths, s.Port, coordinatorToken); err != nil {
			log.Printf("agent: cannot write the coordinator's MCP config: %v", err)
			setProblem("Agent: invalid settings (see log)")
		}
	}
	model := ""
	if s.Model != nil {
		model = *s.Model
	}
	recipe := agent.CoordinatorRecipe{
		Cwd:  paths.Workspace,
		Args: CoordinatorArgs(paths, model, workersOn),
		Env:  []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: coordinatorToken}},
		// A coordinator found running is the kept one: adopted as it is.
		AdoptEarlier: kept.CoordinatorToken != "",
		ResumeLast:   true,
	}
	deps := agent.Deps{
		Host:        agent.NewAgentHost(session, c.Clock, recipe),
		Voice:       a.link,
		Notifier:    NewNotifier(c.Runner, c.Hooks.Notify),
		Mascot:      mascotCue{c.Hooks},
		Status:      statusSink{a},
		Launcher:    termFactory(c.Runner, c.Home),
		TerminalDir: TerminalDir(s.ProjectsRoot, c.Home),
		Store:       NewJsonlStore(paths.Root),
		Clock:       c.Clock,
		IDs:         c.IDs,
		Kept:        KeptFile(paths.Kept()),
		Updates:     updatesSink{c.Hooks},
	}
	// Exactly 127.0.0.1 (the hook server refuses any non-loopback listener). Bound before the
	// runtime is built, so a busy port is known before a warm start could launch anything.
	ln, err := c.Listen(s.Port)
	if err != nil {
		log.Printf("agent: cannot listen on 127.0.0.1:%d: %v", s.Port, err)
		setProblem("Agent: hook port busy")
		ln = nil
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
			OpenConversation:    s.OpenConversationForUpdates,
		},
		Warm: s.Warm,
		// Stepped before the warm start: a disabled app never starts the coordinator.
		SetupProblem: problem,
		Notice:       ws.Notice,
		Kept:         kept,
	}
	var workerDeps *agent.WorkerDeps
	if workersOn {
		workerDeps = WorkerAdapters(paths, ws.Root, c.Home, s.Port, model, tokens)
	}
	a.handle = agent.NewRuntimeWithWorkers(deps, cfg, workerDeps)
	a.toAgent = a.handle.Voice
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.wg.Go(func() { a.handle.Run(a.ctx) })
	if ln != nil {
		a.addr = ln.Addr().String()
		a.wg.Go(func() {
			if err := httpserver.Serve(a.ctx, ln, tokens, a.handle); err != nil && !errors.Is(err, context.Canceled) {
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

// Facts is Bit's memory for each voice session: the coordinator's recent jobs (app.AgentFacts).
func (a *Agent) Facts() string { return a.handle.Facts() }

// Tray is Tray ▸ Agent: the open jobs, the runtime's status line and the standing hint.
func (a *Agent) Tray() app.AgentTray {
	var rows []app.JobRow
	for _, j := range a.handle.OpenJobs() {
		rows = append(rows, app.JobRow{ID: j.JobID, Status: j.Status.String(), Task: j.Task, AgeSecs: j.AgeSecs})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return app.AgentTray{Enabled: true, Jobs: rows, Status: a.status, Hint: a.hint}
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

// Menu runs Tray ▸ Agent items; false for ids that are not the agent's.
func (a *Agent) Menu(id string) bool {
	switch id {
	case "agent:open":
		a.OpenTerminal()
	case "agent:restart":
		a.handle.Restart()
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

// OpenTerminal is a single click on Bit / Tray ▸ Open agent terminal (async). A failure
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

// UpdatesSeen: the updates bubble showed these lines long enough to count as heard.
func (a *Agent) UpdatesSeen(ids []uint64) { a.handle.UpdatesSeen(ids) }

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

// AttachVoice connects the voice loop once it exists: injections go to it, and its signals
// come here. UserSaid heard while Bit speaks, or just after, is dropped as possible echo (R-j).
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
					log.Print("agent: ignored user words heard while Bit was speaking")
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
