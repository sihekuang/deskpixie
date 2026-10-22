//go:build live

package herdr_test

// Opt-in live smokes against the installed Herdr (one of herdr.SupportedVersions), Claude Code (and optionally Codex) and git:
// ports of ai-mascot crates/session-herdr/examples/agent_smoke.rs and worker_smoke.rs (D-543,
// D-544). Run with:
//
//	go test -tags live -run Live -v -timeout 15m ./herdr/
//
// Everything is throwaway: the Herdr session smoketest.SmokeSession ("deskpixie-smoke", never
// the app's "deskpixie"), temp workspaces and projects roots, and hook servers on free loopback
// ports (never the app's 48217). The session is stopped and deleted at the end (SMOKE_KEEP=1
// keeps it). Like the Rust smokes, Claude Code records folder trust for the temp workspace and
// worktrees in ~/.claude.json. No tokens or env values are printed.
//
// Knobs: SMOKE_MODEL (worker smoke coordinator, default sonnet); SMOKE_CODEX=1 adds the Codex
// part of worker_smoke (needs codex).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
)

const (
	smokeJob  = "job_00000000beef"
	smokeTurn = 2 * time.Minute
)

func info(t *testing.T, question, answer string) { t.Logf("info %s: %s", question, answer) }
func ok(t *testing.T, step string)               { t.Logf("ok   %s", step) }

// hookServer serves the hook/MCP endpoints on a free loopback port and hands every event to
// deliver. Stopped at the end of the test.
func hookServer(t *testing.T, tokens *httpserver.Tokens, deliver func(agent.HookEvent) bool) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("the smoke must never use the app's port 48217")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = httpserver.Serve(ctx, ln, tokens, httpserver.DeliverFunc(deliver))
	}()
	t.Cleanup(func() { cancel(); <-done })
	return port
}

// describe is a redacted view of one agent.list entry.
func describe(e map[string]any) string {
	pane, _ := e["pane_id"].(string)
	agentField := "absent"
	if a, ok := e["agent"].(string); ok {
		agentField = "present (" + a + ")"
	}
	return fmt.Sprintf("pane=%s agent=%s status=%v launch_pending=%v interactive_ready=%v",
		pane, agentField, e["agent_status"], e["launch_pending"], e["interactive_ready"])
}

func rawAgents(ctx context.Context, tr herdr.Transport) ([]map[string]any, error) {
	r, err := tr.Request(ctx, "agent.list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("agent.list: %v", herdr.RPCCode(err))
	}
	var v struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(r, &v); err != nil {
		return nil, err
	}
	return v.Agents, nil
}

// waitPrompt answers Claude Code's folder-trust dialog if it shows ("No, exit" is preselected:
// Down, Enter) and waits for the ready prompt.
func waitPrompt(ctx context.Context, t *testing.T, host *herdr.Host, name string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		screen, _ := host.Read(ctx, name, 40)
		if strings.Contains(screen, "trust this folder") {
			_ = host.SendKeys(ctx, name, []string{"Down"})
			time.Sleep(500 * time.Millisecond)
			_ = host.SendKeys(ctx, name, []string{"Enter"})
			time.Sleep(4 * time.Second)
			ok(t, "folder-trust dialog answered")
			return
		}
		if strings.Contains(screen, "for shortcuts") || strings.Contains(screen, "Try \"") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached a prompt; pane:\n%s", name, screen)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitStop(t *testing.T, hooks <-chan agent.HookEvent) (string, error) {
	t.Helper()
	timer := time.NewTimer(smokeTurn)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return "", fmt.Errorf("no Stop hook within %v", smokeTurn)
		case ev := <-hooks:
			switch ev.Kind {
			case agent.HookStop:
				return ev.Message, nil
			case agent.HookPermission:
				ev.Reply <- agent.Deny("Unexpected in this step.")
				return "", fmt.Errorf("unexpected permission request for %s", ev.ToolName)
			}
		}
	}
}

func waitPermission(t *testing.T, hooks <-chan agent.HookEvent, d agent.Decision) error {
	t.Helper()
	const q = "permissions.ask raises PermissionRequest under --permission-mode auto"
	timer := time.NewTimer(smokeTurn)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			info(t, q, "no (no hook within 2 minutes)")
			return fmt.Errorf("no PermissionRequest hook within %v", smokeTurn)
		case ev := <-hooks:
			switch ev.Kind {
			case agent.HookPermission:
				info(t, q, "yes")
				t.Logf("     permission asked for %s %v", ev.ToolName, ev.ToolInput)
				ev.Reply <- d
				return nil
			case agent.HookStop:
				info(t, q, "no (the turn ended without a permission request)")
				return fmt.Errorf("turn ended without a permission request: %q", ev.Message)
			}
		}
	}
}

