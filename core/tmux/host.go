package tmux

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux/compat"
)

// Name is the registry name.
const Name = "tmux"

// Timing. StartTimeout matches Herdr's, so the runtime's budgets hold for both hosts (the
// runtime's budget test asserts the relations).
const (
	// StartTimeout is how long StartAgent waits for the agent's input prompt.
	StartTimeout = 45 * time.Second
	// StartPoll is between two looks at a starting agent's screen.
	StartPoll = 250 * time.Millisecond
	// ScreenPoll is between two reads of the screens whose status comes from them.
	ScreenPoll = 500 * time.Millisecond
	// ExitPoll is between two list-panes looking for exited agents.
	ExitPoll = time.Second
	// PasteSettle is between a pasted prompt and its Enter.
	PasteSettle = 150 * time.Millisecond
	// sendChunk is the most text one send-keys carries (tmux's command messages are bounded).
	sendChunk = 2048
)

// placeholder is the window a new session opens with (a session can't be empty); it is closed
// once an agent's window is up beside it, and never listed.
const placeholder = "_deskpixie"

// listFormat is list-panes' line: the path last, so a tab in it can't shift the other fields.
const listFormat = "#{window_name}\t#{pane_id}\t#{pane_dead}\t#{@deskpixie_kind}\t#{pane_current_path}"

// unsetEnv are dropped from the server's environment besides proc.Scrubbed's: a tmux client
// started inside another tmux would think it is nested.
var unsetEnv = []string{"TMUX", "TMUX_PANE"}

// CheckSession: the socket and session name (agent.session) follows the settings rule.
func CheckSession(session string) error {
	if err := settings.CheckSession(session); err != nil {
		return agent.HostError{Kind: agent.HostOther, Detail: err.Error()}
	}
	return nil
}

// ServerEnv is the tmux server's (and every tmux call's) exact environment: parent scrubbed as
// for Herdr (proc.ServerEnv), without TMUX/TMUX_PANE. Rejects a bad session name.
func ServerEnv(parent []agent.EnvVar, session, home string) ([]agent.EnvVar, error) {
	if err := CheckSession(session); err != nil {
		return nil, err
	}
	return proc.ServerEnv(parent, home, unsetEnv), nil
}

// installDirs are where tmux is looked for beyond PATH (agent.tmuxPath's help).
var installDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// FindTmux: configured (agent.tmuxPath, "" for none), else tmux on pathVar, else in
// /opt/homebrew/bin or /usr/local/bin.
func FindTmux(configured, pathVar string, exists func(string) bool) (string, bool) {
	return proc.FindProgram("tmux", configured, pathVar, installDirs, exists)
}

// Host implements agent.SessionHost (plus Focuser, Labeler, StatusReporter and Shutdown) over a
// private tmux server: `tmux -L <session>`, one session named <session>, one window per agent
// named by the agent's name (agents are found by name, so the name is never changed; labels go
// in the pane title and the window's @deskpixie_label). remain-on-exit keeps an exited agent's
// pane until it is read and closed. Pane ids (%N) are never reused within a server.
type Host struct {
	bin, session, home string
	run                Runner
	det                *Detector

	ensureMu sync.Mutex
	checked  bool // tmux -V passed

	subsMu sync.Mutex
	subs   []*subscriber

	pollOnce sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	shutdown sync.Once

	startTimeout, startPoll, screenPoll, exitPoll time.Duration
	pasteSettle                                   time.Duration
}

var (
	_ agent.SessionHost    = (*Host)(nil)
	_ agent.Focuser        = (*Host)(nil)
	_ agent.Labeler        = (*Host)(nil)
	_ agent.StatusReporter = (*Host)(nil)
)

// NewHost: bin is the tmux program (for AttachCommand), run runs it with the server's
// environment. Nothing runs until the first call.
func NewHost(bin, session, home string, run Runner) *Host {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{bin: bin, session: session, home: home, run: run, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		startTimeout: StartTimeout, startPoll: StartPoll, screenPoll: ScreenPoll, exitPoll: ExitPoll,
		pasteSettle: PasteSettle}
	h.det = NewDetector(h.broadcast)
	return h
}

// SetTiming shortens the waits (tests): start timeout and the start, screen and exit polls.
func (h *Host) SetTiming(startTimeout, startPoll, screenPoll, exitPoll time.Duration) {
	h.startTimeout, h.startPoll, h.screenPoll, h.exitPoll = startTimeout, startPoll, screenPoll, exitPoll
}

