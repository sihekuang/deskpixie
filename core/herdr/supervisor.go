package herdr

// Runs the named Herdr server (`HERDR_SESSION=<name> herdr server`, detached) with a scrubbed
// environment and checks the protocol and the Claude integration. Never touches any other
// session (command-center #83): every CLI call carries HERDR_SESSION, and the session itself
// must be named (never empty, never Herdr's "default"). Never installs the integration: that
// writes into the user's global ~/.claude.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/herdr/compat"
)

// SupportedVersions is the Herdr versions this adapter speaks, as the user reads them:
// Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22). The table is compat.Supported.
const SupportedVersions = compat.SupportedVersions

// SupportedProtocol: the adapter speaks socket protocol p (one of compat.Supported).
func SupportedProtocol(p uint32) bool { return compat.IsSupported(p) }

// Supervisor timing (verbatim from Rust): status poll while a server starts, and how long it
// may take.
const (
	SupervisorPoll         = 250 * time.Millisecond
	SupervisorStartTimeout = 15 * time.Second
)

// CheckSession: ^[a-z0-9][a-z0-9-]{0,47}$ and not Herdr's "default" session.
func CheckSession(session string) error {
	ok := len(session) >= 1 && len(session) <= 48 && session != "default"
	for i := 0; ok && i < len(session); i++ {
		c := session[i]
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		ok = lower || digit || (i > 0 && c == '-')
	}
	if ok {
		return nil
	}
	return agent.HostError{Kind: agent.HostOther,
		Detail: fmt.Sprintf("herdr session must be a dedicated name (a-z, 0-9, -; not \"default\"), not %q", session)}
}

// ServerEnv is the server's (and every CLI call's) exact environment: parent minus the
// scrubbed keys (proc.Scrubbed), PATH (from parent) extended with the usual install dirs (a GUI
// app's PATH is minimal), plus HERDR_SESSION; sorted. Rejects an empty or "default" session.
func ServerEnv(parent []agent.EnvVar, session, home string) ([]agent.EnvVar, error) {
	if err := CheckSession(session); err != nil {
		return nil, err
	}
	return proc.ServerEnv(parent, home, nil, agent.EnvVar{Key: "HERDR_SESSION", Value: session}), nil
}

// FindHerdr: configured (agent.herdrPath, "" for none), else herdr on pathVar, else in the
// install dirs ServerEnv adds (~/.local/bin, /opt/homebrew/bin, /usr/local/bin).
func FindHerdr(configured, pathVar, home string, exists func(string) bool) (string, bool) {
	return proc.FindProgram("herdr", configured, pathVar, proc.InstallDirs(home), exists)
}

// ServerStatus is `herdr status --json`. Nil pointers are absent (Rust's None); the versions
// are "" when absent.
type ServerStatus struct {
	Running        bool
	ClientProtocol uint32
	ClientVersion  string
	ServerProtocol *uint32
	ServerVersion  string
	Compatible     *bool
	Socket         string
	Session        *string
}

func asU32(v any) (uint32, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil || i < 0 {
		return 0, false
	}
	return uint32(i), true
}

// ParseStatus reads `herdr status --json` (captured from 0.7.5 and 0.9.4: both carry
// client.{version,protocol} and server.{running,version,protocol,compatible,socket,session};
// 0.9.4 adds endpoint_compatible, restart_needed and more, which are not needed).
func ParseStatus(text string) (ServerStatus, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return ServerStatus{}, fmt.Errorf("herdr status --json: %v", err)
	}
	server, ok := v["server"].(map[string]any)
	if !ok {
		return ServerStatus{}, fmt.Errorf("herdr status --json: no server")
	}
	var s ServerStatus
	s.Running, _ = server["running"].(bool)
	if client, ok := v["client"].(map[string]any); ok {
		s.ClientProtocol, _ = asU32(client["protocol"])
		s.ClientVersion, _ = client["version"].(string)
	}
	s.ServerVersion, _ = server["version"].(string)
	if p, ok := asU32(server["protocol"]); ok {
		s.ServerProtocol = &p
	}
	if c, ok := server["compatible"].(bool); ok {
		s.Compatible = &c
	}
	socket, ok := server["socket"].(string)
	if !ok {
		return ServerStatus{}, fmt.Errorf("herdr status --json: no socket")
	}
	s.Socket = socket
	if session, ok := server["session"].(string); ok {
		s.Session = &session
	}
	return s, nil
}