// TestLiveAgentSmoke is agent_smoke.rs: a coordinator on the subscription (the server env
// scrubs the parent's API key), a Stop-hook reply with its <spoken ref>, a PermissionRequest
// allowed then denied, agent.list after /exit, and the SessionHost conformance suite, live.
func TestLiveAgentSmoke(t *testing.T) {
	// The parent "has" an API key: the server env must not carry it (checked on the banner).
	sm := smoketest.Smoke(t, agent.EnvVar{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-smoke-not-a-real-key"})
	for _, e := range sm.Env {
		if e.Key == "ANTHROPIC_API_KEY" {
			t.Fatal("ANTHROPIC_API_KEY not scrubbed from the server env")
		}
	}
	ctx := context.Background()
	tmp := smoketest.CanonicalTemp(t)
	ws, canvas := filepath.Join(tmp, "workspace"), filepath.Join(tmp, "canvas")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte(agentapp.RenderClaudeMd(canvas, "")), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hook server on a free loopback port; settings from the shipped template plus an ask rule
	// for `touch`.
	tokens := httpserver.NewTokens()
	token := tokens.Issue(agent.Coordinator)
	hooks := make(chan agent.HookEvent, 256)
	port := hookServer(t, tokens, func(ev agent.HookEvent) bool { hooks <- ev; return true })
	const anchor = `"Bash(git push:*)",`
	settings := agentapp.RenderSettings(port)
	if !strings.Contains(settings, anchor) {
		t.Fatal("the settings template no longer has the git push ask rule to anchor on")
	}
	settings = strings.Replace(settings, anchor, anchor+"\n      \"Bash(touch:*)\",", 1)
	settingsPath := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	sup := sm.Supervisor(t)
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sup)
	t.Cleanup(host.Shutdown)
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatalf("ensure_session: %v", err)
	}
	ok(t, "session "+smoketest.SmokeSession+" running")
	info(t, "integration_missing", fmt.Sprint(host.IntegrationMissing()))

	status, err := sup.Status()
	if err != nil {
		t.Fatalf("herdr status: %v", err)
	}
	echo := "no (no session field)"
	if status.Session != nil {
		echo = "no (a different name came back)"
		if *status.Session == smoketest.SmokeSession {
			echo = "yes"
		}
	}
	info(t, "status --json echoes the session name verbatim", echo)
	tr := herdr.NewUnixTransport(status.Socket)

	// Watch agent.list while the coordinator is launching (StartAgent returns when ready).
	var launchMu sync.Mutex
	launching := ""
	wctx, stopWatch := context.WithCancel(ctx)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for wctx.Err() == nil {
			if list, err := rawAgents(wctx, tr); err == nil {
				for _, e := range list {
					if e["launch_pending"] == true {
						launchMu.Lock()
						if launching == "" {
							launching = describe(e)
						}
						launchMu.Unlock()
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	// `auto` is what the app passes; --model haiku keeps the turns cheap.
	args := []string{"--model", "haiku", "--permission-mode", "auto", "--settings", settingsPath}
	spec := func(name string) agent.AgentSpec {
		return agent.AgentSpec{Name: name, Kind: agent.Claude, Cwd: ws, Args: args,
			Env: []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: token}}}
	}
	_, err = host.StartAgent(ctx, spec(agent.Coordinator))
	stopWatch()
	<-watched
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	launchMu.Lock()
	if launching == "" {
		launching = "not observed (StartAgent was faster than the 100 ms poll)"
	}
	info(t, "agent.list while the agent is launch_pending", launching)
	launchMu.Unlock()
	waitPrompt(ctx, t, host, agent.Coordinator)
	banner, err := host.Read(ctx, agent.Coordinator, 40)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(banner, "API Usage Billing") {
		t.Fatal("the coordinator runs on an API key, not the subscription")
	}
	ok(t, "coordinator started on the subscription (env scrub holds)")

	// 1. A reply comes back through the Stop hook with the job's ref.
	if err := host.Prompt(ctx, agent.Coordinator, "New request from the user: what is two plus two?\n[ref "+smokeJob+"]"); err != nil {
		t.Fatal(err)
	}
	message, err := waitStop(t, hooks)
	if err != nil {
		screen, _ := host.Read(ctx, agent.Coordinator, 40)
		t.Fatalf("%v\n--- coordinator pane ---\n%s\n---", err, screen)
	}
	blocks := agent.ParseSpoken(message)
	matching := false
	for _, b := range blocks {
		matching = matching || (b.Reference != nil && *b.Reference == smokeJob)
	}
	info(t, "Stop hook last_assistant_message carries the full <spoken ref status> block", fmt.Sprintf(
		"%v (len %d, ref= %v, status= %v, parsed blocks %d, matching ref %v)",
		strings.Contains(message, "<spoken") && strings.Contains(message, "</spoken>"), len(message),
		strings.Contains(message, "ref="), strings.Contains(message, "status="), len(blocks), matching))
	if !matching {
		t.Fatalf("no <spoken ref=%q> in the Stop hook message: %q", smokeJob, message)
	}
	ok(t, "Stop hook delivered <spoken ref=…>")

	// 2. An ask-rule command goes through the PermissionRequest hook: allowed once, then denied.
	for _, c := range []struct {
		file  string
		allow bool
	}{{"smoke-allowed.txt", true}, {"smoke-denied.txt", false}} {
		if err := host.Prompt(ctx, agent.Coordinator, "Run this exact shell command and nothing else: touch "+c.file+"\n[ref "+smokeJob+"]"); err != nil {
			t.Fatal(err)
		}
		d := agent.Allow()
		if !c.allow {
			d = agent.Deny("Smoke test says no.")
		}
		if err := waitPermission(t, hooks, d); err != nil {
			t.Fatal(err)
		}
		if _, err := waitStop(t, hooks); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(ws, c.file)); (err == nil) != c.allow {
			t.Fatalf("%s: exists = %v, expected %v", c.file, err == nil, c.allow)
		}
	}
	ok(t, "PermissionRequest hook: allow ran the command, deny blocked it")

	// 3. After /exit the pane stays: what does agent.list show for it?
	if err := host.Prompt(ctx, agent.Coordinator, "/exit"); err != nil {
		t.Fatal(err)
	}
	afterExit := "no listing"
	for range 60 {
		time.Sleep(500 * time.Millisecond)
		list, err := rawAgents(ctx, tr)
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]any
		for _, e := range list {
			if e["name"] == agent.Coordinator {
				entry = e
			}
		}
		if entry == nil {
			afterExit = "entry gone from agent.list (the pane did not stay)"
			continue
		}
		if _, has := entry["agent"]; !has && entry["launch_pending"] != true {
			afterExit = describe(entry)
			break
		}
		afterExit = "still listed with an agent: " + describe(entry)
	}
	info(t, "agent.list after /exit with the pane kept", afterExit)
	_ = host.Close(ctx, agent.Coordinator)

	// 4. The SessionHost conformance suite, live (prompt, Escape, close, no duplicate Exited).
	steps, err := agenttest.RunConformance(ctx, host, spec("probe"), agenttest.Timeouts{Turn: smokeTurn, Settle: 3 * time.Second})
	for _, s := range steps {
		ok(t, "conformance: "+s)
	}
	if err != nil {
		if list, lerr := rawAgents(ctx, tr); lerr == nil {
			for _, a := range list {
				t.Logf("agent.list at failure: %s", describe(a))
			}
		}
		screen, _ := host.Read(ctx, "probe", 30)
		t.Fatalf("conformance: %v\n--- probe pane ---\n%s\n---", err, screen)
	}
	// One Stop for the probe's completed first turn; a second one would mean Claude Code also
	// fires Stop when Escape interrupts a turn.
	stops := 0
	for len(hooks) > 0 {
		if ev := <-hooks; ev.Kind == agent.HookStop {
			stops++
		}
	}
	info(t, "Stop hooks during conformance (1 = Escape fires no Stop)", fmt.Sprint(stops))
	_ = host.Close(ctx, "probe")
}