func (h *Host) Name() string { return Name }

// Shutdown stops the polls and closes every Subscribe channel. Idempotent.
func (h *Host) Shutdown() {
	h.shutdown.Do(func() {
		h.cancel()
		close(h.done)
	})
}

// tmux runs one command on our server.
func (h *Host) tmux(ctx context.Context, args ...string) (Output, error) {
	return h.run.Run(ctx, append([]string{"-L", h.session}, args...)...)
}

// noSession: tmux answered that our server or session isn't there.
func noSession(out Output) bool {
	e := out.Stderr
	return strings.Contains(e, "no server running") || strings.Contains(e, "can't find session") ||
		strings.Contains(e, "error connecting to") || strings.Contains(e, "No such file or directory")
}

// failed is a refused command as a HostError.
func failed(what string, out Output) error {
	return agent.HostError{Kind: agent.HostOther, Detail: "tmux " + what + ": " + strings.TrimSpace(out.Stderr)}
}

// checkVersion runs `tmux -V` once: a missing tmux is NotFound, one older than 3.2 a
// VersionMismatch naming the supported versions.
func (h *Host) checkVersion(ctx context.Context) error {
	if h.checked {
		return nil
	}
	out, err := h.run.Run(ctx, "-V")
	if err != nil {
		return err
	}
	if out.Code != 0 {
		return failed("-V", out)
	}
	have := strings.TrimSpace(out.Stdout)
	if !compat.Supported(compat.Parse(have)) {
		return agent.HostError{Kind: agent.HostVersionMismatch, Host: Name,
			Detail: fmt.Sprintf("you have %s; DeskPixie supports %s", have, compat.SupportedVersions)}
	}
	h.checked = true
	return nil
}

// EnsureSession: tmux is a supported version and our session runs (started with its
// placeholder window and the options the host relies on). Idempotent.
func (h *Host) EnsureSession(ctx context.Context) error {
	h.ensureMu.Lock()
	defer h.ensureMu.Unlock()
	if err := h.checkVersion(ctx); err != nil {
		return err
	}
	h.startPolls()
	out, err := h.tmux(ctx, "has-session", "-t", "="+h.session)
	if err != nil {
		return err
	}
	if out.Code == 0 {
		return nil
	}
	// One call, so the options are set before anything else can use the new server.
	out, err = h.tmux(ctx, "new-session", "-d", "-s", h.session, "-n", placeholder, "-c", h.home,
		";", "set-option", "-gw", "remain-on-exit", "on",
		";", "set-option", "-gw", "automatic-rename", "off",
		";", "set-option", "-gw", "allow-rename", "off")
	if err != nil {
		return err
	}
	if out.Code != 0 && !strings.Contains(out.Stderr, "duplicate session") {
		return failed("new-session", out)
	}
	// tmux 3.4+: the agents' own terminal titles don't replace the label in the pane title.
	// Older versions don't know the option; that only costs the title.
	_, _ = h.tmux(ctx, "set-option", "-gw", "allow-set-title", "off")
	return nil
}

// parseRows reads list-panes lines (listFormat).
func parseRows(text string) []Row {
	var rows []Row
	for _, line := range strings.Split(text, "\n") {
		f := strings.SplitN(strings.TrimSuffix(line, "\r"), "\t", 5)
		if len(f) != 5 || !strings.HasPrefix(f[1], "%") {
			continue
		}
		rows = append(rows, Row{Window: f[0], Pane: f[1], Dead: f[2] == "1", Kind: f[3], Cwd: f[4]})
	}
	return rows
}

// rows lists every pane of the session (dead ones too); gone is true when the server or session
// isn't running (no rows, no error).
func (h *Host) rows(ctx context.Context) (rows []Row, gone bool, err error) {
	out, err := h.tmux(ctx, "list-panes", "-s", "-t", "="+h.session, "-F", listFormat)
	if err != nil {
		return nil, false, err
	}
	if out.Code != 0 {
		if noSession(out) {
			return nil, true, nil
		}
		return nil, false, failed("list-panes", out)
	}
	return parseRows(out.Stdout), false, nil
}

// find is agentName's newest pane: a live one, or with dead also an exited one. NoAgent when
// there is none (or no server).
func (h *Host) find(ctx context.Context, agentName string, dead bool) (Row, error) {
	rows, _, err := h.rows(ctx)
	if err != nil {
		return Row{}, err
	}
	var found *Row
	for i := range rows {
		r := &rows[i]
		if r.Window != agentName || (r.Dead && !dead) {
			continue
		}
		if found == nil || paneNumber(r.Pane) > paneNumber(found.Pane) || (found.Dead && !r.Dead) {
			found = r
		}
	}
	if found == nil {
		return Row{}, agent.ErrNoAgent(agentName)
	}
	return *found, nil
}

