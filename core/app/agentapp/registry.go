package agentapp

// Name → factory registries (spec §2 open/closed): a new multiplexer or terminal is one
// adapter package plus one entry here. Port of src-tauri/src/agent/registry.rs.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/terminal"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux"
)

// Hint posts a one-line hint for the tray (e.g. IntegrationHint).
type Hint func(line string)

// HostDeps are what a session host factory gets.
type HostDeps struct {
	Settings settings.AgentSettings
	Home     string
	Runner   agent.CommandRunner
	Hint     Hint
	// Environ is the parent environment ("KEY=value"); nil = os.Environ().
	Environ []string
}

type (
	HostFactory     func(HostDeps) (agent.SessionHost, error)
	TerminalFactory func(runner agent.CommandRunner, home string) agent.TerminalLauncher
)

// SessionHosts is the session host registry.
func SessionHosts() map[string]HostFactory {
	return map[string]HostFactory{"herdr": newHerdrHost, tmux.Name: newTmuxHost}
}

// parentEnv is d's parent environment as EnvVars, and its PATH.
func parentEnv(d HostDeps) ([]agent.EnvVar, string) {
	environ := d.Environ
	if environ == nil {
		environ = os.Environ()
	}
	parent := make([]agent.EnvVar, 0, len(environ))
	pathVar := ""
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		parent = append(parent, agent.EnvVar{Key: k, Value: v})
		if k == "PATH" {
			pathVar = v
		}
	}
	return parent, pathVar
}

func newHerdrHost(d HostDeps) (agent.SessionHost, error) {
	parent, pathVar := parentEnv(d)
	configured := ""
	if d.Settings.HerdrPath != nil {
		configured = *d.Settings.HerdrPath
	}
	bin, ok := herdr.FindHerdr(configured, pathVar, d.Home, isFile)
	if !ok {
		return nil, agent.HostError{Kind: agent.HostNotFound}
	}
	env, err := herdr.ServerEnv(parent, d.Settings.Session, d.Home)
	if err != nil {
		return nil, err
	}
	sup, err := herdr.NewSupervisor(bin, env, d.Runner)
	if err != nil {
		return nil, err
	}
	host := herdr.NewHost(d.Settings.Session, d.Home, sup)
	return NewIntegrationCheck(host, host.IntegrationMissing, d.Hint), nil
}

// newTmuxHost is the tmux session host (no integration hint: the agents' status hooks are
// DeskPixie's own, written into their settings files).
func newTmuxHost(d HostDeps) (agent.SessionHost, error) {
	parent, pathVar := parentEnv(d)
	configured := ""
	if d.Settings.TmuxPath != nil {
		configured = *d.Settings.TmuxPath
	}
	bin, ok := tmux.FindTmux(configured, pathVar, isFile)
	if !ok {
		return nil, agent.HostError{Kind: agent.HostNotFound, Host: tmux.Name}
	}
	env, err := tmux.ServerEnv(parent, d.Settings.Session, d.Home)
	if err != nil {
		return nil, err
	}
	return tmux.NewHost(bin, d.Settings.Session, d.Home, tmux.ExecRunner{Bin: bin, Env: env, Exec: d.Runner}), nil
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// Terminals is the terminal registry (an unknown name falls back to warp). Each reuses a
// terminal already attached to the session instead of opening another (#98).
func Terminals() map[string]TerminalFactory {
	return map[string]TerminalFactory{
		"warp": func(r agent.CommandRunner, home string) agent.TerminalLauncher {
			return terminal.Reuse(terminal.NewWarpLauncher(r, filepath.Join(home, ".warp/tab_configs")), r)
		},
		"iterm": func(r agent.CommandRunner, _ string) agent.TerminalLauncher {
			return terminal.Reuse(terminal.NewItermLauncher(r), r)
		},
	}
}

// IntegrationCheck is Herdr's session host plus R-h: after the first successful
// EnsureSession it reads the host's own integration check (which the supervisor fills in
// while ensuring the server) and posts IntegrationHint once if the Claude integration isn't
// current. Read-only: it runs nothing and never installs anything. Delegation is unaffected;
// only Herdr's status detection is degraded.
type IntegrationCheck struct {
	inner   agent.SessionHost
	missing func() bool
	hint    Hint
	checked atomic.Bool
}

var _ agent.SessionHost = (*IntegrationCheck)(nil)
var _ agent.PassiveLister = (*IntegrationCheck)(nil)

func NewIntegrationCheck(inner agent.SessionHost, missing func() bool, hint Hint) *IntegrationCheck {
	return &IntegrationCheck{inner: inner, missing: missing, hint: hint}
}

func (c *IntegrationCheck) Name() string { return c.inner.Name() }

func (c *IntegrationCheck) EnsureSession(ctx context.Context) error {
	if err := c.inner.EnsureSession(ctx); err != nil {
		return err
	}
	if !c.checked.Swap(true) && c.missing() {
		c.hint(IntegrationHint)
	}
	return nil
}

func (c *IntegrationCheck) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return c.inner.StartAgent(ctx, spec)
}