// --- worker_smoke.rs ---

const (
	jobLimit     = 8 * time.Minute
	overallLimit = 20 * time.Minute
	gitPath      = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
)

type wallClock struct{}

func (wallClock) NowMs() agent.Millis { return agent.Millis(time.Now().UnixMilli()) }

func gitIn(dir, home string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "user.name=smoke", "-c", "user.email=smoke@example.invalid", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = []string{"PATH=" + gitPath, "HOME=" + home}
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("git %v: %v %s", args, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

// tomlString is a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// trustOverride is the smoke-only Codex override that trusts repo for the run, written
// nowhere: `-c projects={"<repo>"={trust_level="trusted"}}`.
func trustOverride(repo string) []string {
	return []string{"-c", fmt.Sprintf(`projects={%s={trust_level="trusted"}}`, tomlString(repo))}
}

var callNames = map[agent.McpCallKind]string{
	agent.CallStartWorker: "start_worker", agent.CallMessageWorker: "message_worker", agent.CallStopWorker: "stop_worker",
	agent.CallListWorkers: "list_workers", agent.CallReportProgress: "report_progress", agent.CallReportResult: "report_result",
	agent.CallAskCoordinator: "ask_coordinator", agent.CallReportBlocked: "report_blocked",
}

type startReply struct {
	name string
	err  string
}

type workerSmoke struct {
	t         *testing.T
	handle    *agent.AgentHandle
	host      *herdr.Host
	mu        sync.Mutex
	seen      []string
	starts    []startReply
	repo      string
	untrusted string
	codexConf string
	home      string
	mcpDir    string
}

func (s *workerSmoke) saw(entry string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.seen {
		if e == entry {
			return true
		}
	}
	return false
}

func (s *workerSmoke) view(id agent.JobID) (agent.JobView, bool) {
	for _, j := range s.handle.JobStatus() {
		if j.JobID == string(id) {
			return j, true
		}
	}
	return agent.JobView{}, false
}

// job delegates task and waits until the job closes (or jobLimit).
func (s *workerSmoke) job(task string) (agent.JobID, agent.JobView, error) {
	id, err := s.handle.Delegate(task, "")
	if err != nil {
		return "", agent.JobView{}, err
	}
	deadline := time.Now().Add(jobLimit)
	for {
		time.Sleep(2 * time.Second)
		if v, ok := s.view(id); ok && !v.Status.IsOpen() {
			return id, v, nil
		}
		if time.Now().After(deadline) {
			return id, agent.JobView{}, fmt.Errorf("%s did not finish within %v", id, jobLimit)
		}
	}
}

// dump logs agent.list, every pane's screen, the hook/MCP traffic and the jobs.
func (s *workerSmoke) dump() {
	ctx := context.Background()
	list, err := s.host.List(ctx)
	if err != nil {
		s.t.Logf("agent.list failed: %v", err)
	}
	for _, a := range list {
		screen, err := s.host.Read(ctx, a.Name, 40)
		s.t.Logf("agent.list: %s status=%v\n--- %s ---\n%s\n--- (%v)", a.Name, a.Status, a.Name, screen, err)
	}
	s.mu.Lock()
	s.t.Logf("hook/MCP traffic: %q", s.seen)
	s.mu.Unlock()
	for _, j := range s.handle.JobStatus() {
		s.t.Logf("job %s %v workers=%+v last_progress=%v", j.JobID, j.Status, j.Workers, j.LastProgress)
	}
}

// keptThenClosed (#237): a worker that made something is still open after its job ends, and the
// coordinator closes it once the person says they're done with it.
func (s *workerSmoke) keptThenClosed(worker, made string) error {
	if s.gone(worker, 10*time.Second) {
		return fmt.Errorf("%s was closed when its job ended, before the person was done with it", worker)
	}
	ok(s.t, fmt.Sprintf("%s stays open after its job ended", worker))
	id, view, err := s.job("That " + made + " looks good, thanks. You can close the worker that made it now.")
	if err != nil {
		return err
	}
	if view.Status != agent.JobDone {
		return fmt.Errorf("%s ended %v (last progress: %v)", id, view.Status, view.LastProgress)
	}
	if !s.gone(worker, 30*time.Second) {
		return fmt.Errorf("%s is still open after the person said to close it", worker)
	}
	if _, err := os.Stat(filepath.Join(s.mcpDir, worker+".json")); err == nil {
		return fmt.Errorf("%s's MCP config was not deleted", worker)
	}
	ok(s.t, fmt.Sprintf("%s closed once the person said to, and its MCP config is deleted", worker))
	return nil
}

func (s *workerSmoke) gone(name string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		list, err := s.host.List(context.Background())
		if err == nil {
			found := false
			for _, a := range list {
				found = found || a.Name == name
			}
			if !found {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// committed: the worker's worktree has file committed on pixie/<slug>, and the main checkout
// is untouched.
func (s *workerSmoke) committed(slug, file string) error {
	wt := filepath.Join(s.repo, ".worktrees", slug)
	branch, err := gitIn(wt, s.home, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if branch != "pixie/"+slug {
		return fmt.Errorf("the worktree is on %s, not pixie/%s", branch, slug)
	}
	if log, err := gitIn(wt, s.home, "log", "--oneline", "-1", "--", file); err != nil || log == "" {
		return fmt.Errorf("%s was not committed in the worktree (%v)", file, err)
	}
	if st, err := gitIn(s.repo, s.home, "status", "--porcelain"); err != nil || st != "" {
		return fmt.Errorf("the main checkout changed: %q (%v)", st, err)
	}
	exclude, _ := os.ReadFile(filepath.Join(s.repo, ".git/info/exclude"))
	if !strings.Contains("\n"+string(exclude), "\n.worktrees/\n") && !strings.HasSuffix(string(exclude), "\n.worktrees/") {
		return fmt.Errorf(".worktrees/ is not in .git/info/exclude")
	}
	return nil
}

// answerTrustDialogs answers Claude Code's folder-trust dialog in any pane younger than a
// minute, once per pane, until ctx ends.
func answerTrustDialogs(ctx context.Context, host *herdr.Host, answered *[]string, mu *sync.Mutex) {
	firstSeen := map[string]time.Time{}
	done := map[string]bool{}
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		list, err := host.List(ctx)
		if err != nil {
			continue
		}
		for _, a := range list {
			if _, ok := firstSeen[a.Name]; !ok {
				firstSeen[a.Name] = time.Now()
			}
			if done[a.Name] || time.Since(firstSeen[a.Name]) >= time.Minute {
				continue
			}
			screen, _ := host.Read(ctx, a.Name, 40)
			if strings.Contains(screen, "trust this folder") && strings.Contains(screen, "No, exit") &&
				!strings.Contains(screen, "for shortcuts") && !strings.Contains(screen, "Do you want to") {
				done[a.Name] = true
				_ = host.SendKeys(ctx, a.Name, []string{"Down"})
				time.Sleep(500 * time.Millisecond)
				_ = host.SendKeys(ctx, a.Name, []string{"Enter"})
				mu.Lock()
				*answered = append(*answered, a.Name)
				mu.Unlock()
			}
		}
	}
}

// TestLiveWorkerSmoke is worker_smoke.rs: the real delegation runtime with workers and the real
// adapters (herdr.Host, httpserver, GitWorktrees, FileWorkerRecipes, ClaudeTrustStore); only
// voice, the notifier and the job store are in-memory fakes. A Claude worker commits a file on
// its branch, a cancelled job stops its worker, and shutdown closes every pane.
func TestLiveWorkerSmoke(t *testing.T) {
	codex := os.Getenv("SMOKE_CODEX") == "1"
	if codex {
		if _, err := exec.LookPath("codex"); err != nil {
			if _, err := os.Stat("/opt/homebrew/bin/codex"); err != nil {
				t.Skip("SMOKE_CODEX=1 but codex not found")
			}
		}
	}
	sm := smoketest.Smoke(t)
	ctx := context.Background()
	tmp := smoketest.CanonicalTemp(t)
	// The repos: projects/demo and projects/untrusted, one commit each.
	projects := filepath.Join(tmp, "projects")
	repo, untrusted := filepath.Join(projects, "demo"), filepath.Join(projects, "untrusted")
	for _, r := range []string{repo, untrusted} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := gitIn(r, sm.Home, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r, "README.md"), []byte(filepath.Base(r)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := gitIn(r, sm.Home, "add", "README.md"); err != nil {
			t.Fatal(err)
		}
		if _, err := gitIn(r, sm.Home, "commit", "-q", "-m", "first"); err != nil {
			t.Fatal(err)
		}
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(sm.Home, ".codex")
	}

	// App data: the coordinator's workspace (workers CLAUDE.md), settings and MCP configs.
	paths := agentapp.NewPaths(filepath.Join(tmp, "data"))
	tokens := httpserver.NewTokens()
	coordinatorToken := tokens.Issue(agent.Coordinator)

	ws := &workerSmoke{t: t, repo: repo, untrusted: untrusted, codexConf: filepath.Join(codexHome, "config.toml"), home: sm.Home, mcpDir: paths.McpDir}
	var handle *agent.AgentHandle
	var handleReady = make(chan struct{})
	// The server's events go through a tap that records them, then on to the runtime.
	port := hookServer(t, tokens, func(ev agent.HookEvent) bool {
		<-handleReady
		entry := ev.Agent
		switch ev.Kind {
		case agent.HookMcp:
			entry += " " + callNames[ev.Call.Kind]
			if ev.Call.Kind == agent.CallStartWorker {
				toServer := ev.McpReply
				mine := make(chan agent.McpReply, 1)
				ev.McpReply = mine
				go func() {
					r, okr := <-mine
					if !okr {
						return
					}
					rep := startReply{}
					if r.Err != nil {
						rep.err = r.Err.Error()
					} else if m, isMap := r.Value.(map[string]any); isMap {
						rep.name, _ = m["name"].(string)
					} else {
						b, _ := json.Marshal(r.Value)
						var m map[string]any
						_ = json.Unmarshal(b, &m)
						rep.name, _ = m["name"].(string)
					}
					ws.mu.Lock()
					ws.starts = append(ws.starts, rep)
					ws.mu.Unlock()
					toServer <- r
				}()
			}
		case agent.HookStop:
			entry += " stop"
		case agent.HookPermission:
			entry += " permission " + ev.ToolName
		case agent.HookPostTool:
			entry += " post-tool " + ev.ToolName
		}
		ws.mu.Lock()
		ws.seen = append(ws.seen, entry)
		ws.mu.Unlock()
		return handle.Deliver(ev)
	})
	if err := agentapp.Install(paths, port, projects); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.McpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agentapp.WriteCoordinatorMcp(paths, port, coordinatorToken); err != nil {
		t.Fatal(err)
	}
	// Trust: the workspace now (like the app), worktrees when workers start (only under projects).
	claudeJSON := filepath.Join(sm.Home, ".claude.json")
	if _, err := agentfs.NewClaudeTrustStore(claudeJSON, []string{filepath.Join(tmp, "data")}).Grant(paths.Workspace); err != nil {
		t.Fatalf("trust the workspace: %v", err)
	}

	sup := sm.Supervisor(t)
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sup)
	t.Cleanup(host.Shutdown)
	ws.host = host
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatalf("ensure_session: %v", err)
	}
	ok(t, "session "+smoketest.SmokeSession+" running")
	if out, err := sm.Runner.Run([]string{sm.Bin, "integration", "status"}, sm.Env); err == nil {
		for _, l := range strings.Split(out.Stdout, "\n") {
			if strings.HasPrefix(l, "claude:") || strings.HasPrefix(l, "codex:") {
				info(t, "herdr integration", strings.SplitN(l, " (", 2)[0])
			}
		}
	}
	bctx, stopBabysitter := context.WithCancel(ctx)
	var trustedMu sync.Mutex
	var trusted []string
	babysat := make(chan struct{})
	go func() { defer close(babysat); answerTrustDialogs(bctx, host, &trusted, &trustedMu) }()

	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "sonnet"
	}
	recipe := agent.CoordinatorRecipe{
		Cwd:  paths.Workspace,
		Args: agentapp.CoordinatorArgs(paths, model, true),
		Env:  []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: coordinatorToken}},
	}
	deps := agent.Deps{
		Host:     agent.NewAgentHost(host, wallClock{}, recipe),
		Voice:    &agenttest.RecordingVoiceLink{},
		Notifier: &agenttest.RecordingNotifier{},
		Pixie:    &agenttest.RecordingNotifier{},
		Status:   &agenttest.RecordingStatus{},
		Planner:  &agenttest.FakePlanner{},
		Focus:    &agenttest.FakeFocusSink{},
		Store:    &agenttest.MemStore{},
		Clock:    wallClock{},
		IDs:      &agenttest.SeqIds{},
	}
	workerTrust := agentfs.NewClaudeTrustStore(claudeJSON, []string{projects})
	worktrees := agentfs.NewGitWorktrees(projects, paths.Workspace, sm.Home, proc.PatientRunner(agentapp.GitTimeout)).
		WithCleanupRunner(proc.PatientRunner(agentapp.GitCleanupTimeout))
	// Smoke only: a Codex worker in demo (and only there) is trusted for its run.
	extra := agentfs.CodexExtraArgs(func(r string) []string {
		if r == repo {
			return trustOverride(repo)
		}
		return nil
	})
	kinds := []agent.AgentKind{agent.Claude}
	if codex {
		kinds = append(kinds, agent.Codex)
	}
	settings := agent.DefaultSettings()
	settings.PermissionTimeoutMs = 10_000
	settings.Workers = agent.WorkerPolicy{Enabled: true, Max: 2, Kinds: kinds}
	handle = agent.NewRuntimeWithWorkers(deps, agent.RuntimeConfig{Settings: settings}, &agent.WorkerDeps{
		Worktrees: worktrees,
		Tokens:    tokens,
		Recipes:   agentfs.NewFileWorkerRecipes(paths.McpDir, paths.WorkerSettings, port, "haiku", workerTrust, "").WithCodexExtraArgs(extra),
	})
	ws.handle = handle
	close(handleReady)
	rctx, stopRuntime := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() { defer close(ran); handle.Run(rctx) }()

	errc := make(chan error, 1)
	go func() { errc <- ws.scenarios(codex) }()
	var outcome error
	select {
	case outcome = <-errc:
	case <-time.After(overallLimit):
		outcome = fmt.Errorf("the whole run did not finish within %v", overallLimit)
	}
	if outcome != nil {
		ws.dump()
		handle.Shutdown()
	}
	trustedMu.Lock()
	info(t, "folder-trust dialogs answered by the smoke", fmt.Sprint(trusted))
	trustedMu.Unlock()
	stopBabysitter()
	<-babysat
	stopRuntime()
	<-ran
	if outcome != nil {
		t.Fatal(outcome)
	}
}