func paneNumber(id string) int {
	n := 0
	for _, c := range strings.TrimPrefix(id, "%") {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// onPane runs a command on pane; tmux not finding the pane is NoAgent.
func (h *Host) onPane(ctx context.Context, agentName, what string, args ...string) (Output, error) {
	out, err := h.tmux(ctx, args...)
	if err != nil {
		return out, err
	}
	if out.Code != 0 {
		if noSession(out) || strings.Contains(out.Stderr, "can't find") {
			return out, agent.ErrNoAgent(agentName)
		}
		return out, failed(what, out)
	}
	return out, nil
}

func kindOf(name string) agent.AgentKind {
	if k, ok := agent.ParseAgentKind(name); ok {
		return k
	}
	return agent.Claude
}

func (h *Host) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.start(ctx, spec, false)
}

// ReplaceAgent starts spec and closes the running agent of spec's name (without reporting it
// exited: its name lives on). Pane ids aren't reused, so a late close can't hit the new agent.
func (h *Host) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.start(ctx, spec, true)
}

func (h *Host) start(ctx context.Context, spec agent.AgentSpec, replace bool) (agent.AgentInfo, error) {
	if err := h.EnsureSession(ctx); err != nil {
		return agent.AgentInfo{}, err
	}
	rows, _, err := h.rows(ctx)
	if err != nil {
		return agent.AgentInfo{}, err
	}
	// Earlier panes of this name are closed only once the new window exists: closing the
	// session's last window would end the session (and the server).
	var stale []string
	for _, r := range rows {
		if r.Window != spec.Name {
			continue
		}
		switch {
		case r.Dead:
			// An earlier agent of this name exited: reported (once) before the new one starts.
			h.det.Gone(r.Pane)
		case replace:
			// Its name lives on: not reported exited.
			h.det.Forget(r.Pane)
		default:
			return agent.AgentInfo{}, agent.HostError{Kind: agent.HostOther, Detail: "an agent named " + spec.Name + " is already running"}
		}
		stale = append(stale, r.Pane)
	}
	// The agent runs as argv (no shell, so nothing to quote), in its folder, with its env.
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=" + h.session + ":", "-n", spec.Name}
	if spec.Cwd != "" {
		args = append(args, "-c", formatArg(spec.Cwd))
	}
	for _, e := range spec.Env {
		args = append(args, "-e", literalArg(e.Key+"="+e.Value))
	}
	args = append(args, "--", spec.Kind.String())
	for _, a := range spec.Args {
		args = append(args, literalArg(a))
	}
	out, err := h.tmux(ctx, args...)
	if err != nil {
		return agent.AgentInfo{}, err
	}
	if out.Code != 0 {
		return agent.AgentInfo{}, failed("new-window", out)
	}
	pane := strings.TrimSpace(out.Stdout)
	if !strings.HasPrefix(pane, "%") {
		return agent.AgentInfo{}, agent.HostError{Kind: agent.HostOther, Detail: "tmux new-window: no pane id"}
	}
	h.det.Track(pane, spec.Name, spec.Kind)
	for _, p := range stale {
		if out, err := h.tmux(context.WithoutCancel(ctx), "kill-pane", "-t", p); err == nil && out.Code != 0 && !strings.Contains(out.Stderr, "can't find") {
			log.Printf("agent: tmux: could not close %s's earlier pane: %s", spec.Name, errTag(failed("kill-pane", out)))
		}
	}
	// Closes the new window unless the start completes, including when ctx ends (the
	// runtime's start limit), so an abandoned start never leaks a window.
	abandon := func() {
		h.det.Forget(pane)
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultTimeout)
		defer cancel()
		_, _ = h.tmux(c, "kill-pane", "-t", pane)
	}
	label := cmp.Or(spec.Tab.Label, spec.Name)
	if out, err := h.tmux(ctx, append([]string{"set-option", "-w", "-t", pane, "@deskpixie_kind", spec.Kind.String(), ";"},
		labelArgs(pane, label, spec.Tab.Title)...)...); err != nil || out.Code != 0 {
		if err == nil {
			err = failed("set-option", out)
		}
		log.Printf("agent: tmux: could not label %s's window: %s", spec.Name, errTag(err))
	}
	status, err := h.waitStarted(ctx, spec, pane)
	if err != nil {
		abandon()
		return agent.AgentInfo{}, err
	}
	status = h.det.Started(pane, status)
	h.closePlaceholder(ctx)
	return agent.AgentInfo{Name: spec.Name, Pane: pane, Cwd: spec.Cwd, Status: status}, nil
}