func (c *IntegrationCheck) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return c.inner.ReplaceAgent(ctx, spec)
}

func (c *IntegrationCheck) Prompt(ctx context.Context, name, text string) error {
	return c.inner.Prompt(ctx, name, text)
}

func (c *IntegrationCheck) SendKeys(ctx context.Context, name string, keys []string) error {
	return c.inner.SendKeys(ctx, name, keys)
}

func (c *IntegrationCheck) Read(ctx context.Context, name string, lines uint16) (string, error) {
	return c.inner.Read(ctx, name, lines)
}

func (c *IntegrationCheck) Close(ctx context.Context, name string) error {
	return c.inner.Close(ctx, name)
}

func (c *IntegrationCheck) CloseExact(ctx context.Context, name, pane string) error {
	return c.inner.CloseExact(ctx, name, pane)
}

func (c *IntegrationCheck) List(ctx context.Context) ([]agent.AgentInfo, error) {
	return c.inner.List(ctx)
}

// ListRunning forwards the passive list: the roster must read Herdr without starting it, and
// a wrapper that dropped this would turn that into an active List (see agent.PassiveLister).
func (c *IntegrationCheck) ListRunning(ctx context.Context) ([]agent.AgentInfo, error) {
	return agent.ListRunning(ctx, c.inner)
}

func (c *IntegrationCheck) Subscribe() <-chan agent.PaneEvent { return c.inner.Subscribe() }
func (c *IntegrationCheck) AttachCommand() []string           { return c.inner.AttachCommand() }

// Focus passes through to the wrapped host, if it can focus.
func (c *IntegrationCheck) Focus(ctx context.Context, name string) error {
	if f, ok := c.inner.(agent.Focuser); ok {
		return f.Focus(ctx, name)
	}
	return nil
}

// Relabel passes through to the wrapped host, if it can relabel tabs.
func (c *IntegrationCheck) Relabel(ctx context.Context, name string, tab agent.TabLabel) error {
	if l, ok := c.inner.(agent.Labeler); ok {
		return l.Relabel(ctx, name, tab)
	}
	return nil
}

// Shutdown stops the wrapped host (herdr.Host's event pump), if it has one.
func (c *IntegrationCheck) Shutdown() { shutdownHost(c.inner) }

func shutdownHost(h agent.SessionHost) {
	if s, ok := h.(interface{ Shutdown() }); ok {
		s.Shutdown()
	}
}

// Unavailable stands in when the configured multiplexer can't be built (e.g. herdr not
// installed): every call fails with that error, so delegate and a click report it.
type Unavailable struct{ Err error }

var _ agent.SessionHost = Unavailable{}

func (Unavailable) Name() string                          { return "unavailable" }
func (u Unavailable) EnsureSession(context.Context) error { return u.Err }
func (u Unavailable) StartAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, u.Err
}
func (u Unavailable) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.DefaultReplaceAgent(ctx, u, spec)
}
func (u Unavailable) Prompt(context.Context, string, string) error     { return u.Err }
func (u Unavailable) SendKeys(context.Context, string, []string) error { return u.Err }
func (u Unavailable) Read(context.Context, string, uint16) (string, error) {
	return "", u.Err
}
func (Unavailable) Close(_ context.Context, name string) error { return agent.ErrNoAgent(name) }
func (u Unavailable) CloseExact(ctx context.Context, name, pane string) error {
	return agent.DefaultCloseExact(ctx, u, name, pane)
}
func (u Unavailable) List(context.Context) ([]agent.AgentInfo, error) { return nil, u.Err }

// Subscribe never delivers (Rust: the sender is dropped at once, so the channel is closed).
func (Unavailable) Subscribe() <-chan agent.PaneEvent {
	ch := make(chan agent.PaneEvent)
	close(ch)
	return ch
}
func (Unavailable) AttachCommand() []string { return nil }