func (s *workerSmoke) scenarios(codex bool) error {
	t := s.t
	ctx := context.Background()
	// 1. A Claude worker does a small repo task end to end.
	id, view, err := s.job("In the repository demo, have a claude worker create a file HELLO.txt containing the single word hi and commit it on its branch. Wait for its report, then tell me what it did.")
	if err != nil {
		return err
	}
	info(t, "the coordinator called start_worker over MCP", yesNo(s.saw("coordinator start_worker")))
	if len(view.Workers) == 0 {
		return fmt.Errorf("%s closed without a worker (status %v)", id, view.Status)
	}
	worker := view.Workers[0].Name
	slug := strings.TrimPrefix(worker, "demo-")
	info(t, "the Claude worker reported with report_result", yesNo(s.saw(worker+" report_result")))
	info(t, "the Claude worker's Stop hook reached the app", yesNo(s.saw(worker+" stop")))
	if view.Status != agent.JobDone {
		return fmt.Errorf("%s ended %v (last progress: %v)", id, view.Status, view.LastProgress)
	}
	if err := s.committed(slug, "HELLO.txt"); err != nil {
		return err
	}
	ok(t, fmt.Sprintf("claude worker %s: HELLO.txt committed on pixie/%s; the main checkout is untouched; .worktrees/ excluded", worker, slug))
	if err := s.keptThenClosed(worker, "HELLO.txt commit"); err != nil {
		return err
	}

	// 2. Cancelling a job stops its worker.
	long, err := s.handle.Delegate("In the repository demo, have a claude worker write the numbers 1 to 2000 to COUNT.txt, one per line, one shell command per number, reporting progress every 200 numbers.", "")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(jobLimit)
	running := ""
	for running == "" {
		time.Sleep(2 * time.Second)
		if v, ok := s.view(long); ok {
			for _, w := range v.Workers {
				if w.Status == agent.WorkerRunning {
					running = w.Name
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no worker started for the long job")
		}
	}
	if _, err := s.handle.Cancel(string(long)); err != nil {
		return err
	}
	if !s.gone(running, 30*time.Second) {
		return fmt.Errorf("jobs cancel left %s running", running)
	}
	ok(t, fmt.Sprintf("jobs cancel stopped worker %s (Escape, then close)", running))

	if codex {
		if err := s.codexPart(); err != nil {
			return err
		}
	}

	// Quit: every pane closes, every worker MCP config is deleted.
	s.handle.Shutdown()
	list, _ := s.host.List(ctx)
	if len(list) != 0 {
		var names []string
		for _, a := range list {
			names = append(names, a.Name)
		}
		return fmt.Errorf("panes left after shutdown: %v", names)
	}
	ok(t, "shutdown closed every agent pane")
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// watchTrust records the first worker pane that shows Codex's trust screen, until ctx ends.
func (s *workerSmoke) watchTrust(ctx context.Context, seen *string, mu *sync.Mutex) {
	for ctx.Err() == nil {
		if list, err := s.host.List(ctx); err == nil {
			for _, a := range list {
				if !strings.HasPrefix(a.Name, "demo-") && !strings.HasPrefix(a.Name, "untrusted-") {
					continue
				}
				if screen, err := s.host.Read(ctx, a.Name, 40); err == nil && agent.ShowsCodexTrust(screen) {
					mu.Lock()
					if *seen == "" {
						*seen = a.Name
					}
					mu.Unlock()
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// codexPart: a Codex start in a repo Codex does not trust fails with the trust message and
// types nothing; then a worker in the trusted repo works end to end.
func (s *workerSmoke) codexPart() error {
	t := s.t
	saved := func(path string) bool {
		b, _ := os.ReadFile(s.codexConf)
		return strings.Contains(string(b), path)
	}
	if saved(s.untrusted) {
		return fmt.Errorf("the untrusted repo is already in the Codex config")
	}
	// 1. The untrusted repo: the start fails on the trust screen.
	var mu sync.Mutex
	seen := ""
	wctx, stop := context.WithCancel(context.Background())
	go s.watchTrust(wctx, &seen, &mu)
	s.mu.Lock()
	before := len(s.starts)
	s.mu.Unlock()
	id, _, err := s.job("In the repository untrusted, have a codex worker (kind codex) create a file CODEX.txt containing the single word hi. If start_worker fails, do not retry and do not try another kind: end the job as failed and tell me the reason word for word.")
	stop()
	if err != nil {
		return err
	}
	s.mu.Lock()
	replies := append([]startReply(nil), s.starts[before:]...)
	s.mu.Unlock()
	mu.Lock()
	info(t, "codex (untrusted repo): the trust screen showed", map[bool]string{true: "yes (" + seen + ")", false: "not seen"}[seen != ""])
	mu.Unlock()
	info(t, "codex (untrusted repo): start_worker replies", fmt.Sprintf("%+v", replies))
	refused, anyOK := false, false
	for _, r := range replies {
		refused = refused || strings.HasPrefix(r.err, "Codex asks to trust this repo")
		anyOK = anyOK || r.err == ""
	}
	if !refused || anyOK {
		s.dump()
		return fmt.Errorf("%s: a codex start in the untrusted repo did not fail on trust: %+v", id, replies)
	}
	if saved(s.untrusted) {
		return fmt.Errorf("the untrusted repo's trust was saved: a key reached the trust screen")
	}
	ok(t, "codex start in an untrusted repo failed with the trust message; nothing typed (no trust saved)")

	// 2. The trusted repo (the smoke-only -c projects={…} override trusts it): a worker end to end.
	seen = ""
	wctx, stop = context.WithCancel(context.Background())
	go s.watchTrust(wctx, &seen, &mu)
	id, view, err := s.job("In the repository demo, have a codex worker (kind codex) create a file CODEX.txt containing the single word hi and commit it on its branch. Wait for its report, then tell me what it did.")
	stop()
	if err != nil {
		return err
	}
	mu.Lock()
	info(t, "codex (trusted repo): the trust screen showed", map[bool]string{true: "yes (" + seen + "): the -c projects={…} override did not apply", false: "no"}[seen != ""])
	mu.Unlock()
	worker := ""
	for _, w := range view.Workers {
		if w.Kind == agent.Codex {
			worker = w.Name
		}
	}
	if worker == "" {
		s.dump()
		return fmt.Errorf("%s closed without a codex worker (status %v)", id, view.Status)
	}
	reported := false
	for _, tool := range []string{"report_result", "report_progress", "ask_coordinator", "report_blocked"} {
		reported = reported || s.saw(worker+" "+tool)
	}
	info(t, "codex: -c mcp_servers.pixie.* reached the app with its bearer", map[bool]string{true: "yes", false: "no (it reported in plain text; the app read its screen)"}[reported])
	slug := strings.TrimPrefix(worker, "demo-")
	committed := s.committed(slug, "CODEX.txt")
	info(t, "codex: the workspace-write sandbox let it commit in the worktree", fmt.Sprint(committed == nil, " ", committed))
	if view.Status != agent.JobDone {
		s.dump()
		return fmt.Errorf("%s ended %v (last progress: %v)", id, view.Status, view.LastProgress)
	}
	if committed != nil {
		return committed
	}
	ok(t, fmt.Sprintf("codex worker %s: CODEX.txt committed on pixie/%s", worker, slug))
	if err := s.keptThenClosed(worker, "CODEX.txt commit"); err != nil {
		return err
	}
	return nil
}
