# pix Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `pix`, a terminal wrapper bundled in DeskPixie.app, so Bit can list, message, interrupt and (with your OK) close Claude Code / Codex sessions you start yourself in any terminal, next to the workers Pixie spawns.

**Architecture:** `pix <tool>` runs the agent in its own PTY, passes bytes straight through, adds per-run hooks and a stdio MCP server (`pix mcp`), and keeps one control connection to the daemon's Unix socket. In the daemon, a new wrapper host (`core/pixhost`) implements `agent.SessionHost` for registered sessions, and a `CompositeHost` combines it with the existing Herdr/tmux host so the controller still sees one host. Bit gets four new tools (`list_sessions`, `message_session`, `interrupt_session`, `close_session`) handled in the runtime, like `start_worker`.

**Tech Stack:** Go 1.26.2 (`core/`), `github.com/creack/pty`, `golang.org/x/term`; Swift (Settings row, `mac/`), XcodeGen `project.yml`.

**Spec:** `docs/superpowers/specs/2026-10-15-pix-wrapper-design.md` (issue #89). Read it first; this plan argues from it.

## Global Constraints

- Go module `github.com/sihekuang/deskpixie/core`, `go 1.26.2`. New deps allowed: `github.com/creack/pty`, `golang.org/x/term` only.
- Binary name `pix`; bundled at `Contents/Helpers/pix`; host arch only; Developer ID + hardened runtime + timestamp.
- Settings install is opt-in: symlink `~/.local/bin/pix` → `<app>/Contents/Helpers/pix`. Never edit `~/.claude.json`, `~/.claude/settings.json`, `~/.codex/config.toml`.
- Daemon socket `<app_data>/agent/pix.sock`, mode 0600; fallback `$TMPDIR/deskpixie-<uid>/pix.sock` when the path is > 100 bytes. Wire: newline-delimited JSON, `"v":1`.
- Address rules: `agent.IsWorkerName`, same taken pool as workers, `<RepoSlug(git root)>-<tool>` then `-2`, `-3`…
- Description: ≤ 60 characters, one line; precedence user > agent > auto; agent updates ≤ 1 per minute; keep last 5.
- MCP tool name is `set_description` (not set_title).
- Queue ≤ 20 messages per session; typing gate 1.5 s idle; hangup grace 2 s; reconnect backoff 1 s → 30 s; unreachable drop after 10 s.
- You-started sessions: prompt + interrupt allowed; close only through `close_session`, which Claude asks you about (`permissions.ask`); quitting DeskPixie never ends them; `pix` ends with its terminal.
- Pixie's voice never speaks addresses, branches or folders (as `delegation.md`).
- Commits end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01VCzfVR412AotXiviB8Jbsw
  ```

## Review Focus

1. **A prompt arrives while you are mid-sentence in the agent's input** → it must wait, never merge with your text (Task 6 test `TestGateHoldsWhileTyping`, `TestGateUnsureAfterEditingKey`).
2. **DeskPixie restarts while a `pix` session runs** → same address after reconnect, Bit's commands work again (Task 8 test `TestReRegisterKeepsAddress`; Task 15 smoke).
3. **Closing the terminal tab / quitting WebStorm** → agent ends within ~2 s, no orphan process (Task 4 test `TestHangupEndsChild`).
4. **Two `pix` sessions resume the same Claude conversation** → second registers refused, still runs as plain claude (Task 8 test `TestDuplicateSessionRefused`; Task 7 test `TestRefusedRunsStandalone`).
5. **A prompt containing escape sequences or `\x1b[201~`** → stripped so it can't break out of bracketed paste (Task 6 test `TestSanitizeStripsPasteEnd`).

---

## File Structure

```
core/pix/wire/wire.go           Wire messages pix ⇄ daemon (shared). Pure.
core/pix/wire/wire_test.go
core/pix/where.go               Terminal/IDE detection, nested-pix detection. Pure.
core/pix/launch.go              Per-run argv + temp settings/MCP files for claude/codex.
core/pix/pty.go                 PTY pass-through runner, resize, hangup grace, exit code.
core/pix/local.go               $PIX_SOCK server (hooks, mcp → parent pix) + client helpers.
core/pix/hookcmd.go             `pix hook <event>`.
core/pix/mcp.go                 `pix mcp` stdio MCP server (set_description).
core/pix/gate.go                Typing gate (key tracking) + sanitize + bracketed paste.
core/pix/queue.go               Message queue (≤ 20).
core/pix/claudesock.go          Claude inbox-socket delivery (session record, procStart check).
core/pix/client.go              Control connection: register, reconnect, commands.
core/pix/session.go             Ties runner + gate + queue + client + local together.
core/cmd/pix/main.go            Subcommand dispatch.
core/pixhost/names.go           Address allocation + description store. Pure.
core/pixhost/host.go            Daemon listener + agent.SessionHost for pix sessions.
core/agent/composite.go         CompositeHost (primary + wrapper).
core/agent/session_tools.go     list/message/interrupt/close_session (runtime-handled).
core/agent/types.go             AgentInfo fields, McpCallKind values (modify).
core/agent/worker_tools.go      Catalog + parse for the 4 tools (modify).
core/agent/runtime.go           Route the 4 tools in AgentHandle.mcp (modify).
core/app/agentapp/agentapp.go   Build wrapper host + CompositeHost (modify).
core/app/agentapp/assets/coordinator-settings.json   ask: close_session (modify).
core/app/agentapp/assets/repo-work-workers.md         Session tools paragraph (modify).
core/Makefile                   build-pix target (modify).
mac/project.yml                 Copy + sign pix into Contents/Helpers (modify).
mac/Makefile, scripts/package-mac.sh                  Check pix (modify).
mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Platform/PixInstaller.swift  (create)
mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/PixInstallerTests.swift   (create)
mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Settings/SettingsLayout.swift (modify)
mac/DeskPixie/Settings/SettingsWindowView.swift       pix row (modify)
docs/pix.md, README.md                                 (create / modify)
core/pix/smoketest/smoke.go                            Live smoke (create)
```

---

### Task 1: Wire protocol

**Files:**
- Create: `core/pix/wire/wire.go`, `core/pix/wire/wire_test.go`

**Interfaces:**
- Produces:
  ```go
  package wire
  const Version = 1
  type Kind string // "register","registered","refused","event","command","result","bye"
  type Msg struct {
      V    int    `json:"v"`
      Kind Kind   `json:"kind"`
      ID   string `json:"id,omitempty"`      // command/result correlation
      // register
      Tool, Cwd, GitRoot, Where, SessionID, Description string `json:",omitempty"` // see field tags below
      Pid       int    `json:"pid,omitempty"`
      ProcStart string `json:"procStart,omitempty"`
      // registered / refused
      Address string `json:"address,omitempty"`
      Reason  string `json:"reason,omitempty"`
      // event: "status" | "description" | "exited" | "session"
      Event    string `json:"event,omitempty"`
      Status   string `json:"status,omitempty"`   // idle|working|blocked
      Source   string `json:"source,omitempty"`   // auto|agent|user (description)
      ExitCode int    `json:"exitCode,omitempty"`
      // command: "prompt" | "keys" | "close"
      Command string   `json:"command,omitempty"`
      Text    string   `json:"text,omitempty"`
      Keys    []string `json:"keys,omitempty"`
      // result
      Err string `json:"err,omitempty"`
  }
  func Encode(w io.Writer, m Msg) error   // sets V, writes one JSON line
  type Decoder struct{...}
  func NewDecoder(r io.Reader) *Decoder
  func (d *Decoder) Next() (Msg, error)   // rejects lines > 1 MiB
  func SocketPath(appData string, uid int, tmp string) string
  ```

- [ ] **Step 1: Write the failing tests**

```go
package wire

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Msg{Kind: "register", Tool: "claude", Pid: 42, Cwd: "/r", Description: "x"}
	if err := Encode(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("one line expected: %q", buf.String())
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatal(err)
	}
	if got.V != Version || got.Tool != "claude" || got.Pid != 42 || got.Description != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestDecoderRejectsHugeLine(t *testing.T) {
	big := strings.Repeat("a", 2<<20)
	_, err := NewDecoder(strings.NewReader(`{"v":1,"text":"` + big + `"}` + "\n")).Next()
	if err == nil {
		t.Fatal("want error for > 1 MiB line")
	}
}

func TestSocketPathFallsBackWhenLong(t *testing.T) {
	short := SocketPath("/Users/a/Library/Application Support/x", 501, "/tmp/")
	if short != "/Users/a/Library/Application Support/x/agent/pix.sock" {
		t.Fatalf("short: %s", short)
	}
	long := SocketPath("/"+strings.Repeat("d", 120), 501, "/var/folders/T/")
	if long != "/var/folders/T/deskpixie-501/pix.sock" {
		t.Fatalf("long: %s", long)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd core && go test ./pix/wire/`
Expected: FAIL (package has no Go files / undefined: Msg).

- [ ] **Step 3: Implement**

```go
// Package wire is the pix ⇄ daemon protocol: one JSON object per line over a Unix socket
// (spec "Daemon side"). Both ends ship in the same app, so Version only guards stale links.
package wire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

const Version = 1

// MaxLine bounds one message (a prompt is far smaller).
const MaxLine = 1 << 20

type Kind string

const (
	KindRegister   Kind = "register"
	KindRegistered Kind = "registered"
	KindRefused    Kind = "refused"
	KindEvent      Kind = "event"
	KindCommand    Kind = "command"
	KindResult     Kind = "result"
	KindBye        Kind = "bye"
)

type Msg struct {
	V           int      `json:"v"`
	Kind        Kind     `json:"kind"`
	ID          string   `json:"id,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Pid         int      `json:"pid,omitempty"`
	ProcStart   string   `json:"procStart,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	GitRoot     string   `json:"gitRoot,omitempty"`
	Where       string   `json:"where,omitempty"`
	SessionID   string   `json:"sessionId,omitempty"`
	Description string   `json:"description,omitempty"`
	Address     string   `json:"address,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Event       string   `json:"event,omitempty"`
	Status      string   `json:"status,omitempty"`
	Source      string   `json:"source,omitempty"`
	ExitCode    int      `json:"exitCode,omitempty"`
	Command     string   `json:"command,omitempty"`
	Text        string   `json:"text,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	Err         string   `json:"err,omitempty"`
}

// Encode writes m as one line, with V set.
func Encode(w io.Writer, m Msg) error {
	m.V = Version
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

type Decoder struct{ s *bufio.Scanner }

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), MaxLine)
	return &Decoder{s: s}
}

// Next is the next message; io.EOF at the end.
func (d *Decoder) Next() (Msg, error) {
	if !d.s.Scan() {
		if err := d.s.Err(); err != nil {
			return Msg{}, err
		}
		return Msg{}, io.EOF
	}
	var m Msg
	if err := json.Unmarshal(d.s.Bytes(), &m); err != nil {
		return Msg{}, fmt.Errorf("pix wire: %w", err)
	}
	return m, nil
}

// SocketPath is <appData>/agent/pix.sock, or <tmp>/deskpixie-<uid>/pix.sock when that is too
// long for a Unix socket (104 bytes on macOS, with margin).
func SocketPath(appData string, uid int, tmp string) string {
	p := filepath.Join(appData, "agent", "pix.sock")
	if len(p) <= 100 {
		return p
	}
	return filepath.Join(tmp, fmt.Sprintf("deskpixie-%d", uid), "pix.sock")
}
```

- [ ] **Step 4: Run** `cd core && go test ./pix/wire/` → PASS.
- [ ] **Step 5: Commit** `git add core/pix/wire && git commit -m "pix: wire protocol (#89)"` (+ trailer lines).

---

### Task 2: Addresses and descriptions (daemon, pure)

**Files:**
- Create: `core/pixhost/names.go`, `core/pixhost/names_test.go`

**Interfaces:**
- Consumes: `agent.RepoSlug`, `agent.IsWorkerName`, `agent.MaxName` (exist in `core/agent/recipes.go`).
- Produces:
  ```go
  func Address(gitRoot, tool string, taken []string) (string, bool)
  type Source int // SourceAuto, SourceAgent, SourceUser
  type Descriptions struct{...}
  func (d *Descriptions) Set(src Source, text string, now time.Time) bool // false = ignored
  func (d *Descriptions) Clear(src Source)
  func (d *Descriptions) Current() string
  func (d *Descriptions) Past() []string // newest first, ≤ 5, excludes Current
  func AutoDescription(prompt string) string
  func CleanDescription(s string) string // one line, ≤ 60 runes
  ```
  `AutoDescription` needs the first three content words; export `agent.TaskWords` by renaming `taskWords` → `TaskWords` in `core/agent/recipes.go` (update its two callers).

- [ ] **Step 1: Failing tests**

```go
package pixhost

import (
	"strings"
	"testing"
	"time"
)

func TestAddressFromRepoAndTool(t *testing.T) {
	a, ok := Address("/Users/me/Projects/DeskPixie", "claude", nil)
	if !ok || a != "deskpixie-claude" {
		t.Fatalf("%q %v", a, ok)
	}
	b, _ := Address("/Users/me/Projects/DeskPixie", "claude", []string{"deskpixie-claude"})
	if b != "deskpixie-claude-2" {
		t.Fatalf("%q", b)
	}
}

func TestAddressNoGitRoot(t *testing.T) {
	a, _ := Address("", "codex", nil)
	if a != "repo-codex" {
		t.Fatalf("%q", a)
	}
}

func TestAddressAvoidsWorkerNames(t *testing.T) {
	a, _ := Address("/x/focus", "claude", []string{"focus-claude", "focus-claude-2"})
	if a != "focus-claude-3" {
		t.Fatalf("%q", a)
	}
}

func TestDescriptionPrecedence(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	d.Set(SourceAuto, "fix login", t0)
	d.Set(SourceAgent, "fixing login redirect loop", t0)
	if d.Current() != "fixing login redirect loop" {
		t.Fatal(d.Current())
	}
	d.Set(SourceUser, "login bug", t0)
	if d.Set(SourceAgent, "something else", t0.Add(2*time.Minute)); d.Current() != "login bug" {
		t.Fatalf("user must win: %q", d.Current())
	}
	d.Clear(SourceUser)
	if d.Current() != "something else" {
		t.Fatal(d.Current())
	}
}

func TestAgentRateLimit(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	d.Set(SourceAgent, "one", t0)
	if d.Set(SourceAgent, "two", t0.Add(30*time.Second)) {
		t.Fatal("second agent update within a minute must be ignored")
	}
	if !d.Set(SourceAgent, "three", t0.Add(61*time.Second)) {
		t.Fatal("after a minute it is accepted")
	}
}

func TestPastKeepsFive(t *testing.T) {
	var d Descriptions
	t0 := time.Unix(0, 0)
	for i, s := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		d.Set(SourceAgent, s, t0.Add(time.Duration(i)*2*time.Minute))
	}
	if got := strings.Join(d.Past(), ","); got != "f,e,d,c,b" {
		t.Fatal(got)
	}
}

func TestCleanDescription(t *testing.T) {
	if got := CleanDescription("  two\nlines\t here "); got != "two lines here" {
		t.Fatalf("%q", got)
	}
	if got := CleanDescription(strings.Repeat("é", 80)); len([]rune(got)) != 60 {
		t.Fatalf("len %d", len([]rune(got)))
	}
}

func TestAutoDescription(t *testing.T) {
	if got := AutoDescription("Please fix the login redirect loop in this repo"); got != "fix login redirect" {
		t.Fatalf("%q", got)
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pixhost/` → FAIL (undefined).
- [ ] **Step 3: Implement**

```go
// Package pixhost is the daemon side of pix: sessions you start yourself (spec #89).
package pixhost

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/agent"
)

// MaxDescription is a description's length in runes.
const MaxDescription = 60

// AgentInterval is the least time between two accepted agent descriptions.
const AgentInterval = time.Minute

const keepPast = 5

// Address is `<repo>-<tool>`, then -2, -3…, not in taken (which holds worker names too).
func Address(gitRoot, tool string, taken []string) (string, bool) {
	repo := "repo"
	if gitRoot != "" {
		repo = agent.RepoSlug(filepath.Base(gitRoot))
	}
	for n := 1; n < 100; n++ {
		name := repo + "-" + tool
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		if agent.IsWorkerName(name) && !slices.Contains(taken, name) {
			return name, true
		}
	}
	return "", false
}

type Source int

const (
	SourceAuto Source = iota
	SourceAgent
	SourceUser
)

// Descriptions holds one session's description from each source; the highest source wins.
type Descriptions struct {
	by        [3]string
	lastAgent time.Time
	past      []string
}

func (d *Descriptions) Set(src Source, text string, now time.Time) bool {
	text = CleanDescription(text)
	if text == "" {
		return false
	}
	if src == SourceAgent && !d.lastAgent.IsZero() && now.Sub(d.lastAgent) < AgentInterval {
		return false
	}
	before := d.Current()
	d.by[src] = text
	if src == SourceAgent {
		d.lastAgent = now
	}
	if after := d.Current(); before != "" && after != before {
		d.past = append([]string{before}, slices.DeleteFunc(d.past, func(s string) bool { return s == before || s == after })...)
		if len(d.past) > keepPast {
			d.past = d.past[:keepPast]
		}
	}
	return true
}

func (d *Descriptions) Clear(src Source) { d.by[src] = "" }

func (d *Descriptions) Current() string {
	for i := len(d.by) - 1; i >= 0; i-- {
		if d.by[i] != "" {
			return d.by[i]
		}
	}
	return ""
}

func (d *Descriptions) Past() []string { return slices.Clone(d.past) }

// CleanDescription is s on one line, spaces collapsed, at most MaxDescription runes.
func CleanDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxDescription {
		s = strings.TrimSpace(string([]rune(s)[:MaxDescription]))
	}
	return s
}

// AutoDescription is the prompt's first three content words (as worker names use).
func AutoDescription(prompt string) string { return CleanDescription(agent.TaskWords(prompt)) }
```

  In `core/agent/recipes.go`, rename `taskWords` → `TaskWords` (its doc comment too) and update the callers (`grep -n taskWords core/agent/*.go`).

- [ ] **Step 4: Run** `cd core && go test ./pixhost/ ./agent/` → PASS.
- [ ] **Step 5: Commit** `pixhost: addresses and descriptions (#89)`.

---

### Task 3: Launch plan and where-detection (pix, pure)

**Files:**
- Create: `core/pix/where.go`, `core/pix/launch.go`, `core/pix/launch_test.go`

**Interfaces:**
- Produces:
  ```go
  func Where(env func(string) string) string   // "WebStorm"/"JetBrains", "VS Code", "iTerm", "Terminal", "terminal"
  func Nested(env func(string) string) bool     // PIX_SESSION or PIXIE_AGENT_TOKEN set
  type Launch struct { Path string; Args []string; Env []string; Files []string }
  func PlanLaunch(tool string, userArgs []string, self, sock, dir string) (Launch, error)
  const Instruction = "When your task changes, call set_description with a few words describing it."
  ```
  `self` is the absolute path to the running `pix` (`os.Executable()`), `sock` is `$PIX_SOCK`, `dir` a private temp dir (0700) where the per-run files go; `Files` lists them for cleanup.

- [ ] **Step 1: Failing tests**

```go
package pix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestWhere(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm", "__CFBundleIdentifier": "com.jetbrains.WebStorm"}, "WebStorm"},
		{map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm"}, "JetBrains"},
		{map[string]string{"TERM_PROGRAM": "vscode"}, "VS Code"},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iTerm"},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, "Terminal"},
		{map[string]string{}, "terminal"},
	}
	for _, c := range cases {
		if got := Where(envOf(c.env)); got != c.want {
			t.Errorf("%v: got %q want %q", c.env, got, c.want)
		}
	}
}

func TestNested(t *testing.T) {
	if !Nested(envOf(map[string]string{"PIX_SESSION": "1"})) || !Nested(envOf(map[string]string{"PIXIE_AGENT_TOKEN": "t"})) {
		t.Fatal("nested not detected")
	}
	if Nested(envOf(nil)) {
		t.Fatal("false positive")
	}
}

func TestPlanLaunchClaude(t *testing.T) {
	dir := t.TempDir()
	l, err := PlanLaunch("claude", []string{"--resume", "abc"}, "/app/pix", "/tmp/s.sock", dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(l.Path) != "claude" && l.Path != "claude" {
		t.Fatalf("path %q", l.Path)
	}
	joined := strings.Join(l.Args, " ")
	for _, want := range []string{"--settings", "--mcp-config", "--append-system-prompt", "--resume abc"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if !slices.Contains(l.Env, "PIX_SESSION=1") || !slices.Contains(l.Env, "PIX_SOCK=/tmp/s.sock") {
		t.Fatalf("env %v", l.Env)
	}
	var settings map[string]any
	b, _ := os.ReadFile(l.Files[0])
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, ev := range []string{"UserPromptSubmit", "PostToolUse", "Stop", "Notification"} {
		if hooks[ev] == nil {
			t.Errorf("no %s hook", ev)
		}
	}
	if !strings.Contains(string(b), "/app/pix hook") {
		t.Fatalf("hooks must call pix hook: %s", b)
	}
	if fi, _ := os.Stat(l.Files[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestPlanLaunchCodex(t *testing.T) {
	l, err := PlanLaunch("codex", nil, "/app/pix", "/tmp/s.sock", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Args, " ")
	if !strings.Contains(joined, `mcp_servers.pixie.command="/app/pix"`) {
		t.Fatalf("args %q", joined)
	}
}

func TestPlanLaunchUnknownTool(t *testing.T) {
	if _, err := PlanLaunch("vim", nil, "/p", "/s", t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pix/` → FAIL.
- [ ] **Step 3: Implement**

`core/pix/where.go`:
```go
// Package pix is the `pix` terminal wrapper (spec #89): it runs an agent in its own PTY so
// DeskPixie can reach a session you started yourself.
package pix

import "strings"

// Where names the terminal or IDE pix runs in, for the session's description in Bit's list.
func Where(env func(string) string) string {
	if env("TERMINAL_EMULATOR") == "JetBrains-JediTerm" {
		if id := env("__CFBundleIdentifier"); strings.HasPrefix(id, "com.jetbrains.") {
			return strings.TrimPrefix(id, "com.jetbrains.")
		}
		return "JetBrains"
	}
	switch env("TERM_PROGRAM") {
	case "vscode":
		return "VS Code"
	case "iTerm.app":
		return "iTerm"
	case "Apple_Terminal":
		return "Terminal"
	}
	return "terminal"
}

// Nested: already inside a pix session or a Pixie-started agent; pix then just execs the tool.
func Nested(env func(string) string) bool {
	return env("PIX_SESSION") != "" || env("PIXIE_AGENT_TOKEN") != ""
}
```

`core/pix/launch.go`:
```go
package pix

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Instruction is the one line pix adds to the agent's instructions.
const Instruction = "When your task changes, call set_description with a few words describing it."

// Launch is how pix starts the agent.
type Launch struct {
	Path  string
	Args  []string
	Env   []string // added to pix's own environment
	Files []string // per-run files to delete at exit
}

// hookEvents are the Claude hook events pix listens to.
var hookEvents = []string{"UserPromptSubmit", "PostToolUse", "Stop", "Notification"}

// PlanLaunch builds the per-run additions for tool; userArgs follow them unchanged.
func PlanLaunch(tool string, userArgs []string, self, sock, dir string) (Launch, error) {
	path, err := exec.LookPath(tool)
	if err != nil {
		path = tool // let the start fail with the shell's usual message
	}
	env := []string{"PIX_SESSION=1", "PIX_SOCK=" + sock}
	switch tool {
	case "claude":
		hooks := map[string]any{}
		for _, ev := range hookEvents {
			hooks[ev] = []any{map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": strconv.Quote(self) + " hook " + ev, "timeout": 5,
			}}}}
		}
		settings, err := writePrivate(dir, "settings.json", map[string]any{"hooks": hooks})
		if err != nil {
			return Launch{}, err
		}
		mcp, err := writePrivate(dir, "mcp.json", map[string]any{"mcpServers": map[string]any{
			"pixie": map[string]any{"command": self, "args": []string{"mcp"}},
		}})
		if err != nil {
			return Launch{}, err
		}
		args := []string{"--settings", settings, "--mcp-config", mcp, "--append-system-prompt", Instruction}
		return Launch{Path: path, Args: append(args, userArgs...), Env: env, Files: []string{settings, mcp}}, nil
	case "codex":
		args := []string{
			"-c", fmt.Sprintf("mcp_servers.pixie.command=%s", strconv.Quote(self)),
			"-c", `mcp_servers.pixie.args=["mcp"]`,
			"-c", "developer_instructions=" + strconv.Quote(Instruction),
		}
		return Launch{Path: path, Args: append(args, userArgs...), Env: env}, nil
	}
	return Launch{}, fmt.Errorf("pix: unknown tool %q (use claude or codex)", tool)
}

func writePrivate(dir, name string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, b, 0o600)
}
```
  Note: the Codex hook and instruction keys (`developer_instructions`, hooks) are verified in Task 15; if Codex rejects a key, drop that `-c` pair and record it in the spec's "To verify".

- [ ] **Step 4: Run** `cd core && go test ./pix/` → PASS.
- [ ] **Step 5: Commit** `pix: launch plan and terminal detection (#89)`.

---

### Task 4: PTY pass-through runner

**Files:**
- Modify: `core/go.mod`, `core/go.sum` (`go get github.com/creack/pty@v1.1.24 golang.org/x/term@v0.36.0`; use the newest tagged versions at least two weeks old if these differ)
- Create: `core/pix/pty.go`, `core/pix/pty_test.go`

**Interfaces:**
- Produces:
  ```go
  type Runner struct {
      Launch Launch
      Stdin  *os.File; Stdout *os.File   // the user's terminal
      OnOutput func([]byte)                // optional tap (Codex busy marker), called after write
      OnInput  func([]byte)                // tap for the typing gate, called before forwarding
  }
  func (r *Runner) Start() error
  func (r *Runner) Pid() int
  func (r *Runner) WriteToAgent(b []byte) error    // inject (paste, Escape)
  func (r *Runner) Hangup()                        // SIGHUP child, SIGKILL after HangupGrace
  func (r *Runner) Wait() int                      // exit code (128+sig when signalled)
  const HangupGrace = 2 * time.Second
  ```
  Raw mode and `SIGWINCH` forwarding only when `Stdin` is a terminal (`term.IsTerminal`), so tests can use pipes.

- [ ] **Step 1: Failing tests**

```go
package pix

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pipes(t *testing.T) (inR, inW, outR, outW *os.File) {
	inR, inW, _ = os.Pipe()
	outR, outW, _ = os.Pipe()
	t.Cleanup(func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() })
	return
}

func TestPassThroughAndExitCode(t *testing.T) {
	inR, inW, outR, outW := pipes(t)
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo got:$x; exit 3"}}, Stdin: inR, Stdout: outW}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	inW.WriteString("hello\n")
	code := r.Wait()
	outW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(outR)
	if code != 3 || !strings.Contains(buf.String(), "got:hello") {
		t.Fatalf("code %d out %q", code, buf.String())
	}
}

func TestInjectReachesChild(t *testing.T) {
	inR, _, outR, outW := pipes(t)
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo inj:$x"}}, Stdin: inR, Stdout: outW}
	r.Start()
	r.WriteToAgent([]byte("pixie\n"))
	r.Wait()
	outW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(outR)
	if !strings.Contains(buf.String(), "inj:pixie") {
		t.Fatalf("%q", buf.String())
	}
}

func TestHangupEndsChild(t *testing.T) {
	inR, _, _, outW := pipes(t)
	// Ignores SIGHUP, so the grace kill must end it.
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "trap '' HUP; sleep 30"}}, Stdin: inR, Stdout: outW}
	r.Start()
	start := time.Now()
	r.Hangup()
	r.Wait()
	if d := time.Since(start); d > HangupGrace+time.Second {
		t.Fatalf("took %v", d)
	}
	if err := syscall.Kill(r.Pid(), 0); err == nil {
		t.Fatal("child still alive")
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pix/ -run 'PassThrough|Inject|Hangup'` → FAIL.
- [ ] **Step 3: Implement**

```go
package pix

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// HangupGrace is how long the agent gets after a hangup before it is killed.
const HangupGrace = 2 * time.Second

// Runner runs the agent in a PTY and copies bytes both ways, untouched.
type Runner struct {
	Launch   Launch
	Stdin    *os.File
	Stdout   *os.File
	OnOutput func([]byte)
	OnInput  func([]byte)

	cmd     *exec.Cmd
	ptmx    *os.File
	mu      sync.Mutex // serializes writes to the agent (user keys and injections)
	done    chan struct{}
	code    int
	restore func()
}

func (r *Runner) Start() error {
	r.cmd = exec.Command(r.Launch.Path, r.Launch.Args...)
	r.cmd.Env = append(os.Environ(), r.Launch.Env...)
	var size *pty.Winsize
	if term.IsTerminal(int(r.Stdin.Fd())) {
		size, _ = pty.GetsizeFull(r.Stdin)
	}
	ptmx, err := pty.StartWithSize(r.cmd, size)
	if err != nil {
		return err
	}
	r.ptmx = ptmx
	r.done = make(chan struct{})
	if term.IsTerminal(int(r.Stdin.Fd())) {
		if old, err := term.MakeRaw(int(r.Stdin.Fd())); err == nil {
			r.restore = func() { term.Restore(int(r.Stdin.Fd()), old) }
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for {
				select {
				case <-winch:
					pty.InheritSize(r.Stdin, r.ptmx)
				case <-r.done:
					signal.Stop(winch)
					return
				}
			}
		}()
	}
	go r.copyIn()
	outDone := make(chan struct{})
	go func() { r.copyOut(); close(outDone) }()
	go func() {
		err := r.cmd.Wait()
		<-outDone // the child's last output reaches the terminal first
		r.code = exitCode(err)
		if r.restore != nil {
			r.restore()
		}
		close(r.done)
	}()
	return nil
}

func (r *Runner) copyIn() {
	buf := make([]byte, 4096)
	for {
		n, err := r.Stdin.Read(buf)
		if n > 0 {
			if r.OnInput != nil {
				r.OnInput(buf[:n])
			}
			r.WriteToAgent(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (r *Runner) copyOut() {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.ptmx.Read(buf)
		if n > 0 {
			r.Stdout.Write(buf[:n])
			if r.OnOutput != nil {
				r.OnOutput(buf[:n])
			}
		}
		if err != nil { // EIO when the child side closes
			return
		}
	}
}

func (r *Runner) WriteToAgent(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.ptmx.Write(b)
	return err
}

func (r *Runner) Pid() int { return r.cmd.Process.Pid }

// Hangup sends SIGHUP to the agent's process group, then SIGKILL after HangupGrace.
func (r *Runner) Hangup() {
	pgid := r.cmd.Process.Pid // pty.Start makes the child a session (and group) leader
	syscall.Kill(-pgid, syscall.SIGHUP)
	select {
	case <-r.done:
	case <-time.After(HangupGrace):
		syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func (r *Runner) Wait() int {
	<-r.done
	r.ptmx.Close()
	return r.code
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

var _ = io.EOF
```
  (Remove the `var _ = io.EOF` line if `io` ends up unused; it is only a placeholder guard for the import.)

- [ ] **Step 4: Run** `cd core && go test ./pix/ -race -run 'PassThrough|Inject|Hangup'` → PASS.
- [ ] **Step 5: Commit** `pix: PTY pass-through runner (#89)` with go.mod/go.sum.

---

### Task 5: Local socket, `pix hook`, `pix mcp`

**Files:**
- Create: `core/pix/local.go`, `core/pix/hookcmd.go`, `core/pix/mcp.go`, `core/pix/local_test.go`, `core/pix/mcp_test.go`

**Interfaces:**
- Consumes: `wire.Msg`, `wire.Encode`, `wire.NewDecoder`.
- Produces:
  ```go
  // Local is the parent pix's socket for its own hooks and pix mcp.
  type LocalHandler interface {
      Hook(event string, payload map[string]any) (reply string) // reply = hook stdout ("" none)
      SetDescription(text string) error
  }
  func ServeLocal(path string, h LocalHandler) (close func(), err error)
  func RunHook(event string, sock string, stdin io.Reader, stdout io.Writer) int // always exit 0 (never block the agent)
  func RunMCP(sock string, in io.Reader, out io.Writer) error
  ```
  Local messages reuse `wire.Msg`: hook → `{kind:"event", event:"hook", status:<event>, text:<raw stdin JSON>}`; reply `{kind:"result", text:<stdout>}`. MCP → `{kind:"event", event:"description", source:"agent", description:…}`; reply `{kind:"result", err:…}`.

- [ ] **Step 1: Failing tests**

```go
package pix

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type fakeLocal struct {
	events []string
	desc   string
	reply  string
}

func (f *fakeLocal) Hook(ev string, p map[string]any) string { f.events = append(f.events, ev); return f.reply }
func (f *fakeLocal) SetDescription(s string) error        { f.desc = s; return nil }

func TestHookForwardsAndReplies(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "l.sock")
	f := &fakeLocal{reply: `{"hookSpecificOutput":{"additionalContext":"hi"}}`}
	stop, err := ServeLocal(sock, f)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var out bytes.Buffer
	code := RunHook("PostToolUse", sock, strings.NewReader(`{"session_id":"s1"}`), &out)
	if code != 0 || len(f.events) != 1 || f.events[0] != "PostToolUse" || !strings.Contains(out.String(), "additionalContext") {
		t.Fatalf("code %d events %v out %q", code, f.events, out.String())
	}
}

func TestHookWithoutParentIsSilent(t *testing.T) {
	var out bytes.Buffer
	if code := RunHook("Stop", "/nonexistent.sock", strings.NewReader("{}"), &out); code != 0 || out.Len() != 0 {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestMCPSetDescription(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "l.sock")
	f := &fakeLocal{}
	stop, _ := ServeLocal(sock, f)
	defer stop()
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"set_description","arguments":{"text":"fixing login loop"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := RunMCP(sock, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 replies (no reply to a notification): %q", out.String())
	}
	var list map[string]any
	json.Unmarshal([]byte(lines[1]), &list)
	if !strings.Contains(lines[1], `"set_description"`) {
		t.Fatalf("tools/list: %s", lines[1])
	}
	if f.desc != "fixing login loop" {
		t.Fatalf("desc %q", f.desc)
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pix/ -run 'Hook|MCP'` → FAIL.
- [ ] **Step 3: Implement**

`core/pix/local.go`:
```go
package pix

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// LocalHandler is the parent pix, as its own hooks and pix mcp see it.
type LocalHandler interface {
	Hook(event string, payload map[string]any) string
	SetDescription(text string) error
}

// ServeLocal listens on path (0600) for one request per connection.
func ServeLocal(path string, h LocalHandler) (func(), error) {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveLocalConn(c, h)
		}
	}()
	return func() { l.Close(); os.Remove(path) }, nil
}

func serveLocalConn(c net.Conn, h LocalHandler) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	m, err := wire.NewDecoder(c).Next()
	if err != nil {
		return
	}
	reply := wire.Msg{Kind: wire.KindResult}
	switch m.Event {
	case "hook":
		var payload map[string]any
		json.Unmarshal([]byte(m.Text), &payload)
		reply.Text = h.Hook(m.Status, payload)
	case "description":
		if err := h.SetDescription(m.Description); err != nil {
			reply.Err = err.Error()
		}
	default:
		reply.Err = "unknown request"
	}
	wire.Encode(c, reply)
}

// askLocal sends one request to the parent pix and returns its reply.
func askLocal(sock string, m wire.Msg, timeout time.Duration) (wire.Msg, error) {
	if sock == "" {
		return wire.Msg{}, errors.New("not in a pix session")
	}
	c, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return wire.Msg{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	if err := wire.Encode(c, m); err != nil {
		return wire.Msg{}, err
	}
	return wire.NewDecoder(c).Next()
}
```

`core/pix/hookcmd.go`:
```go
package pix

import (
	"io"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// RunHook is `pix hook <event>`: forward the hook JSON to the parent pix and print its reply.
// Always exits 0: a missing parent must never block or fail the agent.
func RunHook(event, sock string, stdin io.Reader, stdout io.Writer) int {
	raw, _ := io.ReadAll(io.LimitReader(stdin, wire.MaxLine))
	reply, err := askLocal(sock, wire.Msg{Kind: wire.KindEvent, Event: "hook", Status: event, Text: string(raw)}, 4*time.Second)
	if err == nil && reply.Text != "" {
		io.WriteString(stdout, reply.Text)
	}
	return 0
}
```

`core/pix/mcp.go`:
```go
package pix

import (
	"bufio"
	"encoding/json"
	"io"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

var setDescriptionTool = map[string]any{
	"name":        "set_description",
	"description": "Say in a few words what you are working on, so the user and Pixie can tell this session apart. Call it when your task changes.",
	"inputSchema": map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string", "description": "A few words, e.g. \"fixing login redirect loop\"."}},
		"required":   []string{"text"},
	},
}

// RunMCP is `pix mcp`: a stdio MCP server (newline-delimited JSON-RPC) with set_description.
func RunMCP(sock string, in io.Reader, out io.Writer) error {
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 64<<10), wire.MaxLine)
	enc := json.NewEncoder(out)
	for s.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(s.Bytes(), &req) != nil || req.ID == nil {
			continue // notifications and junk get no reply
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "pixie", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{setDescriptionTool}}
		case "tools/call":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			text, isErr := "ok", false
			if p.Name != "set_description" {
				text, isErr = "unknown tool", true
			} else if reply, err := askLocal(sock, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: p.Arguments["text"]}, 4*time.Second); err != nil {
				text, isErr = "pix is not reachable; the description was not saved", true
			} else if reply.Err != "" {
				text, isErr = reply.Err, true
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}
		default:
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	return s.Err()
}
```

- [ ] **Step 4: Run** `cd core && go test ./pix/ -race -run 'Hook|MCP'` → PASS.
- [ ] **Step 5: Commit** `pix: local socket, pix hook, pix mcp (#89)`.

---

### Task 6: Delivery — queue, typing gate, paste, Claude socket

**Files:**
- Create: `core/pix/queue.go`, `core/pix/gate.go`, `core/pix/claudesock.go`, `core/pix/gate_test.go`, `core/pix/claudesock_test.go`

**Interfaces:**
- Produces:
  ```go
  const MaxQueue = 20
  type Queue struct{...}; func (q *Queue) Push(s string) error; func (q *Queue) Drain() []string; func (q *Queue) Len() int
  var ErrQueueFull = errors.New("busy: 20 messages waiting")
  const TypingQuiet = 1500 * time.Millisecond
  type Gate struct{...}
  func (g *Gate) Keys(b []byte, now time.Time)          // every byte you type
  func (g *Gate) Clear(now time.Time) bool              // true = safe to type into the agent now
  func Sanitize(s string) string                         // strip control chars except \n, and any ESC
  func Paste(s string) []byte                            // "\x1b[200~" + s + "\x1b[201~" + "\r"
  type ClaudeRecord struct{ Pid int; SessionID string; ProcStart string; PeerProtocol int; SocketPath string; Name string }
  func FindClaudeRecord(home string, pid int) (ClaudeRecord, error)
  func ProcStart(pid int) (string, error)                // `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>`
  func PostClaude(rec ClaudeRecord, text string) error   // verifies procStart + peerProtocol, then posts
  ```
  Claude frame (from the claude-session-registry note / agent-deck `internal/send/claudesocket.go`): optional auth line `{"type":"auth","token":<contents of <pid>.<sha>.key>}` if a key file exists, then `{"msgV":1,"msg_id":<uuid>,"type":"user","session_id":<sessionId>,"message":{"role":"user","content":<text>},"priority":"next"}` + `\n`, wait 150 ms, close.

- [ ] **Step 1: Failing tests**

`core/pix/gate_test.go`:
```go
package pix

import (
	"strings"
	"testing"
	"time"
)

func TestGateClearWhenNothingTyped(t *testing.T) {
	var g Gate
	if !g.Clear(time.Unix(100, 0)) {
		t.Fatal("fresh gate should be clear")
	}
}

func TestGateHoldsWhileTyping(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("hel"), t0)
	if g.Clear(t0.Add(time.Second)) {
		t.Fatal("typed text pending: not clear")
	}
	if g.Clear(t0.Add(5 * time.Second)) {
		t.Fatal("still unsent text after quiet time: not clear")
	}
	g.Keys([]byte("\r"), t0.Add(6*time.Second))
	if g.Clear(t0.Add(6*time.Second + time.Second)) {
		t.Fatal("within quiet period after Enter: not clear")
	}
	if !g.Clear(t0.Add(6*time.Second + 2*time.Second)) {
		t.Fatal("after Enter and quiet: clear")
	}
}

func TestGateBackspaceToEmpty(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("ab\x7f\x7f"), t0)
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("typed and deleted: clear after quiet")
	}
}

func TestGateUnsureAfterEditingKey(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("ab\x1b[D"), t0) // arrow key: we can't track the line any more
	if g.Clear(t0.Add(10 * time.Second)) {
		t.Fatal("unsure: not clear")
	}
	g.Keys([]byte("\r"), t0.Add(11*time.Second))
	if !g.Clear(t0.Add(13 * time.Second)) {
		t.Fatal("Enter resets unsure")
	}
}

func TestGateCtrlCClears(t *testing.T) {
	var g Gate
	t0 := time.Unix(100, 0)
	g.Keys([]byte("abc\x03"), t0)
	if !g.Clear(t0.Add(2 * time.Second)) {
		t.Fatal("Ctrl-C discards the line")
	}
}

func TestSanitizeStripsPasteEnd(t *testing.T) {
	got := Sanitize("hi\x1b[201~rm -rf /\x07\nnext")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) || got != "hi[201~rm -rf /\nnext" {
		t.Fatalf("%q", got)
	}
}

func TestPaste(t *testing.T) {
	if got := string(Paste("a\nb")); got != "\x1b[200~a\nb\x1b[201~\r" {
		t.Fatalf("%q", got)
	}
}

func TestQueueLimit(t *testing.T) {
	var q Queue
	for i := 0; i < MaxQueue; i++ {
		if err := q.Push("m"); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Push("one too many"); err != ErrQueueFull {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if len(q.Drain()) != MaxQueue || q.Len() != 0 {
		t.Fatal("drain")
	}
}
```

`core/pix/claudesock_test.go`:
```go
package pix

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestFindClaudeRecordAndPost(t *testing.T) {
	home := t.TempDir()
	sess := filepath.Join(home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	sock := filepath.Join(t.TempDir(), "c.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	pid := os.Getpid()
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{"pid": pid, "sessionId": "s-1", "procStart": start, "peerProtocol": 1, "messagingSocketPath": sock}
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(sess, strconv.Itoa(pid)+".json"), b, 0o600)

	got := make(chan map[string]any, 1)
	go func() {
		c, _ := l.Accept()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		var m map[string]any
		json.Unmarshal(line, &m)
		got <- m
	}()
	r, err := FindClaudeRecord(home, pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := PostClaude(r, "hello"); err != nil {
		t.Fatal(err)
	}
	m := <-got
	if m["type"] != "user" || m["session_id"] != "s-1" || m["priority"] != "next" {
		t.Fatalf("%v", m)
	}
}

func TestPostClaudeRefusesWrongProcess(t *testing.T) {
	r := ClaudeRecord{Pid: os.Getpid(), ProcStart: "Thu Jan  1 00:00:00 1970", PeerProtocol: 1, SocketPath: "/nonexistent"}
	if err := PostClaude(r, "x"); err == nil {
		t.Fatal("procStart mismatch must refuse")
	}
	r2 := ClaudeRecord{Pid: os.Getpid(), PeerProtocol: 2}
	r2.ProcStart, _ = ProcStart(os.Getpid())
	if err := PostClaude(r2, "x"); err == nil {
		t.Fatal("unknown peerProtocol must refuse")
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pix/ -run 'Gate|Sanitize|Paste|Queue|Claude'` → FAIL.
- [ ] **Step 3: Implement**

`core/pix/queue.go`:
```go
package pix

import (
	"errors"
	"sync"
)

const MaxQueue = 20

var ErrQueueFull = errors.New("busy: 20 messages waiting")

// Queue holds messages for the agent until they can be delivered.
type Queue struct {
	mu   sync.Mutex
	msgs []string
}

func (q *Queue) Push(s string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) >= MaxQueue {
		return ErrQueueFull
	}
	q.msgs = append(q.msgs, s)
	return nil
}

func (q *Queue) Drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}
```

`core/pix/gate.go`:
```go
package pix

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// TypingQuiet is how long you must not have typed before pix types for Pixie.
const TypingQuiet = 1500 * time.Millisecond

// Gate tracks what you type, to know when typing into the agent can't mix with your text.
type Gate struct {
	mu      sync.Mutex
	pending int  // printable runes typed since the last Enter (backspace subtracts)
	unsure  bool // an editing key we can't follow: wait for Enter or Ctrl-C
	last    time.Time
}

func (g *Gate) Keys(b []byte, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = now
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '\r' || c == '\n' || c == 0x03: // Enter, Ctrl-C
			g.pending, g.unsure = 0, false
		case c == 0x7f || c == 0x08: // backspace
			if g.pending > 0 {
				g.pending--
			}
		case c == 0x1b: // escape sequence (arrows, etc.): line is no longer trackable
			g.unsure = true
		case c < 0x20:
			g.unsure = true
		case c&0xC0 == 0x80: // UTF-8 continuation byte
		default:
			g.pending++
		}
	}
}

// Clear: nothing of yours is pending and you have been quiet for TypingQuiet.
func (g *Gate) Clear(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending > 0 || g.unsure {
		return false
	}
	return g.last.IsZero() || now.Sub(g.last) >= TypingQuiet
}

// Sanitize removes control characters (ESC included) except newlines, so a message can't
// leave the bracketed paste or press keys.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Paste is s as a bracketed paste, then Enter.
func Paste(s string) []byte { return []byte("\x1b[200~" + s + "\x1b[201~\r") }
```

`core/pix/claudesock.go`:
```go
package pix

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeRecord is ~/.claude/sessions/<pid>.json (undocumented; see the
// claude-session-registry note). Re-check the fields on Claude updates.
type ClaudeRecord struct {
	Pid          int    `json:"pid"`
	SessionID    string `json:"sessionId"`
	ProcStart    string `json:"procStart"`
	PeerProtocol int    `json:"peerProtocol"`
	SocketPath   string `json:"messagingSocketPath"`
	Name         string `json:"name"`
	keyFile      string
}

func FindClaudeRecord(home string, pid int) (ClaudeRecord, error) {
	dir := filepath.Join(home, ".claude", "sessions")
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return ClaudeRecord{}, err
	}
	var r ClaudeRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return ClaudeRecord{}, err
	}
	if keys, _ := filepath.Glob(filepath.Join(dir, strconv.Itoa(pid)+".*.key")); len(keys) == 1 {
		r.keyFile = keys[0]
	}
	return r, nil
}

func ProcStart(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PostClaude posts text into the session's inbox (queued for its next turn, or delivered
// at once when idle). It refuses unless the record is this exact process and protocol 1.
func PostClaude(r ClaudeRecord, text string) error {
	if r.PeerProtocol != 1 {
		return fmt.Errorf("claude peerProtocol %d not supported", r.PeerProtocol)
	}
	start, err := ProcStart(r.Pid)
	if err != nil || start != strings.TrimSpace(r.ProcStart) {
		return fmt.Errorf("claude session record is not process %d", r.Pid)
	}
	c, err := net.DialTimeout("unix", r.SocketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if r.keyFile != "" {
		if tok, err := os.ReadFile(r.keyFile); err == nil {
			line, _ := json.Marshal(map[string]any{"type": "auth", "token": strings.TrimSpace(string(tok))})
			c.Write(append(line, '\n'))
		}
	}
	line, _ := json.Marshal(map[string]any{
		"msgV": 1, "msg_id": uuid(), "type": "user", "session_id": r.SessionID,
		"message": map[string]any{"role": "user", "content": text}, "priority": "next",
	})
	if _, err := c.Write(append(line, '\n')); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond) // the server never replies; give it time to read
	return nil
}

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
```

- [ ] **Step 4: Run** `cd core && go test ./pix/ -race -run 'Gate|Sanitize|Paste|Queue|Claude'` → PASS.
- [ ] **Step 5: Commit** `pix: delivery queue, typing gate, Claude inbox socket (#89)`.

---

### Task 7: Session (pix side) — control client and `pix` command

**Files:**
- Create: `core/pix/client.go`, `core/pix/session.go`, `core/cmd/pix/main.go`, `core/pix/session_test.go`

**Interfaces:**
- Consumes: Tasks 1, 3–6.
- Produces:
  ```go
  type Client struct{ Sock string; Hello wire.Msg; OnCommand func(wire.Msg) wire.Msg; OnRegistered func(addr string); OnRefused func(reason string) }
  func (c *Client) Run(ctx context.Context)            // connect, register, read commands; reconnect 1 s → 30 s
  func (c *Client) Send(m wire.Msg)                    // event; dropped while disconnected (state is resent on register)
  type Session struct {...}
  func Run(args []string, env func(string) string, home string) int // `pix [--description d] <tool> args…` → exit code
  ```
  Session behaviour:
  - `Hook("UserPromptSubmit")` → status working; first prompt of new work (after a Stop) → `event description source=auto text=<prompt>`; daemon makes the auto description.
  - `Hook("Stop")` → status idle; then flush the queue (typing gate permitting).
  - `Hook("Notification")` → status blocked.
  - `Hook("PostToolUse")` → if the queue has messages, reply `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Messages from Pixie:\n- …"}}` and drain.
  - Command `prompt`: Claude → `PostClaude` (record found once the child's session file appears; re-read each time); on error or Codex → queue, deliver when idle + `Gate.Clear` via `WriteToAgent(Paste(Sanitize(text)))`. Queue full → result `err`.
  - Command `keys`: `Escape` → `\x1b`, `Enter` → `\r`, other names → result err `unknown key`.
  - Command `close`: `Runner.Hangup()`.
  - Tab title: OSC 2 `\x1b]2;<description>\x07` written to Stdout when the description changes; with queued messages, prefix `📨 <n> · `.
  - Exit: send `event exited exitCode=<code>`, then `bye`; delete `Launch.Files` and the local socket; return the code.
  - Nested (`Nested(env)`): `syscall.Exec` the tool with the user args, no registration.
  - Refused registration: keep running the agent with no daemon (standalone), title `pix: <reason>`.

- [ ] **Step 1: Failing tests** (use a fake daemon on a temp socket and `/bin/sh` as the "agent")

```go
package pix

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// fakeDaemon accepts one pix, answers register with addr (or refuses), records events.
type fakeDaemon struct {
	l      net.Listener
	conn   chan net.Conn
	events chan wire.Msg
}

func newFakeDaemon(t *testing.T, refuse string) (*fakeDaemon, string) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{l: l, conn: make(chan net.Conn, 4), events: make(chan wire.Msg, 64)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				dec := wire.NewDecoder(c)
				for {
					m, err := dec.Next()
					if err != nil {
						return
					}
					if m.Kind == wire.KindRegister {
						if refuse != "" {
							wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: refuse})
							continue
						}
						wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: "repo-claude"})
						d.conn <- c
					}
					d.events <- m
				}
			}()
		}
	}()
	t.Cleanup(func() { l.Close() })
	return d, sock
}

func waitEvent(t *testing.T, ch chan wire.Msg, pred func(wire.Msg) bool) wire.Msg {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-ch:
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestClientRegistersAndReconnects(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 2)
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister, Tool: "claude", SessionID: "s1"},
		OnRegistered: func(a string) { got <- a }, OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
	go c.Run(ctx)
	if a := <-got; a != "repo-claude" {
		t.Fatal(a)
	}
	(<-d.conn).Close() // daemon drops us
	select {
	case a := <-got:
		if a != "repo-claude" {
			t.Fatal(a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no re-register after drop")
	}
}

func TestRefusedRunsStandalone(t *testing.T) {
	_, sock := newFakeDaemon(t, "session already connected")
	refused := make(chan string, 1)
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister}, OnRefused: func(r string) { refused <- r },
		OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	if r := <-refused; r != "session already connected" {
		t.Fatal(r)
	}
}

func TestSessionPromptViaPasteForCodexLikeAgent(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	inR, _, outR, outW := pipes(t)
	s := newSessionForTest(t, sessionTestOpts{
		tool: "codex", daemonSock: sock, stdin: inR, stdout: outW,
		agent: Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}},
	})
	c := <-d.conn
	s.markIdle() // as a Stop hook would
	wire.Encode(c, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "hello pix"})
	waitEvent(t, d.events, func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == "1" && m.Err == "" })
	code := s.wait()
	outW.Close()
	buf := make([]byte, 4096)
	n, _ := outR.Read(buf)
	if code != 0 || !containsBytes(buf[:n], "seen:") {
		t.Fatalf("code %d out %q", code, buf[:n])
	}
	waitEvent(t, d.events, func(m wire.Msg) bool { return m.Event == "exited" })
}
```
  `newSessionForTest`, `sessionTestOpts`, `markIdle`, `wait`, `containsBytes` are test helpers in `session_test.go` built on the unexported constructor `newSession(opts)` that `Run` also uses — define `newSession` so tests can inject the agent `Launch` and pipes.

- [ ] **Step 2: Run** `cd core && go test ./pix/ -run 'Client|Refused|Session'` → FAIL.
- [ ] **Step 3: Implement**

`core/pix/client.go`:
```go
package pix

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// Backoff bounds between reconnect attempts.
const (
	BackoffMin = time.Second
	BackoffMax = 30 * time.Second
)

// Client keeps pix registered with the daemon, reconnecting forever.
type Client struct {
	Sock         string
	Hello        wire.Msg
	OnCommand    func(wire.Msg) wire.Msg
	OnRegistered func(address string)
	OnRefused    func(reason string)
	Resend       func() []wire.Msg // state to send after each registration

	mu   sync.Mutex
	conn net.Conn
}

func (c *Client) Run(ctx context.Context) {
	wait := BackoffMin
	for ctx.Err() == nil {
		if c.session(ctx) {
			wait = BackoffMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > BackoffMax {
			wait = BackoffMax
		}
	}
}

// session runs one connection; true if it registered. A refusal stops reconnecting.
func (c *Client) session(ctx context.Context) bool {
	conn, err := net.DialTimeout("unix", c.Sock, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	if wire.Encode(conn, c.Hello) != nil {
		return false
	}
	dec := wire.NewDecoder(conn)
	reply, err := dec.Next()
	if err != nil {
		return false
	}
	if reply.Kind == wire.KindRefused {
		if c.OnRefused != nil {
			c.OnRefused(reply.Reason)
		}
		<-ctx.Done() // standalone from now on
		return false
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	if c.OnRegistered != nil {
		c.OnRegistered(reply.Address)
	}
	if c.Resend != nil {
		for _, m := range c.Resend() {
			c.Send(m)
		}
	}
	for {
		m, err := dec.Next()
		if err != nil {
			break
		}
		if m.Kind == wire.KindCommand {
			go func(m wire.Msg) {
				r := c.OnCommand(m)
				r.Kind, r.ID = wire.KindResult, m.ID
				c.Send(r)
			}(m)
		}
	}
	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()
	return true
}

// Send writes m if connected; otherwise it is dropped (Resend restores state).
func (c *Client) Send(m wire.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		wire.Encode(c.conn, m)
	}
}
```

`core/pix/session.go` — the glue (state: status, description by user flag, queue, gate, runner, client, local socket). Key parts:
```go
package pix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

type sessionOpts struct {
	tool, description, daemonSock, home string
	stdin, stdout                        *os.File
	agent                                Launch
	where                                string
}

type Session struct {
	o      sessionOpts
	runner *Runner
	client *Client
	queue  Queue
	gate   Gate

	mu        sync.Mutex
	status    string // idle|working|blocked
	desc      string
	afterStop bool // the next prompt starts new work (auto description)
	sessionID string
}

func newSession(o sessionOpts) *Session {
	s := &Session{o: o, status: "idle", afterStop: true, desc: o.description}
	s.runner = &Runner{Launch: o.agent, Stdin: o.stdin, Stdout: o.stdout,
		OnInput: func(b []byte) { s.gate.Keys(b, time.Now()) }}
	return s
}

func (s *Session) Hook(event string, p map[string]any) string {
	if id, _ := p["session_id"].(string); id != "" {
		s.setSessionID(id)
	}
	switch event {
	case "UserPromptSubmit":
		s.setStatus("working")
		s.mu.Lock()
		first := s.afterStop
		s.afterStop = false
		s.mu.Unlock()
		if prompt, _ := p["prompt"].(string); first && prompt != "" {
			s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: prompt})
		}
	case "Stop":
		s.mu.Lock()
		s.afterStop = true
		s.mu.Unlock()
		s.setStatus("idle")
		go s.flush()
	case "Notification":
		s.setStatus("blocked")
	case "PostToolUse":
		if msgs := s.queue.Drain(); len(msgs) > 0 {
			s.title()
			return fmt.Sprintf(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":%q}}`,
				"Messages from Pixie:\n- "+strings.Join(msgs, "\n- "))
		}
	}
	return ""
}

func (s *Session) SetDescription(text string) error {
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: text})
	return nil
}

func (s *Session) command(m wire.Msg) wire.Msg {
	switch m.Command {
	case "prompt":
		text := Sanitize(m.Text)
		if s.o.tool == "claude" {
			if rec, err := FindClaudeRecord(s.o.home, s.runner.Pid()); err == nil {
				if err := PostClaude(rec, text); err == nil {
					return wire.Msg{}
				}
			}
		}
		if err := s.queue.Push(text); err != nil {
			return wire.Msg{Err: err.Error()}
		}
		s.title()
		go s.flush()
		return wire.Msg{}
	case "keys":
		for _, k := range m.Keys {
			switch k {
			case "Escape":
				s.runner.WriteToAgent([]byte{0x1b})
			case "Enter":
				s.runner.WriteToAgent([]byte{'\r'})
			default:
				return wire.Msg{Err: "unknown key " + k}
			}
		}
		return wire.Msg{}
	case "close":
		go s.runner.Hangup()
		return wire.Msg{}
	}
	return wire.Msg{Err: "unknown command " + m.Command}
}

// flush types queued messages once the agent is idle and you aren't typing; retries every 500 ms
// while messages wait.
func (s *Session) flush() {
	for s.queue.Len() > 0 {
		if s.currentStatus() == "idle" && s.gate.Clear(time.Now()) {
			for _, m := range s.queue.Drain() {
				s.runner.WriteToAgent(Paste(m))
			}
			s.title()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Session) setStatus(st string) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "status", Status: st})
}

func (s *Session) currentStatus() string { s.mu.Lock(); defer s.mu.Unlock(); return s.status }

func (s *Session) setSessionID(id string) {
	s.mu.Lock()
	changed := s.sessionID != id
	s.sessionID = id
	s.mu.Unlock()
	if changed {
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: id})
	}
}

// title writes the tab title (OSC 2): the description, with waiting messages first.
func (s *Session) title() {
	s.mu.Lock()
	t := s.desc
	s.mu.Unlock()
	if n := s.queue.Len(); n > 0 {
		t = fmt.Sprintf("📨 %d · %s", n, t)
	}
	fmt.Fprintf(s.o.stdout, "\x1b]2;%s\x07", Sanitize(t))
}

func (s *Session) start(ctx context.Context, localSock string) error {
	s.client = &Client{
		Sock: s.o.daemonSock,
		Hello: wire.Msg{Kind: wire.KindRegister, Tool: s.o.tool, Cwd: mustGetwd(), GitRoot: gitRoot(mustGetwd()),
			Where: s.o.where, Description: s.o.description},
		OnCommand:    s.command,
		OnRegistered: func(string) {},
		OnRefused: func(r string) {
			s.mu.Lock()
			s.desc = "pix: " + r
			s.mu.Unlock()
			s.title()
		},
		Resend: func() []wire.Msg {
			s.mu.Lock()
			defer s.mu.Unlock()
			out := []wire.Msg{{Kind: wire.KindEvent, Event: "status", Status: s.status}}
			if s.sessionID != "" {
				out = append(out, wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: s.sessionID})
			}
			return out
		},
	}
	if err := s.runner.Start(); err != nil {
		return err
	}
	s.client.Hello.Pid = s.runner.Pid()
	s.client.Hello.ProcStart, _ = ProcStart(s.runner.Pid())
	go s.client.Run(ctx)
	return nil
}

// Run is `pix [--description d] <tool> [args…]`.
func Run(args []string, env func(string) string, home string) int {
	desc := ""
	for len(args) >= 2 && args[0] == "--description" {
		desc, args = args[1], args[2:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pix [--description \"…\"] claude|codex [args…]")
		return 2
	}
	tool, rest := args[0], args[1:]
	if Nested(env) {
		path, err := exec.LookPath(tool)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		syscall.Exec(path, append([]string{tool}, rest...), os.Environ())
		return 126
	}
	dir, err := os.MkdirTemp("", "pix-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	self, _ := os.Executable()
	localSock := filepath.Join(dir, "pix.sock")
	launch, err := PlanLaunch(tool, rest, self, localSock, dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	s := newSession(sessionOpts{tool: tool, description: desc, home: home, stdin: os.Stdin, stdout: os.Stdout,
		agent: launch, where: Where(env), daemonSock: DaemonSocket(home, env)})
	stop, err := ServeLocal(localSock, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
	go func() { <-hup; s.runner.Hangup() }()
	if err := s.start(ctx, localSock); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	code := s.runner.Wait()
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "exited", ExitCode: code})
	s.client.Send(wire.Msg{Kind: wire.KindBye})
	return code
}
```
  Add `os/exec`, `os/signal` imports; add helpers in `session.go`:
  ```go
  func mustGetwd() string { d, _ := os.Getwd(); return d }

  // gitRoot is the nearest parent of dir holding .git, or "".
  func gitRoot(dir string) string {
  	for d := dir; ; d = filepath.Dir(d) {
  		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
  			return d
  		}
  		if filepath.Dir(d) == d {
  			return ""
  		}
  	}
  }

  // DaemonSocket is where DeskPixie listens (wire.SocketPath under Application Support).
  func DaemonSocket(home string, env func(string) string) string {
  	appData := filepath.Join(home, "Library", "Application Support", "com.sihekuang.deskpixie")
  	return wire.SocketPath(appData, os.Getuid(), os.TempDir())
  }
  ```

`core/cmd/pix/main.go`:
```go
// Command pix runs Claude Code or Codex so DeskPixie can reach the session (spec #89).
package main

import (
	"fmt"
	"os"

	"github.com/sihekuang/deskpixie/core/pix"
)

var version = "dev" // set with -ldflags "-X main.version=…"

func main() {
	home, _ := os.UserHomeDir()
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "hook":
			if len(args) < 2 {
				os.Exit(0)
			}
			os.Exit(pix.RunHook(args[1], os.Getenv("PIX_SOCK"), os.Stdin, os.Stdout))
		case "mcp":
			if err := pix.RunMCP(os.Getenv("PIX_SOCK"), os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "version", "--version":
			fmt.Println("pix", version)
			os.Exit(0)
		}
	}
	os.Exit(pix.Run(args, os.Getenv, home))
}
```

- [ ] **Step 4: Run** `cd core && go test ./pix/... -race && go build -o /dev/null ./cmd/pix` → PASS.
- [ ] **Step 5: Commit** `pix: session, control client, pix command (#89)`.

---

### Task 8: Wrapper host (daemon)

**Files:**
- Create: `core/pixhost/host.go`, `core/pixhost/host_test.go`

**Interfaces:**
- Consumes: `wire`, `Address`, `Descriptions`, `agent.SessionHost`, `agent.HostError`, `agent.ErrNoAgent`, `agent.PaneEvent`.
- Produces:
  ```go
  const Name = "pix"
  const UnreachableFor = 10 * time.Second
  type Host struct{...}
  func New(sock string, taken func() []string) *Host   // taken: worker names in use (from the primary host's List)
  func (h *Host) Listen() error                        // creates dir 0700, socket 0600
  func (h *Host) Shutdown()                            // closes listener and connections; pix sessions keep running
  func (h *Host) Owns(name string) bool
  func (h *Host) SetDescription(name, text string) error // user source; "" clears it
  // agent.SessionHost:
  Name, EnsureSession, StartAgent, ReplaceAgent, Prompt, SendKeys, Read, Close, CloseExact, List, Subscribe, AttachCommand
  ```
  `List` returns `agent.AgentInfo` with the new fields from Task 9 (`Origin: agent.OriginUser`, `Description`, `PastDescriptions`, `Where`, `Repo`, `Tool`). Order Task 9's `types.go` change before this task's implementation if executing in order — Task 9 Step 3a adds the fields; this task's tests use them.
  Commands to a session: send `{kind:"command", id, command…}` and wait for the matching result up to `agent.OpLimit`-equivalent 10 s; no connection → `ErrNoAgent`; result `err` → `HostError{Kind: HostOther, Detail: err}`.
  `StartAgent`/`ReplaceAgent`/`Read` → `HostError{Kind: HostOther, Detail: "not supported for sessions you started"}`.

- [ ] **Step 1: Failing tests**

```go
package pixhost

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pix/wire"
)

type fakePix struct {
	c   net.Conn
	dec *wire.Decoder
}

func dialPix(t *testing.T, sock string, hello wire.Msg) (*fakePix, wire.Msg) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hello.Kind = wire.KindRegister
	wire.Encode(c, hello)
	p := &fakePix{c: c, dec: wire.NewDecoder(c)}
	r, err := p.dec.Next()
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func newHost(t *testing.T, taken ...string) (*Host, string) {
	sock := filepath.Join(t.TempDir(), "pix.sock")
	h := New(sock, func() []string { return taken })
	if err := h.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Shutdown)
	return h, sock
}

func TestRegisterListsSession(t *testing.T) {
	h, sock := newHost(t, "focus-claude")
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus", Where: "WebStorm", Description: "login bug"})
	if r.Kind != wire.KindRegistered || r.Address != "focus-claude-2" {
		t.Fatalf("%+v", r)
	}
	list, _ := h.List(context.Background())
	if len(list) != 1 || list[0].Name != "focus-claude-2" || list[0].Origin != agent.OriginUser ||
		list[0].Description != "login bug" || list[0].Where != "WebStorm" {
		t.Fatalf("%+v", list)
	}
}

func TestPromptRoundTrip(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	go func() {
		m, _ := p.dec.Next()
		wire.Encode(p.c, wire.Msg{Kind: wire.KindResult, ID: m.ID})
	}()
	if err := h.Prompt(context.Background(), r.Address, "hi"); err != nil {
		t.Fatal(err)
	}
}

func TestPromptErrorFromPix(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	go func() {
		m, _ := p.dec.Next()
		wire.Encode(p.c, wire.Msg{Kind: wire.KindResult, ID: m.ID, Err: "busy: 20 messages waiting"})
	}()
	err := h.Prompt(context.Background(), r.Address, "hi")
	if err == nil || err.Error() != "busy: 20 messages waiting" {
		t.Fatalf("%v", err)
	}
}

func TestMissingAgentIsNoAgent(t *testing.T) {
	h, _ := newHost(t)
	if err := h.Prompt(context.Background(), "nope", "x"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatal(err)
	}
}

func TestReRegisterKeepsAddress(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	p.c.Close()
	time.Sleep(50 * time.Millisecond)
	_, r2 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	if r2.Address != r.Address {
		t.Fatalf("%s != %s", r2.Address, r.Address)
	}
	list, _ := h.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("one session expected: %+v", list)
	}
}

func TestDuplicateSessionRefused(t *testing.T) {
	_, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	if r.Kind != wire.KindRefused {
		t.Fatalf("%+v", r)
	}
}

func TestStatusAndExitEvents(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "status", Status: "working"})
	ev := <-events
	if ev.Agent != r.Address || ev.Status != agent.PaneWorking {
		t.Fatalf("%+v", ev)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "exited"})
	ev = <-events
	if ev.Kind != agent.PaneEventExited {
		t.Fatalf("%+v", ev)
	}
	list, _ := h.List(context.Background())
	if len(list) != 0 {
		t.Fatal("exited session still listed")
	}
}

func TestDescriptionSources(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "Please fix the flaky upload test"})
	time.Sleep(50 * time.Millisecond)
	list, _ := h.List(context.Background())
	if list[0].Description != "fix flaky upload" {
		t.Fatalf("auto: %q", list[0].Description)
	}
	h.SetDescription(r.Address, "uploads")
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: "agent text"})
	time.Sleep(50 * time.Millisecond)
	list, _ = h.List(context.Background())
	if list[0].Description != "uploads" {
		t.Fatalf("user must win: %q", list[0].Description)
	}
}

func TestDropAfterUnreachable(t *testing.T) {
	h, sock := newHost(t)
	h.unreachableFor = 100 * time.Millisecond // test hook
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	p.c.Close()
	time.Sleep(300 * time.Millisecond)
	list, _ := h.List(context.Background())
	if len(list) != 0 {
		t.Fatal("unreachable session should be dropped")
	}
}

func TestVersionRefused(t *testing.T) {
	_, sock := newHost(t)
	c, _ := net.Dial("unix", sock)
	c.Write([]byte(`{"v":99,"kind":"register"}` + "\n"))
	r, _ := wire.NewDecoder(c).Next()
	if r.Kind != wire.KindRefused {
		t.Fatalf("%+v", r)
	}
}

func TestStartAgentUnsupported(t *testing.T) {
	h, _ := newHost(t)
	if _, err := h.StartAgent(context.Background(), agent.AgentSpec{Name: "x"}); err == nil {
		t.Fatal("want unsupported")
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./pixhost/` → FAIL.
- [ ] **Step 3: Implement** `core/pixhost/host.go`:

```go
package pixhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pix/wire"
)

const Name = "pix"

// UnreachableFor is how long a session whose connection dropped (without exiting) stays listed.
const UnreachableFor = 10 * time.Second

// CommandLimit bounds one command round trip to a pix.
const CommandLimit = 10 * time.Second

var errUnsupported = agent.HostError{Kind: agent.HostOther, Detail: "not supported for sessions you started"}

type session struct {
	addr      string
	hello     wire.Msg
	sessionID string
	status    agent.PaneStatus
	desc      Descriptions
	conn      net.Conn // nil while unreachable
	wmu       sync.Mutex
	pending   map[string]chan wire.Msg
	dropTimer *time.Timer
}

type Host struct {
	sock           string
	taken          func() []string
	unreachableFor time.Duration

	mu       sync.Mutex
	l        net.Listener
	sessions map[string]*session // by address
	subs     []chan agent.PaneEvent
	seq      atomic.Uint64
}

func New(sock string, taken func() []string) *Host {
	return &Host{sock: sock, taken: taken, unreachableFor: UnreachableFor, sessions: map[string]*session{}}
}

func (h *Host) Listen() error {
	if err := os.MkdirAll(filepath.Dir(h.sock), 0o700); err != nil {
		return err
	}
	os.Remove(h.sock)
	l, err := net.Listen("unix", h.sock)
	if err != nil {
		return err
	}
	os.Chmod(h.sock, 0o600)
	h.mu.Lock()
	h.l = l
	h.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return nil
}

// Shutdown stops listening; pix sessions keep running and reconnect when the app is back.
func (h *Host) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.l != nil {
		h.l.Close()
	}
	for _, s := range h.sessions {
		if s.conn != nil {
			s.conn.Close()
		}
	}
	os.Remove(h.sock)
}

func (h *Host) serve(c net.Conn) {
	defer c.Close()
	dec := wire.NewDecoder(c)
	hello, err := dec.Next()
	if err != nil {
		return
	}
	if hello.V != wire.Version || hello.Kind != wire.KindRegister {
		wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: fmt.Sprintf("pix protocol %d; DeskPixie speaks %d — reinstall the pix command from Settings", hello.V, wire.Version)})
		return
	}
	s, reason := h.register(hello, c)
	if s == nil {
		wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: reason})
		return
	}
	s.wmu.Lock()
	wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: s.addr})
	s.wmu.Unlock()
	for {
		m, err := dec.Next()
		if err != nil {
			break
		}
		if h.onMsg(s, m) {
			return // exited / bye
		}
	}
	h.lost(s, c)
}

func (h *Host) register(hello wire.Msg, c net.Conn) (*session, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hello.SessionID != "" {
		for _, s := range h.sessions {
			if s.sessionID == hello.SessionID {
				if s.conn != nil {
					return nil, "this conversation is already connected to DeskPixie in another pix"
				}
				if s.dropTimer != nil {
					s.dropTimer.Stop()
				}
				s.conn, s.hello = c, hello
				return s, ""
			}
		}
	}
	taken := append([]string(nil), h.taken()...)
	for a := range h.sessions {
		taken = append(taken, a)
	}
	addr, ok := Address(hello.GitRoot, hello.Tool, taken)
	if !ok {
		return nil, "no free name"
	}
	s := &session{addr: addr, hello: hello, sessionID: hello.SessionID, conn: c, pending: map[string]chan wire.Msg{}}
	if hello.Description != "" {
		s.desc.Set(SourceUser, hello.Description, time.Now())
	}
	h.sessions[addr] = s
	return s, ""
}

// onMsg handles one message from pix; true when the session ended.
func (h *Host) onMsg(s *session, m wire.Msg) bool {
	switch m.Kind {
	case wire.KindResult:
		h.mu.Lock()
		ch := s.pending[m.ID]
		delete(s.pending, m.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case wire.KindBye:
		h.remove(s, true)
		return true
	case wire.KindEvent:
		switch m.Event {
		case "status":
			st := agent.ParsePaneStatus(m.Status)
			h.mu.Lock()
			s.status = st
			h.mu.Unlock()
			h.broadcast(agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: s.addr, Status: st})
		case "description":
			h.mu.Lock()
			switch m.Source {
			case "auto":
				s.desc.Set(SourceAuto, AutoDescription(m.Description), time.Now())
			case "agent":
				s.desc.Set(SourceAgent, m.Description, time.Now())
			}
			h.mu.Unlock()
		case "session":
			h.mu.Lock()
			s.sessionID = m.SessionID
			h.mu.Unlock()
		case "exited":
			h.remove(s, true)
			return true
		}
	}
	return false
}

func (h *Host) lost(s *session, c net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.conn != c {
		return // already replaced by a re-registration
	}
	s.conn = nil
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.dropTimer = time.AfterFunc(h.unreachableFor, func() { h.remove(s, true) })
}

func (h *Host) remove(s *session, exited bool) {
	h.mu.Lock()
	if h.sessions[s.addr] != s {
		h.mu.Unlock()
		return
	}
	delete(h.sessions, s.addr)
	h.mu.Unlock()
	if exited {
		h.broadcast(agent.PaneEvent{Kind: agent.PaneEventExited, Agent: s.addr})
	}
}

func (h *Host) broadcast(ev agent.PaneEvent) {
	h.mu.Lock()
	subs := append([]chan agent.PaneEvent(nil), h.subs...)
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // a slow subscriber misses an event rather than blocking pix
		}
	}
}

func (h *Host) command(ctx context.Context, name string, m wire.Msg) error {
	h.mu.Lock()
	s := h.sessions[name]
	if s == nil || s.conn == nil {
		h.mu.Unlock()
		return agent.ErrNoAgent(name)
	}
	m.Kind, m.ID = wire.KindCommand, strconv.FormatUint(h.seq.Add(1), 10)
	ch := make(chan wire.Msg, 1)
	s.pending[m.ID] = ch
	conn := s.conn
	h.mu.Unlock()
	s.wmu.Lock()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	err := wire.Encode(conn, m)
	s.wmu.Unlock()
	if err != nil {
		return agent.ErrNoAgent(name)
	}
	ctx, cancel := context.WithTimeout(ctx, CommandLimit)
	defer cancel()
	select {
	case r, ok := <-ch:
		if !ok {
			return agent.ErrNoAgent(name)
		}
		if r.Err != "" {
			return agent.HostError{Kind: agent.HostOther, Detail: r.Err}
		}
		return nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(s.pending, m.ID)
		h.mu.Unlock()
		return agent.HostError{Kind: agent.HostTimeout, Detail: "pix " + name}
	}
}

func (h *Host) Owns(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[name] != nil
}

// SetDescription sets your description for a session ("" clears it).
func (h *Host) SetDescription(name, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[name]
	if s == nil {
		return agent.ErrNoAgent(name)
	}
	if text == "" {
		s.desc.Clear(SourceUser)
	} else {
		s.desc.Set(SourceUser, text, time.Now())
	}
	return nil
}

func (h *Host) Name() string                       { return Name }
func (h *Host) EnsureSession(context.Context) error { return nil }
func (h *Host) StartAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, errUnsupported
}
func (h *Host) ReplaceAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, errUnsupported
}
func (h *Host) Prompt(ctx context.Context, name, text string) error {
	return h.command(ctx, name, wire.Msg{Command: "prompt", Text: text})
}
func (h *Host) SendKeys(ctx context.Context, name string, keys []string) error {
	return h.command(ctx, name, wire.Msg{Command: "keys", Keys: keys})
}
func (h *Host) Read(context.Context, string, uint16) (string, error) { return "", errUnsupported }
func (h *Host) Close(ctx context.Context, name string) error {
	return h.command(ctx, name, wire.Msg{Command: "close"})
}
func (h *Host) CloseExact(ctx context.Context, name, _ string) error {
	if err := h.Close(ctx, name); err != nil && !agent.IsHostError(err, agent.HostNoAgent) {
		return err
	}
	return nil
}
func (h *Host) List(context.Context) ([]agent.AgentInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]agent.AgentInfo, 0, len(h.sessions))
	for _, s := range h.sessions {
		repo := ""
		if s.hello.GitRoot != "" {
			repo = filepath.Base(s.hello.GitRoot)
		}
		out = append(out, agent.AgentInfo{
			Name: s.addr, Pane: "pix:" + s.addr, Cwd: s.hello.Cwd, Status: s.status,
			Origin: agent.OriginUser, Tool: s.hello.Tool, Repo: repo, Where: s.hello.Where,
			Description: s.desc.Current(), PastDescriptions: s.desc.Past(), Reachable: s.conn != nil,
		})
	}
	return out, nil
}
func (h *Host) Subscribe() <-chan agent.PaneEvent {
	ch := make(chan agent.PaneEvent, 64)
	h.mu.Lock()
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	return ch
}
func (h *Host) AttachCommand() []string { return nil }

var _ agent.SessionHost = (*Host)(nil)
```

- [ ] **Step 4: Run** `cd core && go test ./pixhost/ -race` → PASS.
- [ ] **Step 5: Commit** `pixhost: wrapper session host (#89)`.

---

### Task 9: AgentInfo fields and CompositeHost

**Files:**
- Modify: `core/agent/types.go:318-323` (AgentInfo)
- Create: `core/agent/composite.go`, `core/agent/composite_test.go`

**Interfaces:**
- Produces:
  ```go
  type Origin int
  const ( OriginPixie Origin = iota; OriginUser )
  type AgentInfo struct {
      Name string; Pane string; Cwd string; Status PaneStatus
      // pix sessions (spec #89); zero for Herdr/tmux agents:
      Origin Origin; Tool, Repo, Where, Description string; PastDescriptions []string; Reachable bool
  }
  type SecondaryHost interface { SessionHost; Owns(name string) bool }
  func NewCompositeHost(primary SessionHost, secondary SecondaryHost) *CompositeHost
  func (c *CompositeHost) Secondary() SecondaryHost
  ```
  `CompositeHost` implements `SessionHost`, and `Focuser`, `Labeler`, `StatusReporter` by delegating to the primary when it implements them (secondary agents: `Focus`/`Relabel` → nil no-op; `ReportStatus` → primary only). `Subscribe` merges both channels into one. `Name()` returns the primary's name (the tray and settings key on it).

- [ ] **Step 1: Failing test**

```go
package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// userHost is a fake secondary that owns "you-1".
type userHost struct {
	*agenttest.FakeSessionHost
}

func (u userHost) Owns(n string) bool { return n == "you-1" }

func TestCompositeRoutesByOwner(t *testing.T) {
	ctx := context.Background()
	primary := agenttest.NewFakeSessionHost()
	secondary := userHost{agenttest.NewFakeSessionHost()}
	secondary.StartAgent(ctx, agent.AgentSpec{Name: "you-1"})
	c := agent.NewCompositeHost(primary, secondary)

	if _, err := c.StartAgent(ctx, agent.AgentSpec{Name: "focus-fix"}); err != nil {
		t.Fatal(err)
	}
	if len(primary.Starts()) != 1 || len(secondary.Starts()) != 1 {
		t.Fatal("StartAgent must go to the primary")
	}
	c.Prompt(ctx, "you-1", "hi")
	c.Prompt(ctx, "focus-fix", "hi")
	if len(secondary.Prompts()) != 1 || len(primary.Prompts()) != 1 {
		t.Fatalf("routing: secondary %v primary %v", secondary.Prompts(), primary.Prompts())
	}
	list, _ := c.List(ctx)
	if len(list) != 2 {
		t.Fatalf("merged list: %+v", list)
	}
	if c.Name() != primary.Name() {
		t.Fatal("name is the primary's")
	}
}

func TestCompositeMergesEvents(t *testing.T) {
	ctx := context.Background()
	primary := agenttest.NewFakeSessionHost()
	secondary := userHost{agenttest.NewFakeSessionHost()}
	c := agent.NewCompositeHost(primary, secondary)
	events := c.Subscribe()
	secondary.StartAgent(ctx, agent.AgentSpec{Name: "you-1"})
	secondary.SetStatus("you-1", agent.PaneWorking)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Agent == "you-1" && ev.Status == agent.PaneWorking {
				return
			}
		case <-deadline:
			t.Fatal("no merged event")
		}
	}
}
```

- [ ] **Step 2: Run** `cd core && go test ./agent/ -run Composite` → FAIL.
- [ ] **Step 3a:** Edit `AgentInfo` in `core/agent/types.go`:

```go
// Origin says who started an agent: Pixie (Herdr/tmux) or you (pix, spec #89).
type Origin int

const (
	OriginPixie Origin = iota
	OriginUser
)

// AgentInfo is a running agent as the session host lists it. Cwd "" means unknown. The fields
// after Status are set for pix sessions only.
type AgentInfo struct {
	Name   string
	Pane   string
	Cwd    string
	Status PaneStatus

	Origin           Origin
	Tool             string
	Repo             string
	Where            string
	Description      string
	PastDescriptions []string
	Reachable        bool
}
```

- [ ] **Step 3b:** Create `core/agent/composite.go`:

```go
package agent

import "context"

// SecondaryHost is a session host for agents the primary didn't start (pix sessions, #89).
type SecondaryHost interface {
	SessionHost
	Owns(name string) bool
}

// CompositeHost is the primary host (Herdr or tmux) plus a secondary one, as one SessionHost:
// starts go to the primary, per-agent calls to whichever owns the agent, lists and events merge.
type CompositeHost struct {
	primary   SessionHost
	secondary SecondaryHost
}

func NewCompositeHost(primary SessionHost, secondary SecondaryHost) *CompositeHost {
	return &CompositeHost{primary: primary, secondary: secondary}
}

func (c *CompositeHost) Secondary() SecondaryHost { return c.secondary }

func (c *CompositeHost) owner(name string) SessionHost {
	if c.secondary.Owns(name) {
		return c.secondary
	}
	return c.primary
}

func (c *CompositeHost) Name() string                          { return c.primary.Name() }
func (c *CompositeHost) EnsureSession(ctx context.Context) error { return c.primary.EnsureSession(ctx) }
func (c *CompositeHost) StartAgent(ctx context.Context, s AgentSpec) (AgentInfo, error) {
	return c.primary.StartAgent(ctx, s)
}
func (c *CompositeHost) ReplaceAgent(ctx context.Context, s AgentSpec) (AgentInfo, error) {
	return c.primary.ReplaceAgent(ctx, s)
}
func (c *CompositeHost) Prompt(ctx context.Context, a, t string) error {
	return c.owner(a).Prompt(ctx, a, t)
}
func (c *CompositeHost) SendKeys(ctx context.Context, a string, k []string) error {
	return c.owner(a).SendKeys(ctx, a, k)
}
func (c *CompositeHost) Read(ctx context.Context, a string, n uint16) (string, error) {
	return c.owner(a).Read(ctx, a, n)
}
func (c *CompositeHost) Close(ctx context.Context, a string) error { return c.owner(a).Close(ctx, a) }
func (c *CompositeHost) CloseExact(ctx context.Context, a, p string) error {
	return c.owner(a).CloseExact(ctx, a, p)
}
func (c *CompositeHost) List(ctx context.Context) ([]AgentInfo, error) {
	out, err := c.primary.List(ctx)
	if err != nil {
		return nil, err
	}
	more, _ := c.secondary.List(ctx) // pix sessions never fail Pixie's own listing
	return append(out, more...), nil
}
func (c *CompositeHost) Subscribe() <-chan PaneEvent {
	out := make(chan PaneEvent, 64)
	a, b := c.primary.Subscribe(), c.secondary.Subscribe()
	go func() {
		for a != nil || b != nil {
			select {
			case ev, ok := <-a:
				if !ok {
					a = nil
					continue
				}
				out <- ev
			case ev, ok := <-b:
				if !ok {
					b = nil
					continue
				}
				out <- ev
			}
		}
		close(out)
	}()
	return out
}
func (c *CompositeHost) AttachCommand() []string { return c.primary.AttachCommand() }

func (c *CompositeHost) Focus(ctx context.Context, a string) error {
	if f, ok := c.primary.(Focuser); ok && !c.secondary.Owns(a) {
		return f.Focus(ctx, a)
	}
	return nil
}
func (c *CompositeHost) Relabel(ctx context.Context, a string, tab TabLabel) error {
	if l, ok := c.primary.(Labeler); ok && !c.secondary.Owns(a) {
		return l.Relabel(ctx, a, tab)
	}
	return nil
}
func (c *CompositeHost) ReportStatus(a string, s PaneStatus) {
	if r, ok := c.primary.(StatusReporter); ok {
		r.ReportStatus(a, s)
	}
}
```
  Caution: code that checks `host.(Focuser)` / `(StatusReporter)` to decide behaviour (e.g. tmux status hooks in `agentapp.go:195`, tray focus) now always sees the composite implementing them. Grep `\.(agent\.Focuser)\|\.(Focuser)\|(StatusReporter)\|(Labeler)` and, where the presence of the trait matters, check `c.primary` instead: add `func (c *CompositeHost) Primary() SessionHost`, and in those call sites unwrap with
  ```go
  func PrimaryOf(h SessionHost) SessionHost { if c, ok := h.(*CompositeHost); ok { return c.primary }; return h }
  ```
  Add a test `TestCompositeTraitsFollowPrimary`: a primary without `Focuser` → `PrimaryOf(c).(Focuser)` is false.

- [ ] **Step 4: Run** `cd core && go test ./agent/... -race` → PASS (existing tests unchanged: `AgentInfo` gained fields only).
- [ ] **Step 5: Commit** `agent: CompositeHost and pix fields on AgentInfo (#89)`.

---

### Task 10: Coordinator session tools

**Files:**
- Modify: `core/agent/types.go` (McpCallKind: add `CallListSessions`, `CallMessageSession`, `CallInterruptSession`, `CallCloseSession`, `CallDescribeSession`)
- Modify: `core/agent/worker_tools.go` (Catalog entries + parse)
- Create: `core/agent/session_tools.go`, `core/agent/session_tools_test.go`
- Modify: `core/agent/runtime.go:750-765` (route in `AgentHandle.mcp`)
- Modify: `core/app/agentapp/assets/coordinator-settings.json` (`ask` += `mcp__pixie__close_session`)
- Modify: `core/app/agentapp/assets/repo-work-workers.md` (+ `repo-work-solo.md`: session tools paragraph)

**Interfaces:**
- Consumes: `CompositeHost.Secondary()`, `AgentInfo` pix fields, `pixhost.Host.SetDescription` (via an interface `Describer { SetDescription(name, text string) error }` in `agent`).
- Produces (coordinator MCP tools):
  - `list_sessions` → `{"sessions":[{name, description, past_descriptions, tool, repo, where, status, reachable}]}` — only `Origin == OriginUser`.
  - `message_session {name, text}` → `{name, status:"sent"}` (Host.Prompt).
  - `interrupt_session {name}` → `{name, status:"interrupted"}` (SendKeys Escape).
  - `close_session {name}` → `{name, status:"closing"}` (Host.Close). In `permissions.ask`, so Claude asks you first.
  - `describe_session {name, text}` → `{name, description}`: your words for a session ("call the WebStorm one 'login bug'"); `text:""` clears it.
  All return `No session <name>.` for unknown names, and refuse names that are not `OriginUser` with `<name> is a worker; use the worker tools.`

- [ ] **Step 1: Failing tests** (`core/agent/session_tools_test.go`, internal package `agent`):

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

type userFake struct {
	*agenttest.FakeSessionHost
	described map[string]string
}

func (u *userFake) Owns(n string) bool { return strings.HasPrefix(n, "you-") }
func (u *userFake) List(ctx context.Context) ([]AgentInfo, error) {
	l, _ := u.FakeSessionHost.List(ctx)
	for i := range l {
		l[i].Origin, l[i].Description, l[i].Where = OriginUser, "login bug", "WebStorm"
	}
	return l, nil
}
func (u *userFake) SetDescription(n, t string) error { u.described[n] = t; return nil }

func newSessionHarness(t *testing.T) (*CompositeHost, *userFake, *agenttest.FakeSessionHost) {
	ctx := context.Background()
	primary := agenttest.NewFakeSessionHost()
	user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}}
	user.FakeSessionHost.StartAgent(ctx, AgentSpec{Name: "you-1"})
	primary.StartAgent(ctx, AgentSpec{Name: "focus-fix"})
	return NewCompositeHost(primary, user), user, primary
}

func TestListSessionsOnlyUser(t *testing.T) {
	c, _, _ := newSessionHarness(t)
	r := sessionTool(context.Background(), c, McpCall{Kind: CallListSessions})
	rows := r.Value.(map[string]any)["sessions"].([]SessionRow)
	if r.Err != nil || len(rows) != 1 || rows[0].Name != "you-1" || rows[0].Description != "login bug" {
		t.Fatalf("%+v %v", rows, r.Err)
	}
}

func TestMessageAndInterruptSession(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	ctx := context.Background()
	if r := sessionTool(ctx, c, McpCall{Kind: CallMessageSession, Name: "you-1", Text: "hi"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := sessionTool(ctx, c, McpCall{Kind: CallInterruptSession, Name: "you-1"}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if len(user.Prompts()) != 1 || len(user.Keys()) != 1 || user.Keys()[0].Keys[0] != "Escape" {
		t.Fatalf("prompts %v keys %v", user.Prompts(), user.Keys())
	}
	if len(user.Closed()) != 0 {
		t.Fatal("interrupt must not close")
	}
}

func TestSessionToolsRefuseWorkers(t *testing.T) {
	c, _, primary := newSessionHarness(t)
	r := sessionTool(context.Background(), c, McpCall{Kind: CallInterruptSession, Name: "focus-fix"})
	if r.Err == nil || !strings.Contains(r.Err.Error(), "worker") {
		t.Fatalf("%v", r.Err)
	}
	if len(primary.Keys()) != 0 {
		t.Fatal("a worker must not be touched by session tools")
	}
}

func TestDescribeSession(t *testing.T) {
	c, user, _ := newSessionHarness(t)
	sessionTool(context.Background(), c, McpCall{Kind: CallDescribeSession, Name: "you-1", Text: "uploads"})
	if user.described["you-1"] != "uploads" {
		t.Fatal(user.described)
	}
}

func TestCloseSessionIsAsked(t *testing.T) {
	// coordinator-settings.json must list it under permissions.ask so Claude asks the user.
	b := mustAsset(t, "coordinator-settings.json")
	if !strings.Contains(b, `"mcp__pixie__close_session"`) {
		t.Fatal("close_session must be in permissions.ask")
	}
}
```
  `mustAsset` reads `../app/agentapp/assets/<name>` relative to the test; put `TestCloseSessionIsAsked` in `core/app/agentapp` tests instead if a cross-package read is awkward (it reads the embedded asset there).

- [ ] **Step 2: Run** `cd core && go test ./agent/ -run Session` → FAIL.
- [ ] **Step 3: Implement**

`core/agent/session_tools.go`:
```go
package agent

import (
	"context"
	"errors"
	"fmt"
)

// SessionRow is a session you started (pix) as list_sessions shows it.
type SessionRow struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	PastDescriptions []string `json:"past_descriptions,omitempty"`
	Tool             string   `json:"tool"`
	Repo             string   `json:"repo,omitempty"`
	Where            string   `json:"where"`
	Status           string   `json:"status"`
	Reachable        bool     `json:"reachable"`
}

// Describer sets your description of a session.
type Describer interface {
	SetDescription(name, text string) error
}

func isSessionCall(k McpCallKind) bool {
	switch k {
	case CallListSessions, CallMessageSession, CallInterruptSession, CallCloseSession, CallDescribeSession:
		return true
	}
	return false
}

// sessionTool runs a session tool on host (the CompositeHost); other hosts have no sessions.
func sessionTool(ctx context.Context, host SessionHost, call McpCall) McpReply {
	c, ok := host.(*CompositeHost)
	if !ok {
		if call.Kind == CallListSessions {
			return McpReply{Value: map[string]any{"sessions": []SessionRow{}}}
		}
		return McpReply{Err: errors.New("Sessions you start yourself are not available.")}
	}
	sec := c.Secondary()
	if call.Kind == CallListSessions {
		list, err := sec.List(ctx)
		if err != nil {
			return McpReply{Err: err}
		}
		rows := []SessionRow{}
		for _, a := range list {
			if a.Origin == OriginUser {
				rows = append(rows, SessionRow{Name: a.Name, Description: a.Description, PastDescriptions: a.PastDescriptions,
					Tool: a.Tool, Repo: a.Repo, Where: a.Where, Status: a.Status.String(), Reachable: a.Reachable})
			}
		}
		return McpReply{Value: map[string]any{"sessions": rows}}
	}
	if !sec.Owns(call.Name) {
		if list, _ := c.primary.List(ctx); hasAgent(list, call.Name) {
			return McpReply{Err: fmt.Errorf("%s is a worker; use the worker tools.", call.Name)}
		}
		return McpReply{Err: fmt.Errorf("No session %s.", call.Name)}
	}
	var err error
	status := ""
	switch call.Kind {
	case CallMessageSession:
		err, status = sec.Prompt(ctx, call.Name, call.Text), "sent"
	case CallInterruptSession:
		err, status = sec.SendKeys(ctx, call.Name, []string{"Escape"}), "interrupted"
	case CallCloseSession:
		err, status = sec.Close(ctx, call.Name), "closing"
	case CallDescribeSession:
		d, ok := sec.(Describer)
		if !ok {
			return McpReply{Err: errors.New("Descriptions are not available.")}
		}
		if err = d.SetDescription(call.Name, call.Text); err == nil {
			return McpReply{Value: map[string]any{"name": call.Name, "description": call.Text}}
		}
	}
	if err != nil {
		return McpReply{Err: err}
	}
	return McpReply{Value: map[string]any{"name": call.Name, "status": status}}
}

func hasAgent(list []AgentInfo, name string) bool {
	for _, a := range list {
		if a.Name == name {
			return true
		}
	}
	return false
}
```

In `core/agent/types.go`, append to the `McpCallKind` consts (after `CallReportBlocked`):
```go
	CallListSessions
	CallMessageSession
	CallInterruptSession
	CallCloseSession
	CallDescribeSession
```

In `core/agent/worker_tools.go` `Catalog(RoleCoordinator)`, append:
```go
			{
				"name":        "list_sessions",
				"description": "Claude Code and Codex sessions the user started themselves with pix (in WebStorm, iTerm, …): name, description, past descriptions, tool, repo, where, status. Match what the user says to a description; ask if several fit.",
				"inputSchema": objectSchema(map[string]any{}),
			},
			{
				"name":        "message_session",
				"description": "Send a message to a session the user started. It arrives as if the user typed it.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The session's name from list_sessions."}, [2]string{"text", "The message."}), "name", "text"),
			},
			{
				"name":        "interrupt_session",
				"description": "Interrupt a session the user started (like pressing Esc). It keeps running.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The session's name."}), "name"),
			},
			{
				"name":        "close_session",
				"description": "End a session the user started. Only when the user asked for it; the user is asked to confirm.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The session's name."}), "name"),
			},
			{
				"name":        "describe_session",
				"description": "Set the user's own description of a session they started (\"call the WebStorm one login bug\"). Empty text clears it.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The session's name."}, [2]string{"text", "A few words, or empty to clear."}), "name"),
			},
```
And in the parse `switch name`:
```go
	case "list_sessions":
		return McpCall{Kind: CallListSessions}, nil
	case "message_session", "describe_session":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		kind := CallMessageSession
		if name == "describe_session" {
			kind = CallDescribeSession
			t, _ := text("text")
			return McpCall{Kind: kind, Name: n, Text: singleLine(t)}, nil
		}
		t, err := needPlain("text", MaxTaskChars)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: kind, Name: n, Text: t}, nil
	case "interrupt_session", "close_session":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		if name == "close_session" {
			return McpCall{Kind: CallCloseSession, Name: n}, nil
		}
		return McpCall{Kind: CallInterruptSession, Name: n}, nil
```
(`needName` validates `IsWorkerName`, which pix addresses satisfy.)

In `core/agent/runtime.go` `AgentHandle.mcp`, after the `CallStartWorker` branch:
```go
	if isSessionCall(call.Kind) {
		if agent != Coordinator {
			return McpReply{Err: errors.New("This tool is not available to you.")}
		}
		ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
		defer cancel()
		return sessionTool(ctx, h.deps.Host.Session(), call)
	}
```

`coordinator-settings.json`: add `"mcp__pixie__close_session"` to `permissions.ask`. Verify in Task 15 that `ask` beats the `mcp__pixie` allow (Claude's precedence is deny > ask > allow); if not, move it to `deny` and drop the tool.

`repo-work-workers.md` and `repo-work-solo.md`: add after the worker paragraph:
```markdown
- The user can also start Claude Code or Codex themselves with `pix` (in WebStorm, iTerm, …).
  `list_sessions` shows those sessions with a description of what each is doing. When the user
  refers to one ("the WebStorm one", "the login-bug session"), match their words to a
  description or past description; if several fit, ask which, by description. Use
  `message_session` to tell it something, `interrupt_session` to stop its current turn, and
  `close_session` only when the user asks to end it. These are not workers: they don't report
  results; read what they say instead. Never say a session's name to the user — use its
  description.
```

- [ ] **Step 4: Run** `cd core && go test ./agent/... ./app/... -race` → PASS (update any catalog-count test that lists coordinator tools).
- [ ] **Step 5: Commit** `agent: coordinator tools for sessions you start (#89)`.

---

### Task 11: Wire the wrapper host into the app

**Files:**
- Modify: `core/app/agentapp/agentapp.go:206-222` (after the primary host is built), shutdown path
- Modify: `core/app/agentapp/agentapp_test.go` (or the file that tests `Start`)

**Interfaces:**
- Consumes: `pixhost.New`, `pixhost.Host.Listen/Shutdown`, `agent.NewCompositeHost`, `pix/wire.SocketPath`.
- Produces: `a.session` is a `*agent.CompositeHost` whenever delegation is on; `a.pix *pixhost.Host` shut down in the app's agent shutdown (never closes pix sessions).

- [ ] **Step 1: Failing test** — in the agentapp tests, after `Start` with a fake primary host and a temp app-data dir:

```go
func TestStartListensForPix(t *testing.T) {
	a := startTestApp(t) // existing helper pattern in agentapp tests; temp AppSupport
	sock := wire.SocketPath(a.appSupport, os.Getuid(), os.TempDir())
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("pix socket not listening: %v", err)
	}
	wire.Encode(c, wire.Msg{Kind: wire.KindRegister, Tool: "claude", GitRoot: "/x/focus"})
	r, _ := wire.NewDecoder(c).Next()
	if r.Kind != wire.KindRegistered {
		t.Fatalf("%+v", r)
	}
	if _, ok := a.session.(*agent.CompositeHost); !ok {
		t.Fatal("session host must be a CompositeHost")
	}
}
```
  (Use the test file's existing app-start helper; if none fits, construct `Config` as the nearest existing `Start` test does.)

- [ ] **Step 2: Run** `cd core && go test ./app/agentapp/ -run Pix` → FAIL.
- [ ] **Step 3: Implement** — after `a.session = session` (`agentapp.go:221`):

```go
	// Sessions you start yourself with pix (#89) sit next to the primary host's agents.
	pixSock := wire.SocketPath(c.AppSupport, os.Getuid(), os.TempDir())
	ph := pixhost.New(pixSock, func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var names []string
		if list, err := session.List(ctx); err == nil {
			for _, ag := range list {
				names = append(names, ag.Name)
			}
		}
		return names
	})
	if err := ph.Listen(); err != nil {
		log.Printf("agent: pix: %v", err) // delegation still works without it
	} else {
		a.pix = ph
		session = agent.NewCompositeHost(session, ph)
		a.session = session
	}
```
  Use the actual field that holds `~/Library/Application Support/com.sihekuang.deskpixie` in `Config` (see `core/app/app.go:38`, `AppSupport`); add `pix *pixhost.Host` to the agent app struct; in its shutdown, call `a.pix.Shutdown()` after the runtime's shutdown. Replace trait checks on `a.session` that decide behaviour (Task 9 caution) with `agent.PrimaryOf(a.session)`.

- [ ] **Step 4: Run** `cd core && go test ./... -race` → PASS.
- [ ] **Step 5: Commit** `agentapp: listen for pix sessions next to the session host (#89)`.

---

### Task 12: Build, bundle and sign `pix`

**Files:**
- Modify: `core/Makefile`
- Modify: `mac/project.yml` (new build phase after "Copy libdeskpixie.dylib into Frameworks")
- Modify: `mac/Makefile:44-66`, `scripts/package-mac.sh:60-67`

- [ ] **Step 1: Add the Makefile target** (`core/Makefile`):

```make
.PHONY: build-pix
# pix: the terminal wrapper bundled at Contents/Helpers/pix (#89). Plain Go; host arch like the dylib.
build-pix:
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_MIN) CGO_ENABLED=0 go build -trimpath \
		-ldflags "-X main.version=$${VERSION:-dev}" -o build/pix ./cmd/pix
```
  Run: `cd core && make build-pix && ./build/pix version` → `pix dev`.

- [ ] **Step 2: project.yml phase** — after the libdeskpixie copy phase:

```yaml
      - name: Copy pix into Helpers
        script: |
          set -e
          make -C "$SRCROOT/../core" build-pix
          DST="$BUILT_PRODUCTS_DIR/$CONTENTS_FOLDER_PATH/Helpers"
          mkdir -p "$DST"
          cp -pf "$SRCROOT/../core/build/pix" "$DST/pix"
          ID="${EXPANDED_CODE_SIGN_IDENTITY:--}"
          FLAGS="--options runtime"
          if [ "$CONFIGURATION" = "Release" ]; then FLAGS="$FLAGS --timestamp"; fi
          codesign --force --sign "$ID" $FLAGS "$DST/pix"
        inputFiles:
          - "$(SRCROOT)/../core/build/pix"
        outputFiles:
          - "$(BUILT_PRODUCTS_DIR)/$(CONTENTS_FOLDER_PATH)/Helpers/pix"
```
  Match the existing dylib phase's identity/flags variables exactly (read `mac/project.yml:77-93` and copy its `ID`/`FLAGS` lines rather than the ones above if they differ). Regenerate: `cd mac && xcodegen generate`.

- [ ] **Step 3: Checks** — `scripts/package-mac.sh:61`: `for f in "$APP" "$APP"/Contents/Frameworks/*.dylib "$APP"/Contents/Helpers/pix; do`. `mac/Makefile:57`: add `"$$APP/Contents/Helpers/pix"` to the `for bin in` list, and `test -x "$$APP/Contents/Helpers/pix"` to the structural checks at line 50.

- [ ] **Step 4: Verify**
  Run: `cd mac && make` (or the repo's Release build target), then
  `codesign -dvv build/…/DeskPixie.app/Contents/Helpers/pix 2>&1 | grep -E 'runtime|Authority'` → shows `flags=0x10000(runtime)`.
  `build/…/DeskPixie.app/Contents/Helpers/pix version` → `pix <version>`.
- [ ] **Step 5: Commit** `build: bundle and sign pix in Contents/Helpers (#89)`.

---

### Task 13: Settings — "Install pix command"

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Platform/PixInstaller.swift`
- Create: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/PixInstallerTests.swift`
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Settings/SettingsLayout.swift:451-473`
- Modify: `mac/DeskPixie/Settings/SettingsWindowView.swift:218-232` (and the model that owns `resetPosition`/`openFile`)

**Interfaces:**
- Produces:
  ```swift
  public enum PixLinkState: Equatable, Sendable { case notInstalled, installed, broken, other(String) }
  public struct PixInstaller: Sendable {
      public init(home: URL, bundled: URL, path: String)
      public var link: URL                       // ~/.local/bin/pix
      public func state() -> PixLinkState
      public func install() throws               // mkdir ~/.local/bin; symlink (replaces only our own or a broken link)
      public func uninstall() throws             // removes only a link pointing at a DeskPixie pix
      public var onPath: Bool                    // ~/.local/bin in PATH
      public static let pathLine = "export PATH=\"$HOME/.local/bin:$PATH\""
      public static let aliases = "alias claude='pix claude'\nalias codex='pix codex'"
  }
  ```
  `SettingsLayout.Action` gains `.installPix`, `.uninstallPix`, `.copyPixAliases`; `extras(.agent)` returns a `.pix` extra (new case `case pix(help: String)`), rendered by the view as: state text, Install/Uninstall button, PATH hint when `!onPath`, "Copy aliases" button, and a "Learn more" link to `docs/pix.md` on GitHub.

- [ ] **Step 1: Failing tests**

```swift
import XCTest
@testable import DeskPixieCore

final class PixInstallerTests: XCTestCase {
    var home: URL!
    var bundled: URL!

    override func setUpWithError() throws {
        home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        bundled = home.appendingPathComponent("DeskPixie.app/Contents/Helpers/pix")
        try FileManager.default.createDirectory(at: bundled.deletingLastPathComponent(), withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: bundled.path, contents: Data("#!/bin/sh\n".utf8))
    }

    func testInstallCreatesSymlink() throws {
        let p = PixInstaller(home: home, bundled: bundled, path: "/usr/bin")
        XCTAssertEqual(p.state(), .notInstalled)
        try p.install()
        XCTAssertEqual(p.state(), .installed)
        XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: p.link.path), bundled.path)
    }

    func testBrokenWhenAppMoved() throws {
        let p = PixInstaller(home: home, bundled: bundled, path: "")
        try p.install()
        try FileManager.default.removeItem(at: bundled)
        XCTAssertEqual(p.state(), .broken)
    }

    func testDoesNotReplaceSomeoneElsesFile() throws {
        let p = PixInstaller(home: home, bundled: bundled, path: "")
        try FileManager.default.createDirectory(at: p.link.deletingLastPathComponent(), withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: p.link.path, contents: Data("mine".utf8))
        if case .other = p.state() {} else { XCTFail("expected .other") }
        XCTAssertThrowsError(try p.install())
        XCTAssertThrowsError(try p.uninstall())
        XCTAssertTrue(FileManager.default.fileExists(atPath: p.link.path))
    }

    func testUninstall() throws {
        let p = PixInstaller(home: home, bundled: bundled, path: "")
        try p.install()
        try p.uninstall()
        XCTAssertEqual(p.state(), .notInstalled)
    }

    func testOnPath() {
        let inPath = PixInstaller(home: home, bundled: bundled, path: "/usr/bin:\(home.path)/.local/bin")
        XCTAssertTrue(inPath.onPath)
        XCTAssertFalse(PixInstaller(home: home, bundled: bundled, path: "/usr/bin").onPath)
    }
}
```

- [ ] **Step 2: Run** `cd mac/Packages/DeskPixieCore && swift test --filter PixInstallerTests` → FAIL.
- [ ] **Step 3: Implement** `PixInstaller.swift`:

```swift
import Foundation

/// Where the pix symlink stands (#89).
public enum PixLinkState: Equatable, Sendable {
    case notInstalled, installed, broken
    /// Something else is at ~/.local/bin/pix (described); DeskPixie leaves it alone.
    case other(String)
}

public enum PixInstallError: Error, LocalizedError {
    case occupied(String)
    public var errorDescription: String? {
        switch self {
        case .occupied(let what): return "~/.local/bin/pix is \(what); DeskPixie won't replace it."
        }
    }
}

/// Installs the pix command as a symlink to the app's bundled copy, so it updates with the app.
public struct PixInstaller: Sendable {
    public let link: URL
    public let bundled: URL
    private let path: String

    public static let pathLine = "export PATH=\"$HOME/.local/bin:$PATH\""
    public static let aliases = "alias claude='pix claude'\nalias codex='pix codex'"

    public init(home: URL, bundled: URL, path: String) {
        self.link = home.appendingPathComponent(".local/bin/pix")
        self.bundled = bundled
        self.path = path
    }

    public var onPath: Bool {
        let dir = link.deletingLastPathComponent().path
        return path.split(separator: ":").contains { String($0) == dir }
    }

    public func state() -> PixLinkState {
        let fm = FileManager.default
        guard let dest = try? fm.destinationOfSymbolicLink(atPath: link.path) else {
            return fm.fileExists(atPath: link.path) ? .other("a file") : .notInstalled
        }
        guard isOurs(dest) else { return .other("a link to \(dest)") }
        return fm.fileExists(atPath: dest) ? (dest == bundled.path ? .installed : .broken) : .broken
    }

    public func install() throws {
        let fm = FileManager.default
        switch state() {
        case .installed: return
        case .other(let what): throw PixInstallError.occupied(what)
        case .broken: try fm.removeItem(at: link)
        case .notInstalled: break
        }
        try fm.createDirectory(at: link.deletingLastPathComponent(), withIntermediateDirectories: true)
        try fm.createSymbolicLink(atPath: link.path, withDestinationPath: bundled.path)
    }

    public func uninstall() throws {
        switch state() {
        case .notInstalled: return
        case .other(let what): throw PixInstallError.occupied(what)
        case .installed, .broken: try FileManager.default.removeItem(at: link)
        }
    }

    /// A link DeskPixie made: it points at some DeskPixie.app's Helpers/pix.
    private func isOurs(_ dest: String) -> Bool {
        dest == bundled.path || dest.hasSuffix(".app/Contents/Helpers/pix")
    }
}
```

  `SettingsLayout.swift`:
  ```swift
  public enum Action: Equatable, Sendable { case resetPosition, openFile, installPix, uninstallPix, copyPixAliases }
  public enum Extra: Equatable, Sendable {
      case button(label: String, help: String, title: String, action: Action)
      case secret(id: SettingsSecretID, label: String, help: String)
      /// The pix command row: install state, Install/Uninstall, PATH hint, Copy aliases (#89).
      case pix(help: String)
  }
  // in extras(_:)
  case .agent:
      return [.pix(help: "Run `pix claude` or `pix codex` in any terminal (WebStorm, iTerm, …) and Pixie can see, message and interrupt that session. Sessions you start this way keep running when DeskPixie quits.")]
  case .workers, .notifications, .canvas:
      return []
  ```
  Update any existing test that asserts `extras(.agent) == []`.

  `SettingsWindowView.swift` `extraRow`: add
  ```swift
        case .pix(let help):
            row(label: "pix command") {
                HStack(spacing: 6) {
                    switch model.pixState {
                    case .installed:
                        Button("Uninstall") { model.uninstallPix() }
                    case .notInstalled, .broken:
                        Button("Install") { model.installPix() }
                    case .other:
                        EmptyView()
                    }
                    Button("Copy Aliases") { model.copyPixAliases() }
                        .help("Copies alias lines for ~/.zshrc so `claude` and `codex` run through pix.")
                }
            } note: {
                VStack(alignment: .leading, spacing: 2) {
                    Text(model.pixStateText).font(.callout)
                    if let e = model.pixError { NoteText(text: e, isError: true) }
                    if !model.pixOnPath {
                        NoteText(text: "Add to ~/.zshrc: \(PixInstaller.pathLine)", isError: false)
                    }
                    NoteText(text: help, isError: false)
                    Link("How pix works", destination: URL(string: "https://github.com/sihekuang/deskpixie/blob/main/docs/pix.md")!)
                }
            }
  ```
  In the settings model (the type with `resetPosition()`/`openFile()`; find with `grep -rn "func openFile" mac/DeskPixie`), add:
  ```swift
    private var pix: PixInstaller {
        PixInstaller(home: FileManager.default.homeDirectoryForCurrentUser,
                     bundled: Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/pix"),
                     path: ProcessInfo.processInfo.environment["PATH"] ?? "")
    }
    @Published var pixState: PixLinkState = .notInstalled
    @Published var pixError: String?
    var pixOnPath: Bool { pix.onPath }
    var pixStateText: String {
        switch pixState {
        case .installed: return "Installed at ~/.local/bin/pix"
        case .notInstalled: return "Not installed"
        case .broken: return "Link broken (the app moved) — Install fixes it"
        case .other(let what): return "~/.local/bin/pix is \(what); left alone"
        }
    }
    func refreshPix() { pixState = pix.state() }
    func installPix() { do { try pix.install(); pixError = nil } catch { pixError = error.localizedDescription }; refreshPix() }
    func uninstallPix() { do { try pix.uninstall(); pixError = nil } catch { pixError = error.localizedDescription }; refreshPix() }
    func copyPixAliases() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(PixInstaller.aliases, forType: .string)
    }
  ```
  Call `refreshPix()` where the model loads (its `init` or `onAppear`). A GUI app's `PATH` is launchd's, not the shell's: `onPath` reads `$SHELL -lc 'echo $PATH'` once at load instead (`Process`, 2 s timeout), falling back to the environment.

- [ ] **Step 4: Run** `cd mac/Packages/DeskPixieCore && swift test --filter 'PixInstallerTests|SettingsLayout'` → PASS; build the app (`cd mac && make`), open Settings → Agent, check the row.
- [ ] **Step 5: Commit** `settings: Install pix command (#89)`.

---

### Task 14: Documentation

**Files:**
- Create: `docs/pix.md`
- Modify: `README.md` (new section after "Supported Herdr versions")

- [ ] **Step 1: Write `docs/pix.md`** with these sections, in plain language:
  1. **What pix is** — one paragraph: `pix claude` / `pix codex` instead of `claude` / `codex`; looks and works the same; Pixie can now see and reach that session.
  2. **Two kinds of sessions** — the spec's table (Pixie-started vs you-started), word for word in substance: who starts it, where it lives, what Bit may do, what quitting DeskPixie does, what closing the terminal does.
  3. **Install** — Settings → Agent → pix command → Install; what it creates (`~/.local/bin/pix` link to the app); PATH line; optional aliases (`alias claude='pix claude'`), and that plain `claude` is untouched without them.
  4. **Using it in WebStorm** — the built-in terminal (`pix claude`); the JetBrains Claude Code plugin if it lets you set its command (point it at `pix claude`); JetBrains' AI chat (ACP) sessions can't use pix.
  5. **How Pixie tells sessions apart** — the description (auto from your first prompt, the agent's `set_description`, your own: `pix --description "…"` or "call the WebStorm one 'login bug'"); you > agent > auto; it shows in the tab title.
  6. **What Pixie can do to your sessions** — message, interrupt (Esc); close only after asking you; never ends them on quit.
  7. **When messages wait** — Pixie never types over you: it waits until you've stopped typing and the agent is idle; the tab shows "📨 n".
  8. **Why it's built this way** — the spec's "Why this design", in user terms: owning the launch is the only reliable way in; per-run so nothing global changes; not MCP-only (can't wake or interrupt); not A2A (nothing outside DeskPixie needs it yet); not instead of Herdr (workers need a background host).
  9. **Troubleshooting** — `pix: link broken` (reinstall from Settings), "pix protocol N" (reinstall), "already connected in another pix" (two pix on one conversation), messages waiting (finish or clear your line), `pix version`.
- [ ] **Step 2: README** — add:
  ```markdown
  ## Sessions you start yourself

  Start Claude Code or Codex with `pix` (`pix claude`, `pix codex`) in any terminal — WebStorm,
  iTerm, VS Code — and Pixie can see, message and interrupt it next to its own workers. Install the
  command from Settings → Agent. See [docs/pix.md](docs/pix.md).
  ```
- [ ] **Step 3: Check** every claim against the code (flags, paths, tab text, tool names). Fix the doc, not the code, unless the code is wrong.
- [ ] **Step 4: Commit** `docs: pix, and why it is a wrapper (#89)`.

---

### Task 15: Live smoke test and the open checks

**Files:**
- Create: `core/pix/smoketest/smoke.go` (build tag `smoke`, like `core/tmux/smoketest`)
- Modify: spec "To verify during implementation" with findings

- [ ] **Step 1: Write the smoke runner** (opt-in, real `claude`): starts a `pixhost.Host` on a temp socket, runs `pix claude --model haiku` under a PTY with `DESKPIXIE_PIX_SOCK` pointing at it (add this env override to `DaemonSocket` for tests: if set, use it), then:
  1. waits for registration and an address;
  2. `Prompt(addr, "Reply with the single word PONG.")` → waits for `working` then `idle` events;
  3. `Prompt` a long task, then `SendKeys(Escape)` while `working` → `idle` within 10 s;
  4. `Shutdown()` the host, start a new one on the same socket → same address within 35 s;
  5. sends `/exit` via `Prompt` (or `Close`) → `exited` event; the pix process exit code is 0.
  Prints each passed step; non-zero exit on failure. Run: `cd core && go run -tags smoke ./pix/smoketest`.
- [ ] **Step 2: Run it** and record results.
- [ ] **Step 3: Resolve the spec's "To verify" items**, each with a one-line finding in the spec:
  - `env | grep -i -E 'TERMINAL_EMULATOR|__CFBundleIdentifier'` inside WebStorm's terminal (ask the owner to paste it if no WebStorm is reachable); fix `Where` if different.
  - Claude `permissions.ask` beats `allow: mcp__pixie` for `mcp__pixie__close_session` (call it from the coordinator in a throwaway run; expect a permission request).
  - Codex: run `pix codex` once; confirm the `-c` keys are accepted (`developer_instructions`, `mcp_servers.pixie.*`) and `set_description` shows in Codex's MCP tools. Drop any rejected key.
  - Whether Claude fills the session record's `name`; if it does, send it as an auto description from pix when it changes.
- [ ] **Step 4: Full test run** `cd core && go test ./... -race` and `cd mac/Packages/DeskPixieCore && swift test` → PASS.
- [ ] **Step 5: Commit** `pix: live smoke test and verified details (#89)`.

---

## Self-review notes

- Spec coverage: wrapper (T3–T7), bundling (T12), Settings link (T13), naming + `set_description` + three sources (T2, T5, T7, T8, T10 `describe_session`), lifecycle (T4, T7, T8), delivery (T6, T7), rules for you-started sessions (T10: interrupt ≠ close; close asked; shutdown never touches pix sessions — `runtime.go` stop-all iterates only known workers, and T11's `Shutdown` only closes the socket), CompositeHost (T9, T11), docs (T14), testing (all; conformance suite not reused because it requires `StartAgent` and `Read`, which the wrapper host doesn't support — T8's tests cover the same contract for the methods it has, and T15 covers the live path).
- Deviation from the spec, recorded here and to be noted in the spec: "close only after asking" is implemented through Claude's `permissions.ask` on `close_session` (Bit's existing spoken-confirm flow for permission requests), not a new confirm path; Pixie-started worker tools are unchanged (session tools are separate, so `EfStopWorker` is untouched).