// errTag is an error for the log: its kind, never agent text.
func errTag(err error) string {
	switch {
	case agent.IsHostError(err, agent.HostTimeout):
		return "timeout"
	case agent.IsHostError(err, agent.HostNoAgent):
		return "no agent"
	}
	return "error"
}

// waitStarted watches a new agent's pane until it is up (StartedScreen), its process exits
// (ExitedAtStart, e.g. `claude --continue` with nothing to continue) or the start times out.
func (h *Host) waitStarted(ctx context.Context, spec agent.AgentSpec, pane string) (agent.PaneStatus, error) {
	deadline := time.Now().Add(h.startTimeout)
	for {
		out, err := h.tmux(ctx, "display-message", "-p", "-t", pane, "#{pane_dead}")
		if err != nil {
			if ctx.Err() != nil {
				return agent.PaneUnknown, ctx.Err()
			}
			return agent.PaneUnknown, err
		}
		if out.Code != 0 || strings.TrimSpace(out.Stdout) == "1" {
			// Gone, or its process ended.
			return agent.PaneUnknown, agent.HostError{Kind: agent.HostExitedAtStart, Detail: spec.Kind.String()}
		}
		if screen, err := h.capture(ctx, pane, 0); err == nil {
			if status, ok := StartedScreen(spec.Kind, screen); ok {
				return status, nil
			}
		}
		if !time.Now().Before(deadline) {
			return agent.PaneUnknown, agent.HostError{Kind: agent.HostTimeout, Detail: spec.Kind.String() + " did not start"}
		}
		select {
		case <-ctx.Done():
			return agent.PaneUnknown, ctx.Err()
		case <-time.After(h.startPoll):
		}
	}
}

// closePlaceholder closes the session's first, empty window once an agent's window is up
// beside it.
func (h *Host) closePlaceholder(ctx context.Context) {
	rows, _, err := h.rows(ctx)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.Window == placeholder {
			_, _ = h.tmux(context.WithoutCancel(ctx), "kill-pane", "-t", r.Pane)
		}
	}
}

// capture is pane's screen as plain text (no ANSI): the visible screen, plus history lines
// above it.
func (h *Host) capture(ctx context.Context, pane string, history int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", pane}
	if history > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", history))
	}
	out, err := h.tmux(ctx, args...)
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", failed("capture-pane", out)
	}
	return out.Stdout, nil
}