// ClaudeIntegrationCurrent: `herdr integration status` says the Claude integration is current.
func ClaudeIntegrationCurrent(statusText string) bool {
	for _, l := range rustLines(statusText) {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), "claude: current") {
			return true
		}
	}
	return false
}

// Supervisor runs and checks the session's Herdr server through the CLI.
type Supervisor struct {
	bin          string
	env          []agent.EnvVar
	session      string
	runner       agent.CommandRunner
	poll         time.Duration
	startTimeout time.Duration

	integrationOnce       sync.Once
	integrationMissing    atomic.Bool
	unknownProtocolLogged atomic.Bool
	// protocol is the one the CLI and the running server agreed on (0 until then).
	protocol atomic.Uint32
}

// NewSupervisor: env is ServerEnv(...); its HERDR_SESSION names the session (empty or
// "default" rejected).
func NewSupervisor(bin string, env []agent.EnvVar, runner agent.CommandRunner) (*Supervisor, error) {
	session := ""
	for _, e := range env {
		if e.Key == "HERDR_SESSION" {
			session = e.Value
			break
		}
	}
	if err := CheckSession(session); err != nil {
		return nil, err
	}
	return &Supervisor{bin: bin, env: env, session: session, runner: runner,
		poll: SupervisorPoll, startTimeout: SupervisorStartTimeout}, nil
}

// WithTiming sets the start poll and timeout (tests).
func (s *Supervisor) WithTiming(poll, startTimeout time.Duration) *Supervisor {
	s.poll, s.startTimeout = poll, startTimeout
	return s
}

func (s *Supervisor) Bin() string { return s.bin }

// Protocol is the socket protocol the CLI and the running server last agreed on (one of
// compat.Supported), 0 before Ensure or Running found one. The host keys version-specific
// behaviour on it.
func (s *Supervisor) Protocol() uint32 { return s.protocol.Load() }

// IntegrationMissing is true after Ensure found the Claude integration missing or outdated.
// The app tells the user to run `herdr integration install claude`; we never do (it writes to
// ~/.claude).
func (s *Supervisor) IntegrationMissing() bool { return s.integrationMissing.Load() }

// runError maps a runner error to a HostError: only a program that isn't there (ENOENT) is
// NotFound (which turns delegation off); a timeout is Timeout; anything else is Other.
func runErrorToHost(err error) error {
	msg := err.Error()
	switch {
	case proc.IsNotFound(err):
		return agent.HostError{Kind: agent.HostNotFound}
	case strings.Contains(msg, "timed out"):
		return agent.HostError{Kind: agent.HostTimeout, Detail: "herdr did not answer"}
	}
	return agent.HostError{Kind: agent.HostOther, Detail: "herdr could not be run"}
}

// cli is one blocking CLI call.
func (s *Supervisor) cli(args ...string) (agent.CommandOutput, error) {
	argv := append([]string{s.bin}, args...)
	out, err := s.runner.Run(argv, s.env)
	if err != nil {
		return out, runErrorToHost(err)
	}
	return out, nil
}

// statusErr is why a status call failed: the poll loop retries bad ones but must stop on a
// wrong session.
type statusErr struct {
	wrongSession bool
	err          error
}

func (s *Supervisor) parse(out agent.CommandOutput) (ServerStatus, *statusErr) {
	st, err := ParseStatus(out.Stdout)
	if err != nil {
		return st, &statusErr{err: agent.HostError{Kind: agent.HostOther, Detail: err.Error()}}
	}
	if st.Session != nil && *st.Session != s.session {
		return st, &statusErr{wrongSession: true, err: agent.HostError{Kind: agent.HostOther,
			Detail: fmt.Sprintf("herdr reports session %q, expected %q", *st.Session, s.session)}}
	}
	return st, nil
}

func (s *Supervisor) statusCall() (ServerStatus, *statusErr) {
	out, err := s.cli("status", "--json")
	if err != nil {
		return ServerStatus{}, &statusErr{err: err}
	}
	return s.parse(out)
}

// Status is the server's status (blocking; prefer Ensure).
func (s *Supervisor) Status() (ServerStatus, error) {
	st, e := s.statusCall()
	if e != nil {
		return st, e.err
	}
	return st, nil
}

// release is how a status names a Herdr: "0.9.4 (protocol 22)", or "protocol 22" without a
// version.
func release(version string, protocol uint32) string {
	if version == "" {
		return fmt.Sprintf("protocol %d", protocol)
	}
	return fmt.Sprintf("%s (protocol %d)", version, protocol)
}