// Prompt types text into the agent, then presses Enter. One line is typed literally (in
// chunks); text of several lines is pasted (bracketed paste: tmux's paste-buffer -p), since a
// typed line feed would reach the agent as Ctrl-J, which a TUI may take as Enter.
func (h *Host) Prompt(ctx context.Context, agentName, text string) error {
	r, err := h.find(ctx, agentName, false)
	if err != nil {
		return err
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.ContainsAny(text, "\r\n") {
		err = h.paste(ctx, agentName, r.Pane, text)
	} else {
		for _, chunk := range chunks(text, sendChunk) {
			if _, err = h.onPane(ctx, agentName, "send-keys", "send-keys", "-t", r.Pane, "-l", "--", literalArg(chunk)); err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	// The agent reads a paste as one; Enter right behind it could join it.
	if strings.ContainsAny(text, "\r\n") {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(h.pasteSettle):
		}
	}
	_, err = h.onPane(ctx, agentName, "send-keys", "send-keys", "-t", r.Pane, "Enter")
	return err
}

// paste puts text in a buffer of its own (in chunks: tmux's messages are bounded) and pastes it
// into pane bracketed (the agent enabled bracketed paste, as Claude Code and Codex do: it then
// takes the line breaks as text), deleting the buffer.
func (h *Host) paste(ctx context.Context, agentName, pane, text string) error {
	buf := "deskpixie-" + strings.TrimPrefix(pane, "%")
	for i, chunk := range chunks(text, sendChunk) {
		args := []string{"set-buffer", "-b", buf}
		if i > 0 {
			args = append(args, "-a")
		}
		if _, err := h.onPane(ctx, agentName, "set-buffer", append(args, "--", literalArg(chunk))...); err != nil {
			return err
		}
	}
	_, err := h.onPane(ctx, agentName, "paste-buffer", "paste-buffer", "-p", "-d", "-b", buf, "-t", pane)
	return err
}

// chunks cuts text into pieces of at most n bytes, on character boundaries.
func chunks(text string, n int) []string {
	var out []string
	for len(text) > n {
		cut := n
		for cut > 0 && !isRuneStart(text[cut]) {
			cut--
		}
		out = append(out, text[:cut])
		text = text[cut:]
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// SendKeys presses keys (tmux key names: Escape, Enter, Down, …).
func (h *Host) SendKeys(ctx context.Context, agentName string, keys []string) error {
	r, err := h.find(ctx, agentName, false)
	if err != nil {
		return err
	}
	_, err = h.onPane(ctx, agentName, "send-keys", append([]string{"send-keys", "-t", r.Pane}, keys...)...)
	return err
}

// Read returns the last lines of the agent's screen (an exited agent's last screen too),
// without ANSI codes and without the blank rows below the content.
func (h *Host) Read(ctx context.Context, agentName string, lines uint16) (string, error) {
	r, err := h.find(ctx, agentName, true)
	if err != nil {
		return "", err
	}
	out, err := h.onPane(ctx, agentName, "capture-pane", "capture-pane", "-p", "-J", "-t", r.Pane, "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", err
	}
	return lastLines(out.Stdout, int(lines)), nil
}

// lastLines is text's last n lines, trailing blank lines dropped.
func lastLines(text string, n int) string {
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for len(all) > 0 && strings.TrimSpace(all[len(all)-1]) == "" {
		all = all[:len(all)-1]
	}
	return strings.Join(all[max(0, len(all)-n):], "\n")
}

// Close closes every pane of the agent's name (its process ends); each is reported exited once.
func (h *Host) Close(ctx context.Context, agentName string) error {
	rows, _, err := h.rows(ctx)
	if err != nil {
		return err
	}
	closed := false
	for _, r := range rows {
		if r.Window != agentName {
			continue
		}
		if err := h.kill(ctx, agentName, r.Pane); err != nil {
			return err
		}
		closed = true
	}
	if !closed {
		return agent.ErrNoAgent(agentName)
	}
	return nil
}

func (h *Host) kill(ctx context.Context, agentName, pane string) error {
	out, err := h.tmux(ctx, "kill-pane", "-t", pane)
	if err != nil {
		return err
	}
	if out.Code != 0 && !strings.Contains(out.Stderr, "can't find") && !noSession(out) {
		return failed("kill-pane", out)
	}
	h.det.Gone(pane)
	return nil
}

// CloseExact closes pane only while it is still agentName's (a pane already gone is fine).
func (h *Host) CloseExact(ctx context.Context, agentName, pane string) error {
	rows, _, err := h.rows(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Window == agentName && r.Pane == pane {
			return h.kill(ctx, agentName, pane)
		}
	}
	return nil
}

// List is the running agents (an exited agent's pane, kept by remain-on-exit, is not listed).
// No server running is an empty list. Agents not known yet (left by an earlier run) are
// adopted: from then on their status and exit are reported; one whose status isn't known yet
// has its screen read now.
func (h *Host) List(ctx context.Context) ([]agent.AgentInfo, error) {
	if err := h.checkVersionLocked(ctx); err != nil {
		return nil, err
	}
	h.startPolls()
	rows, _, err := h.rows(ctx)
	if err != nil {
		return nil, err
	}
	var out []agent.AgentInfo
	for _, r := range rows {
		if r.Dead || strings.HasPrefix(r.Window, "_") {
			continue
		}
		kind := kindOf(r.Kind)
		h.det.Adopt(r.Pane, r.Window, kind)
		status := h.det.Status(r.Pane)
		if status == agent.PaneUnknown {
			if screen, err := h.capture(ctx, r.Pane, 0); err == nil {
				h.det.Screen(r.Pane, screen)
				status = h.det.Status(r.Pane)
			}
		}
		out = append(out, agent.AgentInfo{Name: r.Window, Pane: r.Pane, Cwd: r.Cwd, Status: status})
	}
	return out, nil
}

func (h *Host) checkVersionLocked(ctx context.Context) error {
	h.ensureMu.Lock()
	defer h.ensureMu.Unlock()
	return h.checkVersion(ctx)
}

// Focus brings the agent's window to the front, so an attached terminal shows it.
func (h *Host) Focus(ctx context.Context, agentName string) error {
	r, err := h.find(ctx, agentName, true)
	if err != nil {
		return err
	}
	_, err = h.onPane(ctx, agentName, "select-window", "select-window", "-t", r.Pane)
	return err
}

// Relabel shows tab in the pane title (the label) and the window's @deskpixie_label and
// @deskpixie_title options. The window's name stays the agent's name.
func (h *Host) Relabel(ctx context.Context, agentName string, tab agent.TabLabel) error {
	r, err := h.find(ctx, agentName, false)
	if err != nil {
		return err
	}
	_, err = h.onPane(ctx, agentName, "relabel", labelArgs(r.Pane, cmp.Or(tab.Label, agentName), tab.Title)...)
	return err
}

// labelArgs are the commands that show label and title on pane's window.
func labelArgs(pane, label, title string) []string {
	return []string{"set-option", "-w", "-t", pane, "@deskpixie_label", literalArg(label),
		";", "set-option", "-w", "-t", pane, "@deskpixie_title", literalArg(title),
		";", "select-pane", "-t", pane, "-T", formatArg(label)}
}

// literalArg is s as one tmux argument that arrives unchanged. tmux reads its own argv as
// commands: an argument ending in ";" ends the command there (and loses the ";"), and "\;" at
// the end stands for ";". So a final ";" is written "\;" (whatever precedes it). Nothing else
// in an argument is special unless the command expands it as a format (formatArg).
func literalArg(s string) string {
	if strings.HasSuffix(s, ";") {
		return s[:len(s)-1] + `\;`
	}
	return s
}

// formatArg is s as an argument tmux expands as a format (new-window -c, select-pane -T):
// "#" doubled, so "#H" or "#{…}" stay text, then as literalArg.
func formatArg(s string) string { return literalArg(strings.ReplaceAll(s, "#", "##")) }

// ReportStatus is agent.StatusReporter: a Claude agent's own hook said its status.
func (h *Host) ReportStatus(agentName string, status agent.PaneStatus) {
	h.det.Hook(agentName, status)
}

// AttachCommand is the argv that attaches a terminal to the session.
func (h *Host) AttachCommand() []string {
	return []string{h.bin, "-L", h.session, "attach", "-t", h.session}
}

// startPolls starts the screen and exit polls (once, until Shutdown). They cost nothing while
// no agent is tracked.
func (h *Host) startPolls() {
	h.pollOnce.Do(func() {
		go h.poll(h.screenPoll, h.pollScreens)
		go h.poll(h.exitPoll, h.pollExits)
	})
}

func (h *Host) poll(every time.Duration, f func(ctx context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			c, cancel := context.WithTimeout(h.ctx, DefaultTimeout)
			f(c)
			cancel()
		}
	}
}

func (h *Host) pollScreens(ctx context.Context) {
	for _, pane := range h.det.ScreenPanes() {
		if screen, err := h.capture(ctx, pane, 0); err == nil {
			h.det.Screen(pane, screen)
		}
	}
}

func (h *Host) pollExits(ctx context.Context) {
	if h.det.Len() == 0 {
		return
	}
	known := h.det.Snapshot()
	rows, gone, err := h.rows(ctx)
	if err != nil {
		return // a slow or failed list says nothing about the agents
	}
	if gone {
		rows = nil
	}
	h.det.Listed(known, rows)
}

// subscriber is one Subscribe channel, unbounded (never drops).
type subscriber struct {
	mu   sync.Mutex
	q    []agent.PaneEvent
	wake chan struct{}
	out  chan agent.PaneEvent
}

func (s *subscriber) push(ev agent.PaneEvent) {
	s.mu.Lock()
	s.q = append(s.q, ev)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) run(done <-chan struct{}) {
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.q) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-done:
				return
			}
		}
		ev := s.q[0]
		s.q = s.q[1:]
		s.mu.Unlock()
		select {
		case s.out <- ev:
		case <-done:
			return
		}
	}
}

func (h *Host) broadcast(ev agent.PaneEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for _, s := range h.subs {
		s.push(ev)
	}
}

// Subscribe returns status changes and exits from now on (nothing is replayed). The channel is
// unbounded and closes on Shutdown.
func (h *Host) Subscribe() <-chan agent.PaneEvent {
	s := &subscriber{wake: make(chan struct{}, 1), out: make(chan agent.PaneEvent)}
	h.subsMu.Lock()
	h.subs = append(h.subs, s)
	h.subsMu.Unlock()
	go s.run(h.done)
	return s.out
}