// versionMismatch: the CLI and the server don't agree on a protocol this adapter speaks. Client
// and Server are the protocols they report (Server 0 when there is none); Detail says what the
// user has and which versions are supported, for the status line.
func (s *Supervisor) versionMismatch(st ServerStatus) error {
	server := uint32(0)
	if st.ServerProtocol != nil {
		server = *st.ServerProtocol
	}
	var detail string
	switch {
	case !SupportedProtocol(st.ClientProtocol):
		detail = "you have herdr " + release(st.ClientVersion, st.ClientProtocol)
	case SupportedProtocol(server):
		// Both supported but different: the session still runs the server of another version
		// (herdr was updated). Restarting it closes its panes, so that is the user's call.
		detail = fmt.Sprintf("herdr is %s but session %s still runs %s: restart it with `herdr session stop %s`",
			release(st.ClientVersion, st.ClientProtocol), s.session, release(st.ServerVersion, server), s.session)
	default:
		detail = fmt.Sprintf("herdr is %s but its server is %s",
			release(st.ClientVersion, st.ClientProtocol), release(st.ServerVersion, server))
	}
	return agent.HostError{Kind: agent.HostVersionMismatch, Client: st.ClientProtocol, Server: server,
		Detail: detail + "; DeskPixie supports " + SupportedVersions}
}

// ready: running with a protocol the CLI and the server agree on and this adapter speaks? (The
// CLI's own protocol is checked first, by agreed.) A running server that doesn't say which
// protocol it speaks is treated as the CLI's (a second server must never be started over it).
func (s *Supervisor) ready(st ServerStatus) (bool, error) {
	if !st.Running {
		return false, nil
	}
	switch {
	case st.Compatible != nil && !*st.Compatible:
		return false, s.versionMismatch(st)
	case st.ServerProtocol != nil && *st.ServerProtocol != st.ClientProtocol:
		return false, s.versionMismatch(st)
	}
	if st.ServerProtocol == nil && !s.unknownProtocolLogged.Swap(true) {
		log.Printf("agent: herdr is running but reports no protocol; assuming the CLI's (%d)", st.ClientProtocol)
	}
	s.protocol.Store(st.ClientProtocol)
	return true, nil
}

// agreed: the CLI speaks a supported protocol (whether or not a server runs).
func (s *Supervisor) agreed(st ServerStatus) error {
	if !SupportedProtocol(st.ClientProtocol) {
		return s.versionMismatch(st)
	}
	return nil
}

// Running is the socket of this session's server if it is running and speaks a supported
// protocol its CLI agrees with (ok false when it isn't running). Never starts one (for calls
// like closing a pane, where no server means nothing to do).
func (s *Supervisor) Running(ctx context.Context) (string, bool, error) {
	st, e := s.statusCall()
	if e != nil {
		return "", false, e.err
	}
	if err := s.agreed(st); err != nil {
		return "", false, err
	}
	ok, err := s.ready(st)
	if err != nil || !ok {
		return "", false, err
	}
	return st.Socket, true, nil
}

// Ensure: the server for this session is running and speaks a supported protocol its CLI
// agrees with; returns its socket.
func (s *Supervisor) Ensure(ctx context.Context) (string, error) {
	first, e := s.statusCall()
	if e != nil {
		return "", e.err
	}
	if err := s.agreed(first); err != nil {
		return "", err
	}
	ok, err := s.ready(first)
	if err != nil {
		return "", err
	}
	socket := first.Socket
	if !ok {
		if err := s.runner.SpawnDetached([]string{s.bin, "server"}, s.env); err != nil {
			return "", runErrorToHost(err)
		}
		deadline := time.Now().Add(s.startTimeout)
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(s.poll):
			}
			st, e := s.statusCall()
			if e == nil {
				ok, err := s.ready(st)
				if err != nil {
					return "", err
				}
				if ok {
					socket = st.Socket
					break
				}
			} else if e.wrongSession {
				return "", e.err
			}
			if !time.Now().Before(deadline) {
				return "", agent.HostError{Kind: agent.HostTimeout, Detail: "herdr server did not start"}
			}
		}
	}
	s.checkIntegration()
	return socket, nil
}

// checkIntegration records whether the Claude integration is current (once per run).
// Read-only.
func (s *Supervisor) checkIntegration() {
	s.integrationOnce.Do(func() {
		out, err := s.cli("integration", "status")
		s.integrationMissing.Store(!(err == nil && ClaudeIntegrationCurrent(out.Stdout)))
	})
}
