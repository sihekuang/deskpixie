# Fewer Approvals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A request for work in one of the user's projects starts a worker with at most one spoken confirmation for anything risky, and a Claude agent that silently fell back from auto to Manual mode is detected, explained, fixable, and still able to do routine work without asking.

**Architecture:** Go core changes in `core/agent` (start-screen trust answer, `create_project`, spoken path shortening, permission-mode tracking, tap-to-approve decisions, the Manual-mode cause table), `core/agent/fs` (project creation, Codex trust roots), `core/agent/httpserver` (read `permission_mode`), and `core/app/agentapp` (allow rules in the settings it writes, tray fix, instructions). Swift gets the tray fix items and the bubble's Allow/Deny buttons. Every change is unit-tested with fakes; live checks run first and live smokes last, in temp folders only.

**Tech Stack:** Go 1.x (core), Swift/SwiftUI + XCTest (mac/Packages/DeskPixieCore, mac/DeskPixie), Claude Code and Codex CLIs for live checks.

**Spec:** `docs/specs/2026-10-15-fewer-approvals-design.md`

## Global Constraints

- Never write the user's `~/.claude/settings.json` or Codex's `~/.codex/config.toml`; Codex records trust itself when its own screen is answered.
- Trust is answered only for a repo strictly inside the projects root (`agent.projectsRoot`), never the coordinator's workspace, never the coordinator's own start.
- Exactly one trust-answer attempt per start; any doubt fails the start with today's `CodexTrustRefusal` text.
- `create_project` names: 1–64 characters from `A–Z a–z 0–9 - _ .`, not starting with `.` or `-`, no `/`; refuses any existing path; removes only a folder it made itself on failure.
- Spoken path shortening: projects root + `/` → `projects/`, home + `/` → `home/`, root first; whole path components only; the command that runs is never changed.
- Existing `ask` and `deny` rules in both settings assets are unchanged; allow rules are exactly the lists in Task 5.
- Fixes follow #80's rules: inside DeskPixie runs directly; terminal commands are only copied, never run.
- Live tests and smokes use temp folders only — never the user's real repos (owner rule after the Sonnet benchmark).
- Commit footer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01H6pihU54KXJjKniQjKJogv`.
- Verify with `cd core && go vet ./... && go test -race ./...`; Swift with `cd mac/Packages/DeskPixieCore && swift test`; app with `make -C mac build`.

## Rulings (where the plan narrows the spec)

- Spec §1 says the trust answer is "recorded in the job's progress". The start path has no job-progress hook at the point the screen is answered, so the plan logs it (`agent: answered <worker>'s start screen with [1]`) instead. Cost if wrong: the user doesn't see "Trusted <repo>" in the job; add it to the worker's first progress line later.
- Spec §5 has Pixie say the short reason once. The cause is computed in the app layer (it needs `claude --help` and the managed-settings file), after the controller has already chosen to speak, so Pixie's one-time line is the fixed `ManualNotice` and the reason is in the tray line beside it. Cost if wrong: one extra glance at the menu.
- Spec §5's Setup row waits for #80 (the spec says "when #80 lands"); the tray line and Pixie's notice carry it until then.

## Review Focus

1. A projects root that is a string prefix of a sibling folder (`/Users/u/Projects` vs `/Users/u/Projects2/x`) must not be shortened or treated as inside the root — pinned in Task 2 and Task 4.
2. `create_project` names like `..`, `a/b`, `naïve`, `` (empty), 65 characters, or an existing file must be refused without touching disk — pinned in Task 3.
3. A Codex agent (coordinator or worker) never reports `permission_mode`, or reports something else; it must never be flagged as Manual — pinned in Task 6.
4. A tap on Allow/Deny for a token that already expired or was decided by voice must do nothing and not crash — pinned in Task 9.
5. The trust screen re-appearing after the answer (e.g. a second Codex prompt) must fail the start, not loop typing — pinned in Task 4.

---

### Task 1: Live checks (investigation, no product code)

Findings decide details in Tasks 4, 5 and 6. Record them in `docs/parity/fewer-approvals-live-checks.md` and commit it.

**Files:**
- Create: `docs/parity/fewer-approvals-live-checks.md`

- [ ] **Step 1: Which hook events carry `permission_mode`.** In a temp dir, write a settings file whose `SessionStart`, `PostToolUse`, `PermissionRequest` and `Stop` hooks are `{"type":"command","command":"cat >> $TMPDIR/hooks.jsonl"}`, then run:

```bash
T=$(mktemp -d); cd $T && git init -q
claude -p --permission-mode auto --settings $T/s.json --setting-sources user "run git status and then say done" --output-format json > out.json
claude -p --permission-mode default --settings $T/s.json --setting-sources user "run git status and then say done" --output-format json > out2.json
python3 -c 'import json;[print(json.loads(l).get("hook_event_name"), json.loads(l).get("permission_mode")) for l in open("'$TMPDIR'/hooks.jsonl")]'
```

Record which events carry the field and its values in each mode. If `SessionStart` carries it and `PostToolUse` doesn't, Task 6 adds a `SessionStart` hook to `coordinator-settings.json` and `worker-settings.json` posting to `/hooks/mode` (see Task 6, Step 6).

- [ ] **Step 2: Rule syntax.** In the same temp repo, write a settings file with `permissions.allow` = `["Bash(git status:*)", "Edit(//<T>/**)"]` (`<T>` = the temp dir without its leading `/`), run `claude -p --permission-mode default ... "run git status, then create a file note.txt containing hi"` with `--output-format json`, and check `permission_denials` in the JSON is empty and `note.txt` exists. Record the exact forms that work (`//abs` and `:*`).

- [ ] **Step 3: Codex trust keys.** With an isolated Codex home so the user's config is untouched:

```bash
C=$(mktemp -d); ln -s ~/.codex/auth.json $C/auth.json   # link only; never read or copy it
R=$(mktemp -d)/repo; mkdir -p $R && git -C $R init -q && git -C $R commit -q --allow-empty -m init
tmux new-session -d -s trustcheck -x 200 -y 50 "cd $R && CODEX_HOME=$C codex --no-daemon"
sleep 6; tmux capture-pane -p -t trustcheck | tail -20      # expect "Trust this folder?" + "1. Trust and continue"
tmux send-keys -t trustcheck 1; sleep 2; tmux capture-pane -p -t trustcheck | tail -8
# if the prompt is still there: tmux send-keys -t trustcheck Enter; sleep 2; capture again
cat $C/config.toml; tmux kill-session -t trustcheck
```

Record: whether `1` alone accepts, whether Enter is needed, and the key Codex wrote (`[projects."<path>"]`: the worktree or the main repo). Repeat once with `$R/.worktrees/w` made by `git -C $R worktree add .worktrees/w`.

- [ ] **Step 4: The hung `git init`.** Reproduce the 2026-10-15 09:39 case: run the Codex coordinator recipe against a temp projects root (`core/agent` live test helpers in `core/tmux/live_test.go`, `-tags live`, `SMOKE_CODEX=1`) and ask it to `git init` a new folder there with escalation; approve through the permission hook. Watch the pane (`tmux capture-pane`) while it hangs. Record where it waits: a Codex approval screen in the TUI after our hook allowed it, the hook reply never reaching Codex, or git. If the cause is in DeskPixie, add a task after Task 9 with a failing test for it before fixing (rule: the spec makes it part of this work).

- [ ] **Step 5: Commit the findings**

```bash
git add docs/parity/fewer-approvals-live-checks.md
git commit -m "docs: fewer approvals live checks (#97)"
```

---

### Task 2: Voice approvals read the projects path short

**Files:**
- Modify: `core/agent/confirm.go` (add `SpokenPaths`, `ShortenPaths`, `VoiceConfirmableWith`, `DescribeWith`; old functions wrap them)
- Modify: `core/agent/controller.go:54-76` (`Settings.SpokenPaths`), `:1162` and `:1177` (use the `With` forms)
- Modify: `core/app/agentapp/agentapp.go:~324` (fill `SpokenPaths`)
- Test: `core/agent/confirm_test.go`

**Interfaces:**
- Produces: `type SpokenPaths struct{ ProjectsRoot, Home string }`; `func ShortenPaths(cmd string, p SpokenPaths) string`; `func VoiceConfirmableWith(toolName string, toolInput any, p SpokenPaths) bool`; `func DescribeWith(toolName string, toolInput any, agent string, p SpokenPaths) string`; `agent.Settings.SpokenPaths SpokenPaths`.

- [ ] **Step 1: Write the failing tests** (append to `core/agent/confirm_test.go`)

```go
func TestSpokenPathsShortenTheProjectsRootFirst(t *testing.T) {
	p := SpokenPaths{ProjectsRoot: "/Users/u/Documents/Projects", Home: "/Users/u"}
	cmd := "git -C /Users/daniel/Documents/Projects/spacex-dashboard rev-parse --is-inside-work-tree"
	p0 := SpokenPaths{ProjectsRoot: "/Users/daniel/Documents/Projects", Home: "/Users/daniel"}
	if got := ShortenPaths(cmd, p0); got != "git -C projects/spacex-dashboard rev-parse --is-inside-work-tree" {
		t.Fatal(got)
	}
	if !VoiceConfirmableWith("Bash", map[string]any{"command": cmd}, p0) || VoiceConfirmable("Bash", map[string]any{"command": cmd}) {
		t.Fatal("the 09:40 command must fit only once shortened")
	}
	if got := ShortenPaths("cat /Users/u/notes.txt", p); got != "cat home/notes.txt" {
		t.Fatal(got)
	}
	// Whole path components only: a sibling that shares the prefix is left alone.
	if got := ShortenPaths("ls /Users/u/Documents/Projects2/x", p); got != "ls home/Documents/Projects2/x" {
		t.Fatal(got)
	}
	if got := ShortenPaths("ls /Users/u2/x", p); got != "ls /Users/u2/x" {
		t.Fatal(got)
	}
	// The root itself has no "/" after it, so only the home prefix applies.
	if got := ShortenPaths("ls /Users/u/Documents/Projects", p); got != "ls home/Documents/Projects" {
		t.Fatal(got)
	}
	if got := ShortenPaths("ls /x", SpokenPaths{}); got != "ls /x" {
		t.Fatal(got)
	}
	if got := DescribeWith("Bash", map[string]any{"command": "ls /Users/u/Documents/Projects/a"}, "focus", p); got != "Run ls projects/a in focus?" {
		t.Fatal(got)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd core && go test ./agent -run TestSpokenPathsShortenTheProjectsRootFirst`
Expected: FAIL, `undefined: SpokenPaths`.

- [ ] **Step 3: Implement** (in `core/agent/confirm.go`, below `MaxSpokenCommand`)

```go
// SpokenPaths are the folders a spoken command names short (#97): the projects root as
// "projects/" and the home folder as "home/". Empty fields shorten nothing.
type SpokenPaths struct{ ProjectsRoot, Home string }

// ShortenPaths replaces "<root>/" with "projects/" and "<home>/" with "home/" where the path
// starts a word (start of the command or after a space, quote or '='), root first. Only what is
// read and measured changes; the command that runs is untouched.
func ShortenPaths(cmd string, p SpokenPaths) string {
	for _, a := range [][2]string{{p.ProjectsRoot, "projects/"}, {p.Home, "home/"}} {
		root := strings.TrimRight(a[0], "/")
		if root == "" {
			continue
		}
		prefix := root + "/"
		var b strings.Builder
		for i := 0; i < len(cmd); {
			starts := i == 0 || strings.ContainsRune(" \t'\"=", rune(cmd[i-1]))
			if starts && strings.HasPrefix(cmd[i:], prefix) {
				b.WriteString(a[1])
				i += len(prefix)
				continue
			}
			b.WriteByte(cmd[i])
			i++
		}
		cmd = b.String()
	}
	return cmd
}

// VoiceConfirmableWith is VoiceConfirmable with paths read short.
func VoiceConfirmableWith(toolName string, toolInput any, p SpokenPaths) bool {
	if toolName == "Bash" {
		raw := ShortenPaths(stringField(toolInput, "command"), p)
		return isSpeakableText(raw) && utf8.RuneCountInString(SpeakableCommand(raw)) <= MaxSpokenCommand
	}
	return VoiceConfirmable(toolName, toolInput)
}

// DescribeWith is Describe with paths read short.
func DescribeWith(toolName string, toolInput any, agent string, p SpokenPaths) string {
	if toolName == "Bash" {
		toolInput = map[string]any{"command": ShortenPaths(stringField(toolInput, "command"), p)}
	}
	return Describe(toolName, toolInput, agent)
}
```

In `controller.go` add the field to `Settings`:

```go
	// SpokenPaths: folders a spoken approval names short (#97).
	SpokenPaths SpokenPaths
```

and change `onPermission`: `VoiceConfirmable(toolName, toolInput)` → `VoiceConfirmableWith(toolName, toolInput, c.settings.SpokenPaths)`; `Describe(toolName, toolInput, label)` → `DescribeWith(toolName, toolInput, label, c.settings.SpokenPaths)`.

In `core/app/agentapp/agentapp.go` where `agent.Settings{...}` is built (~line 324), add `SpokenPaths: agent.SpokenPaths{ProjectsRoot: <the resolved projects root used for WorkerAdapters>, Home: <home>}` — the same `root` and `home` values passed to `WorkerAdapters` at ~line 343 (hoist them above if they are computed later).

- [ ] **Step 4: Run tests**

Run: `cd core && go vet ./... && go test -race ./agent/... ./app/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/agent/confirm.go core/agent/confirm_test.go core/agent/controller.go core/app/agentapp/agentapp.go
git commit -m "agent: voice approvals read the projects path short (#97)"
```

---

### Task 3: `create_project`

**Files:**
- Modify: `core/agent/fs/worktree.go` (add `CreateProject`)
- Modify: `core/agent/traits.go:134` (`WorktreeFs` gains `CreateProject`)
- Modify: `core/agent/agenttest/fakes.go` (fake implements it)
- Modify: `core/agent/types.go:574-590` (`CallCreateProject`, `McpCall.Name` already exists for sessions — reuse it)
- Modify: `core/agent/worker_tools.go` (`CoordinatorTools`, `Catalog`, `ParseToolCall`)
- Modify: `core/agent/runtime.go:891` (dispatch to `h.createProject`)
- Modify: `core/app/agentapp/assets/repo-work-workers.md` (instruction bullet)
- Test: `core/agent/fs/worktree_test.go`, `core/agent/worker_tools_test.go`, `core/agent/runtime_worker_test.go`, `core/app/agentapp/files_test.go`

**Interfaces:**
- Produces: `func (g *GitWorktrees) CreateProject(name string) (string, error)` (returns the canonical path); `WorktreeFs.CreateProject(name string) (string, error)`; `CallCreateProject McpCallKind`; tool `create_project {name}` → `{"name": name, "path": path}`.

- [ ] **Step 1: Write the failing fs test** (append to `core/agent/fs/worktree_test.go`; use the file's existing helper that builds a `GitWorktrees` over a temp root with the real git runner — the same one `ResolveRepo` tests use)

```go
func TestCreateProjectMakesARepoAWorkerCanUse(t *testing.T) {
	g, root := newTestWorktrees(t) // existing helper: temp projects root + workspace
	path, err := g.CreateProject("spacex-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(root, "spacex-dashboard")); path != want {
		t.Fatalf("%s != %s", path, want)
	}
	if out, err := exec.Command("git", "-C", path, "log", "--format=%s").Output(); err != nil || strings.TrimSpace(string(out)) != "Start spacex-dashboard" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := g.Ensure(path, "first"); err != nil {
		t.Fatal("a worktree must be creatable at once: ", err)
	}
	if _, err := g.CreateProject("spacex-dashboard"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
}

func TestCreateProjectRefusesBadNamesWithoutTouchingDisk(t *testing.T) {
	g, root := newTestWorktrees(t)
	_ = os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0o600)
	before, _ := os.ReadDir(root)
	for _, name := range []string{"", ".", "..", ".hidden", "-x", "a/b", "naïve", "has space", strings.Repeat("a", 65), "afile"} {
		if _, err := g.CreateProject(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	after, _ := os.ReadDir(root)
	if len(after) != len(before) {
		t.Fatalf("disk changed: %v -> %v", before, after)
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `cd core && go test ./agent/fs -run TestCreateProject`
Expected: FAIL, `g.CreateProject undefined`.

- [ ] **Step 3: Implement** (in `core/agent/fs/worktree.go`)

```go
// projectName: 1–64 of A–Z a–z 0–9 - _ ., not starting with '.' or '-' (#97).
var projectName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

// CreateProject makes <root>/<name> a new git repository with one empty commit, so a worker's
// worktree can be made from it at once. It refuses a bad name or any existing path, and removes
// only a folder it made itself when a later step fails.
func (g *GitWorktrees) CreateProject(name string) (string, error) {
	if !projectName.MatchString(name) || name == "." || name == ".." {
		return "", werr("%q is not a usable project name: use letters, digits, '-', '_' or '.'.", name)
	}
	root, err := canonicalize(g.root)
	if err != nil {
		return "", werr("The projects root %s does not exist.", g.root)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Lstat(dir); err == nil {
		return "", werr("%s already exists under the projects root.", name)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", werr("Could not create %s: %v", name, err)
	}
	by := g.deadline()
	fail := func(step string, err error) (string, error) {
		_ = os.RemoveAll(dir)
		return "", werr("Could not %s for %s: %v", step, name, err)
	}
	if _, err := g.git(by, dir, "init", "-q", "-b", "main"); err != nil {
		return fail("initialise git", err)
	}
	args := []string{"commit", "-q", "--allow-empty", "-m", "Start " + name}
	if out, _ := g.run(by, dir, "config", "user.email"); strings.TrimSpace(out.Stdout) == "" {
		args = append([]string{"-c", "user.name=DeskPixie", "-c", "user.email=deskpixie@localhost"}, args...)
	}
	if _, err := g.git(by, dir, args...); err != nil {
		return fail("make the first commit", err)
	}
	return canonicalize(dir)
}
```

(Check the `agent.CommandOutput` field name for stdout in `traits.go` — use it instead of `Stdout` if it differs.) Add `CreateProject(name string) (string, error)` to `WorktreeFs` in `traits.go`, and to the fake in `agenttest/fakes.go`:

```go
func (f *FakeWorktrees) CreateProject(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Created = append(f.Created, name)
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	return "/projects/" + name, nil
}
```

(add `Created []string` and `CreateErr error` fields; match the fake's actual type and mutex names.)

- [ ] **Step 4: Run the fs tests**

Run: `cd core && go test ./agent/fs -run TestCreateProject`
Expected: PASS.

- [ ] **Step 5: Write the failing tool tests** (append to `core/agent/worker_tools_test.go` and `core/agent/runtime_worker_test.go`)

```go
func TestCreateProjectIsACoordinatorTool(t *testing.T) {
	var found bool
	for _, e := range Catalog(RoleCoordinator) {
		if e["name"] == "create_project" {
			found = true
		}
	}
	for _, e := range Catalog(RoleWorker) {
		if e["name"] == "create_project" {
			t.Fatal("workers can't create projects")
		}
	}
	call, err := ParseToolCall(RoleCoordinator, "create_project", map[string]any{"name": "spacex-dashboard"})
	if !found || err != nil || call.Kind != CallCreateProject || call.Name != "spacex-dashboard" {
		t.Fatalf("%v %+v %v", found, call, err)
	}
	if _, err := ParseToolCall(RoleCoordinator, "create_project", map[string]any{}); err == nil {
		t.Fatal("name is required")
	}
}
```

```go
func TestCreateProjectGoesToTheWorktreesAndOnlyForTheCoordinator(t *testing.T) {
	h, wt := newWorkerRuntime(t) // existing helper that wires FakeWorktrees; use its real name
	r := h.MCP(Coordinator, McpCall{Kind: CallCreateProject, Name: "spacex-dashboard"})
	if r.Err != nil || !strings.Contains(string(r.Result), `"path":"/projects/spacex-dashboard"`) {
		t.Fatalf("%+v", r)
	}
	if r := h.MCP("some-worker", McpCall{Kind: CallCreateProject, Name: "x"}); r.Err == nil {
		t.Fatal("a worker created a project")
	}
	if len(wt.Created) != 1 {
		t.Fatal(wt.Created)
	}
}
```

(Use the runtime test file's existing way to call `h.mcp` — an export in `runtime_export_test.go` or `Deliver(HookEvent{Kind: HookMcp...})`; add an export `func (h *AgentHandle) MCP(agent string, c McpCall) McpReply { return h.mcp(agent, c) }` there if none exists. Match `McpReply`'s result field name.)

- [ ] **Step 6: Run to see them fail**

Run: `cd core && go test ./agent -run 'TestCreateProject'`
Expected: FAIL, `undefined: CallCreateProject`.

- [ ] **Step 7: Implement the tool**

`types.go`: add `CallCreateProject` at the end of the `McpCallKind` const block.

`worker_tools.go`: change `CoordinatorTools` to `[5]string{"start_worker", "create_project", "message_worker", "stop_worker", "list_workers"}` (fix any code that depends on the array length); add to `Catalog(RoleCoordinator)` after `start_worker`:

```go
{
	"name":        "create_project",
	"description": "Create a new, empty project under the projects root: a folder with a git repository and one empty commit, ready for start_worker. Use it instead of mkdir or git init. Refuses a name that already exists. Returns {name, path} or an error.",
	"inputSchema": objectSchema(stringProps([2]string{"name", "The new project's folder name: letters, digits, '-', '_' or '.', e.g. spacex-dashboard."}), "name"),
},
```

and in `ParseToolCall`, a `case "create_project":` that requires a non-empty `name` string (copy the error style of the `stop_worker` case) and returns `McpCall{Kind: CallCreateProject, Name: name}`; reject it for `RoleWorker` the way other coordinator-only tools are rejected.

`runtime.go` in `mcp`, before the `CallStartWorker` check:

```go
	if call.Kind == CallCreateProject {
		return h.createProject(agent, call.Name)
	}
```

and below `startWorker`:

```go
// createProject is create_project (#97): a new repository under the projects root.
func (h *AgentHandle) createProject(agent, name string) McpReply {
	if agent != Coordinator || h.workers == nil {
		return McpReply{Err: errors.New("This tool is not available to you.")}
	}
	var path string
	err := within(h.ctx, RepoLimit, func() error {
		var err error
		path, err = h.workers.Worktrees.CreateProject(name)
		return err
	})
	if err != nil {
		return McpReply{Err: err}
	}
	return jsonReply(map[string]string{"name": name, "path": path})
}
```

(Use the existing success-reply helper the other tools use instead of `jsonReply` if it has another name; match `within`'s real signature from `startWorker`.)

`core/app/agentapp/assets/repo-work-workers.md`: add as the second bullet:

```
- For a new project, call `create_project` with its folder name, then `start_worker` with that name. Never create folders or run `git init` yourself.
```

and add a test to `core/app/agentapp/files_test.go` that the rendered coordinator `CLAUDE.md` and `AGENTS.md` both contain "`create_project`" when workers are on and neither does when they're off.

- [ ] **Step 8: Run all tests**

Run: `cd core && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add core/agent core/app/agentapp
git commit -m "agent: create_project makes a new repo a worker can start in (#97)"
```

---

### Task 4: Codex workers answer Codex's trust screen inside the projects root

**Files:**
- Modify: `core/agent/types.go:465` (`StartRefusal.Answer`)
- Modify: `core/agent/runtime.go:1508` (`checkStartScreen` answers once)
- Modify: `core/agent/fs/recipes.go:199-219` (set `Answer` when the repo is inside the roots)
- Modify: `core/agent/fs/recipes.go` constructor + `core/app/agentapp/workers.go:160` (pass the projects root)
- Test: `core/agent/runtime_worker_test.go`, `core/agent/fs/recipes_test.go`

**Interfaces:**
- Consumes: Task 1 Step 3's finding (keys that accept: `["1"]` and, if needed, `["Enter"]`).
- Produces: `StartRefusal{Screens [][]string; Reason string; Answer []string}` — `Answer` nil = never answer (today's behaviour).

- [ ] **Step 1: Write the failing runtime tests** (append to `core/agent/runtime_worker_test.go`, using the same fakes as `TestATrustPromptFailsTheStartAndNothingIsTyped` at :339 and its `codexLike` helper at :374)

```go
// #97: inside the projects root the trust screen is answered once, then the start goes on.
func TestATrustPromptInsideTheRootIsAnsweredOnceAndTheStartGoesOn(t *testing.T) {
	refusal := StartRefusal{Screens: CodexTrustScreensOwned(), Reason: "refused", Answer: []string{"1"}}
	host := newScreenHost(t, CodexTrustScreenForTest, CodexTrustScreenForTest, codexIdleScreen) // screens returned in order, last repeats
	err := checkStartScreen(context.Background(), host, "w", []StartRefusal{refusal}, codexReady(), PaneIdle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := host.keys(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("keys %v", got)
	}
}

func TestATrustPromptThatStaysAfterTheAnswerFailsTheStart(t *testing.T) {
	refusal := StartRefusal{Screens: CodexTrustScreensOwned(), Reason: "refused", Answer: []string{"1"}}
	host := newScreenHost(t, CodexTrustScreenForTest) // never goes away
	err := checkStartScreen(context.Background(), host, "w", []StartRefusal{refusal}, codexReady(), PaneIdle, time.Now())
	if err == nil || err.Error() != "refused" || len(host.keys()) != 1 {
		t.Fatalf("%v keys %v", err, host.keys())
	}
}

func TestARefusalWithoutAnAnswerStillTypesNothing(t *testing.T) {
	refusal := StartRefusal{Screens: CodexTrustScreensOwned(), Reason: "refused"}
	host := newScreenHost(t, CodexTrustScreenForTest)
	if err := checkStartScreen(context.Background(), host, "w", []StartRefusal{refusal}, codexReady(), PaneIdle, time.Now()); err == nil || len(host.keys()) != 0 {
		t.Fatal(err, host.keys())
	}
}
```

(`newScreenHost`, `codexIdleScreen`, `codexReady` — reuse the helpers the existing trust tests use; if they read screens from a fake `SessionHost`, extend that fake to record `SendKeys` calls as `keys()` and to step through a list of screens.)

- [ ] **Step 2: Run to see them fail**

Run: `cd core && go test ./agent -run 'TrustPrompt|RefusalWithoutAnAnswer'`
Expected: FAIL, unknown field `Answer`.

- [ ] **Step 3: Implement**

`types.go`:

```go
type StartRefusal struct {
	Screens [][]string
	Reason  string
	// Answer, when set, is sent once (SendKeys) the first time the screen shows; the start then
	// goes on, and fails with Reason if the screen is still there after AnswerGrace (#97: Codex's
	// trust screen in a repo under the projects root). Nil: never answered, the start fails.
	Answer []string
}
```

`runtime.go`: add `const AnswerGrace = 3 * time.Second` next to `StartScreenPoll`, and in `checkStartScreen` replace the refusal branch:

```go
		for i, r := range refusals {
			if !ShowsAnyScreen(text, r.Screens) {
				continue
			}
			if r.Answer != nil && answeredAt[i].IsZero() {
				if err := session.SendKeys(ctx, name, r.Answer); err != nil {
					return errors.New(r.Reason)
				}
				answeredAt[i] = time.Now()
				log.Printf("agent: answered %s's start screen with %v", name, r.Answer)
				continue
			}
			if r.Answer == nil || time.Since(answeredAt[i]) >= AnswerGrace {
				return errors.New(r.Reason)
			}
		}
```

with `answeredAt := make([]time.Time, len(refusals))` declared before the poll loop. (Keep the rest of the loop — the ready-marker wait — unchanged. If Task 1 found Enter is also needed, set `Answer: []string{"1", "Enter"}` in Step 5 rather than adding a second round.)

- [ ] **Step 4: Run the runtime tests**

Run: `cd core && go test ./agent -run 'TrustPrompt|RefusalWithoutAnAnswer|TestATrustPromptFailsTheStart|TestNoTrustPrompt'`
Expected: PASS (the old tests still pass: their refusals have no `Answer`).

- [ ] **Step 5: Write the failing recipe test** (in `core/agent/fs/recipes_test.go`, next to `TestACodexWorkerNeedsNoFileAndNeverTrustsAFolder` at :149 — rename that test to `TestACodexWorkerNeedsNoFile` and keep its file assertions)

```go
func TestACodexWorkerAnswersTrustOnlyInsideTheProjectsRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "repo")
	_ = os.Mkdir(inside, 0o755)
	outside := t.TempDir()
	r := newTestRecipes(t, root) // existing constructor helper, now given the projects root
	for _, c := range []struct {
		repo   string
		answer bool
	}{{inside, true}, {outside, false}, {root, false}, {root + "2", false}} {
		p, err := r.Prepare(agent.WorkerLaunch{Name: "w", Kind: agent.Codex, Repo: c.repo, Worktree: c.repo})
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Refusal != nil && p.Refusal.Answer != nil; got != c.answer {
			t.Errorf("%s: answer %v", c.repo, got)
		}
	}
}
```

- [ ] **Step 6: Run to see it fail, then implement**

Run: `cd core && go test ./agent/fs -run TestACodexWorkerAnswersTrust` → FAIL.

In `core/agent/fs/recipes.go`: add a `trustRoots []string` field to `FileWorkerRecipes`, set from a new constructor argument (projects root) in `NewFileWorkerRecipes`; in `codex(l)`:

```go
	refusal := &agent.StartRefusal{Screens: agent.CodexTrustScreensOwned(), Reason: agent.CodexTrustRefusal(l.Repo)}
	if r.insideTrustRoots(l.Repo) {
		refusal.Answer = []string{"1"} // Codex records the trust itself (#97); keys per docs/parity/fewer-approvals-live-checks.md
	}
```

```go
// insideTrustRoots: repo is strictly inside one of the roots (whole path components).
func (r *FileWorkerRecipes) insideTrustRoots(repo string) bool {
	real, err := canonicalize(repo)
	if err != nil {
		return false
	}
	for _, root := range r.trustRoots {
		if c, err := canonicalize(root); err == nil && real != c && hasPrefix(real, c) {
			return true
		}
	}
	return false
}
```

Update the doc comment at `recipes.go:15-20` ("Its folder-trust prompt is answered once for a repo under the projects root (#97); elsewhere it is never answered…"), update `CodexTrustRefusalText`'s callers only if wording must change (it doesn't), and pass `root` from `WorkerAdapters` (`core/app/agentapp/workers.go:160`) into `NewFileWorkerRecipes`. Fix every other caller of `NewFileWorkerRecipes` (tests) to pass a root.

- [ ] **Step 7: Run all tests**

Run: `cd core && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add core/agent core/app/agentapp
git commit -m "agent: Codex workers answer Codex's trust screen for repos under the projects root (#97)"
```

---

### Task 5: Allow rules in the settings the app writes

**Files:**
- Create: `core/app/agentapp/allow.go`
- Modify: `core/app/agentapp/files.go:305` (`InstallFor` adds the rules before `ValidateSettings`)
- Test: `core/app/agentapp/allow_test.go`

**Interfaces:**
- Consumes: Task 1 Step 2's verified rule forms (`//abs/**` and `:*`).
- Produces: `func WorkerAllowRules(projectsRoot string) []string`; `func CoordinatorAllowRules(workspace, canvas string) []string`; `func WithAllowRules(settingsJSON string, rules []string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

```go
package agentapp

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestWorkerAllowRulesAreExactlyTheReviewedList(t *testing.T) {
	got := WorkerAllowRules("/Users/u/Documents/Projects")
	want := []string{
		"Edit(//Users/u/Documents/Projects/*/.worktrees/**)",
		"Write(//Users/u/Documents/Projects/*/.worktrees/**)",
		"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
		"Bash(git add:*)", "Bash(git commit:*)", "Bash(git branch:*)", "Bash(git rev-parse:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
		"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)",
		"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)",
		"Bash(swift build:*)", "Bash(swift test:*)", "Bash(make:*)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestCoordinatorAllowRulesHaveNoBuildsOrTests(t *testing.T) {
	got := CoordinatorAllowRules("/a/workspace", "/a/canvas")
	want := []string{
		"Edit(//a/workspace/**)", "Write(//a/workspace/**)", "Edit(//a/canvas/**)", "Write(//a/canvas/**)",
		"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
		"Bash(git rev-parse:*)", "Bash(git branch:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestWithAllowRulesKeepsAskAndDenyAndAppendsOnce(t *testing.T) {
	in := RenderWorkerSettings(4321)
	out, err := WithAllowRules(in, []string{"Bash(ls:*)", "mcp__pixie"})
	if err != nil {
		t.Fatal(err)
	}
	var a, b struct{ Permissions map[string][]string }
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal([]byte(out), &b)
	if !slices.Equal(a.Permissions["ask"], b.Permissions["ask"]) || !slices.Equal(a.Permissions["deny"], b.Permissions["deny"]) {
		t.Fatal("ask/deny changed")
	}
	if want := append(slices.Clone(a.Permissions["allow"]), "Bash(ls:*)"); !slices.Equal(b.Permissions["allow"], want) {
		t.Fatalf("%q", b.Permissions["allow"])
	}
	if err := ValidateSettings(out); err != nil { // match ValidateSettings' real signature
		t.Fatal(err)
	}
}
```

Plus an `InstallFor` test: after `InstallFor(paths, port, root, false)` into a temp `Paths`, the written worker settings' `allow` contains `WorkerAllowRules(root)` and the coordinator's contains `CoordinatorAllowRules(paths.Workspace, paths.Canvas)`.

- [ ] **Step 2: Run to see them fail**

Run: `cd core && go test ./app/agentapp -run 'AllowRules|InstallFor'`
Expected: FAIL, undefined.

- [ ] **Step 3: Implement** `core/app/agentapp/allow.go`

```go
package agentapp

// Allow rules (#97): routine work that runs without asking even when Claude Code fell back to
// Manual mode. In auto mode they change nothing (auto allows these anyway). The ask and deny
// lists in the assets are untouched, so push, publish, deploy, rm -rf and .env still ask.

import (
	"encoding/json"
	"slices"
	"strings"
)

// abs is a path in Claude Code's absolute rule form: "/Users/u/x" → "//Users/u/x".
func abs(p string) string { return "/" + strings.TrimRight(p, "/") }

var readCommands = []string{
	"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
}

// WorkerAllowRules: edits inside any worker's worktree, git read/stage/commit, and builds/tests.
func WorkerAllowRules(projectsRoot string) []string {
	wt := abs(projectsRoot) + "/*/.worktrees/**"
	out := []string{"Edit(" + wt + ")", "Write(" + wt + ")"}
	out = append(out, readCommands...)
	return append(out,
		"Bash(git add:*)", "Bash(git commit:*)", "Bash(git branch:*)", "Bash(git rev-parse:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
		"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)",
		"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)",
		"Bash(swift build:*)", "Bash(swift test:*)", "Bash(make:*)",
	)
}

// CoordinatorAllowRules: edits in its workspace and canvas, and git/ls reads. No builds: the
// coordinator delegates those to workers.
func CoordinatorAllowRules(workspace, canvas string) []string {
	out := []string{
		"Edit(" + abs(workspace) + "/**)", "Write(" + abs(workspace) + "/**)",
		"Edit(" + abs(canvas) + "/**)", "Write(" + abs(canvas) + "/**)",
	}
	out = append(out, readCommands...)
	return append(out, "Bash(git rev-parse:*)", "Bash(git branch:*)", "Bash(ls:*)", "Bash(pwd)", "Bash(which:*)")
}

// WithAllowRules appends rules not already in permissions.allow, keeping everything else.
func WithAllowRules(settingsJSON string, rules []string) (string, error) {
	var v map[string]any
	if err := json.Unmarshal([]byte(settingsJSON), &v); err != nil {
		return "", err
	}
	perms, _ := v["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		v["permissions"] = perms
	}
	var allow []string
	if raw, ok := perms["allow"].([]any); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok {
				allow = append(allow, s)
			}
		}
	}
	for _, r := range rules {
		if !slices.Contains(allow, r) {
			allow = append(allow, r)
		}
	}
	perms["allow"] = allow
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
```

In `InstallFor`, for the worker settings string: `s, err := WithAllowRules(rendered, WorkerAllowRules(projectsRoot))`; for the coordinator: `WithAllowRules(rendered, CoordinatorAllowRules(paths.Workspace, paths.Canvas))` — after `WithStateHooks` and before `ValidateSettings`. When workers are off (`projectsRoot == ""`), skip the worker rules.

- [ ] **Step 4: Run all tests**

Run: `cd core && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/app/agentapp
git commit -m "agentapp: routine work is allowed without asking, even in Manual mode (#97)"
```

---

### Task 6: Track each Claude agent's permission mode

**Files:**
- Modify: `core/agent/types.go:645` (`HookEvent.PermissionMode`)
- Modify: `core/agent/httpserver/hooks.go:121` (read `permission_mode` in stop, permission, post-tool, and `mode` if Task 1 needs it)
- Modify: `core/agent/runtime.go:850` (`hook()` forwards a mode)
- Modify: `core/agent/controller.go` (`InMode`, `ModeInput`, per-agent modes, `EfManual` effect, the once-per-run Pixie line)
- Modify: `core/agent/traits.go` (`ModeSink`), `core/agent/runtime.go` Deps + effect handling
- Test: `core/agent/httpserver/hooks_test.go`, `core/agent/controller_test.go`

**Interfaces:**
- Produces: `HookEvent.PermissionMode string`; `func ModeInput(agent, mode string) Input`; effect `EfManual{Agent string, Manual bool}` (an `Effect` with `Kind: EfManual`, `Agent`, and `Manual` set); `type ModeSink interface{ ManualMode(agent string, manual bool) }` in `Deps.Modes` (nil: ignored); `const ManualNotice = "Heads up: the coordinator is asking before everything because it's running in Claude Code's Manual mode. I've put the reason in the menu."`.

- [ ] **Step 1: Write the failing hook-server test** (append to `core/agent/httpserver/hooks_test.go`, using its existing request helper)

```go
func TestHookBodiesCarryThePermissionMode(t *testing.T) {
	rt := &recRuntime{} // existing fake Runtime recording delivered events
	srv := newTestServer(t, rt) // existing helper with a coordinator token
	post(t, srv, "/hooks/stop", `{"last_assistant_message":"done","permission_mode":"default"}`)
	post(t, srv, "/hooks/post-tool", `{"tool_name":"Bash","tool_input":{},"permission_mode":"auto"}`)
	post(t, srv, "/hooks/stop", `{"last_assistant_message":"done"}`)
	got := rt.events()
	if got[0].PermissionMode != "default" || got[1].PermissionMode != "auto" || got[2].PermissionMode != "" {
		t.Fatalf("%+v", got)
	}
}
```

- [ ] **Step 2: Run to fail; implement**

Run: `cd core && go test ./agent/httpserver -run TestHookBodiesCarryThePermissionMode` → FAIL.

Add `PermissionMode string` to `HookEvent`; in `hook()` read `mode, _ := obj["permission_mode"].(string)` once after decoding and set `PermissionMode: clean(mode)` on the stop, permission and post-tool events. If Task 1 found only `SessionStart` carries it, add a `mode` event: `case "mode": s.rt.Deliver(agent.HookEvent{Kind: agent.HookMode, Agent: name, PermissionMode: clean(mode)})` and reply 200 `{}`, plus a `SessionStart` http hook to both settings assets (`/hooks/mode`, timeout 5) and to `HookTimeouts`/`WorkerHookTimeouts`.

- [ ] **Step 3: Write the failing controller tests** (append to `core/agent/controller_test.go`, using its existing controller/step helpers)

```go
func TestAnAutoLaunchedClaudeAgentReportingDefaultIsInManual(t *testing.T) {
	c := NewAgentController(DefaultSettings()) // CoordinatorKind zero = Claude
	fx := c.Step(ModeInput(Coordinator, "default"), 1000)
	m := effectsOf(fx, EfManual)
	if len(m) != 1 || m[0].Agent != Coordinator || !m[0].Manual {
		t.Fatalf("%+v", fx)
	}
	if lines := linesSaid(fx); len(lines) != 1 || lines[0] != ManualNotice {
		t.Fatalf("notice %v", lines)
	}
	if fx := c.Step(ModeInput(Coordinator, "default"), 2000); len(effectsOf(fx, EfManual)) != 0 || len(linesSaid(fx)) != 0 {
		t.Fatal("repeated")
	}
	fx = c.Step(ModeInput(Coordinator, "auto"), 3000)
	if m := effectsOf(fx, EfManual); len(m) != 1 || m[0].Manual {
		t.Fatalf("not cleared: %+v", fx)
	}
	// Back in Manual later: flagged again, but Pixie says it only once per app run.
	fx = c.Step(ModeInput(Coordinator, "default"), 4000)
	if len(effectsOf(fx, EfManual)) != 1 || len(linesSaid(fx)) != 0 {
		t.Fatalf("%+v", fx)
	}
}

func TestACodexAgentIsNeverFlaggedManual(t *testing.T) {
	s := DefaultSettings()
	s.CoordinatorKind = Codex
	c := NewAgentController(s)
	for _, m := range []string{"default", "", "on-request", "never"} {
		if fx := c.Step(ModeInput(Coordinator, m), 1000); len(effectsOf(fx, EfManual)) != 0 {
			t.Fatalf("%q flagged a Codex coordinator", m)
		}
	}
}
```

Also a worker case: admit a Codex worker and a Claude worker with the existing worker-admission helpers; `ModeInput(<codex worker>, "default")` yields no `EfManual`, `ModeInput(<claude worker>, "default")` yields one. (`effectsOf` and `linesSaid` — use the test file's existing effect filters; add small ones if missing.)

- [ ] **Step 4: Run to fail; implement**

`controller.go`: `InMode // Agent, Mode` in the input kinds, `Mode string` on `Input`, `func ModeInput(agent, mode string) Input { return Input{Kind: InMode, Agent: agent, Mode: mode} }`; fields `modes map[string]bool` (agent → manual) and `manualSaid bool` on `AgentController` (init the map in `NewAgentController`); `EfManual` in the effect kinds with `Agent string` and `Manual bool` on `Effect`. Handler:

```go
	case InMode:
		c.onMode(in.Agent, in.Mode, now, &fx)
```

```go
// onMode: a Claude agent launched with --permission-mode auto that reports "default" fell back
// to Manual (#97). Codex agents never report it, and anything else is ignored.
func (c *AgentController) onMode(agent, mode string, now Millis, fx *[]Effect) {
	if mode != "auto" && mode != "default" {
		return
	}
	kind := c.settings.CoordinatorKind
	if agent != Coordinator {
		w := c.workers.Get(agent)
		if w == nil {
			return
		}
		kind = w.Plan.Kind
	}
	if kind == Codex {
		return
	}
	manual := mode == "default"
	if c.modes[agent] == manual {
		return
	}
	c.modes[agent] = manual
	*fx = append(*fx, Effect{Kind: EfManual, Agent: agent, Manual: manual})
	if manual && !c.manualSaid {
		c.manualSaid = true
		c.deliver(NewLine(LineInfo, ManualNotice), now, fx)
	}
}
```

(`c.modes[agent]` defaults to false = not manual, so a first `auto` emits nothing. Check `WorkerPlan`'s kind field name.)

`traits.go`: `type ModeSink interface{ ManualMode(agent string, manual bool) }`; `runtime.go`: `Modes ModeSink` in `Deps` (nil-safe), `hook()` — after handling any event, `if ev.PermissionMode != "" { h.Input(ModeInput(ev.Agent, ev.PermissionMode)) }` (for the permission event, send it before the permission input), and in the effect switch: `case EfManual: if d.Modes != nil { d.Modes.ManualMode(e.Agent, e.Manual) }`.

- [ ] **Step 5: Run all tests**

Run: `cd core && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add core/agent
git commit -m "agent: notice when a Claude agent fell back to Manual mode (#97)"
```

---

### Task 7: The Manual-mode cause, tray line and fixes (core)

**Files:**
- Create: `core/agent/automode.go`, `core/agent/automode_test.go`
- Modify: `core/app/agentapp/agentapp.go` (implement `ModeSink`, compute the cause, expose it in `Tray()`, handle the fix action)
- Modify: `core/app/tray.go` / `core/app/app.go` (`AgentTray` gains the mode line and fix; `State()` shows it)
- Test: `core/app/agentapp/agentapp_test.go` (or the file testing `Tray()`/`Menu()`), `core/app/app_test.go`

**Interfaces:**
- Produces: `type ManualCause int` (`CauseOldModel`, `CauseOldClaude`, `CauseOrgOff`, `CauseUnavailable`); `func ManualCauseOf(model string, claudeListsAuto bool, managedOff bool) ManualCause`; `func ModelSupportsAuto(model string) bool`; `func (c ManualCause) Reason() string`; `app.AgentTray.Mode string` and `app.AgentTray.ModeFix *app.TrayFix` with `type TrayFix struct{ ID, Title, Copy string }` (ID set: Go handles `app.action`; Copy set: Swift copies the text, no Go call); action id `agent:fix-model`.

- [ ] **Step 1: Write the failing cause tests**

```go
func TestManualCauseOrder(t *testing.T) {
	for _, c := range []struct {
		model       string
		lists, org  bool
		want        ManualCause
	}{
		{"claude-sonnet-4-5", true, true, CauseOldModel},
		{"claude-opus-4-1", true, false, CauseOldModel},
		{"", false, false, CauseOldClaude},
		{"sonnet", false, true, CauseOldClaude},
		{"", true, true, CauseOrgOff},
		{"", true, false, CauseUnavailable},
		{"some-future-model", true, false, CauseUnavailable}, // unknown models are never blamed
	} {
		if got := ManualCauseOf(c.model, c.lists, c.org); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestModelSupportsAuto(t *testing.T) {
	for m, ok := range map[string]bool{
		"": true, "opus": true, "sonnet": true, "haiku": true, "claude-opus-4-6": true, "claude-sonnet-4-6": true,
		"claude-opus-5-5": true, "claude-haiku-5-5": true, "claude-fable-5-1": true,
		"claude-sonnet-4-5": false, "claude-opus-4-1": false, "claude-haiku-4-5": false, "claude-3-7-sonnet": false,
		"gpt-5": true, "who-knows": true,
	} {
		if ModelSupportsAuto(m) != ok {
			t.Errorf("%q", m)
		}
	}
}
```

- [ ] **Step 2: Run to fail; implement** `core/agent/automode.go`

```go
package agent

// Why a Claude agent launched in auto mode runs in Manual (#97), most likely first. Claude Code
// starts in Manual without a word when auto mode isn't available
// (https://code.claude.com/docs/en/permission-modes).

import (
	"regexp"
	"strconv"
)

type ManualCause int

const (
	CauseOldModel ManualCause = iota
	CauseOldClaude
	CauseOrgOff
	CauseUnavailable
)

// Reason is the tray's wording after "… is in Manual mode: ".
func (c ManualCause) Reason() string {
	switch c {
	case CauseOldModel:
		return "its model is too old for auto mode"
	case CauseOldClaude:
		return "Claude Code is too old for auto mode"
	case CauseOrgOff:
		return "your organization turned auto mode off"
	}
	return "auto mode isn't available for your account right now"
}

func ManualCauseOf(model string, claudeListsAuto, managedOff bool) ManualCause {
	switch {
	case !ModelSupportsAuto(model):
		return CauseOldModel
	case !claudeListsAuto:
		return CauseOldClaude
	case managedOff:
		return CauseOrgOff
	}
	return CauseUnavailable
}

var claudeModelID = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d+))?`)

// ModelSupportsAuto: the documented minimum on the API (Opus/Sonnet 4.6+, Haiku 5.5, Fable).
// Aliases, empty (Claude Code's default) and ids it doesn't recognise count as supported, so a
// model it doesn't know is never blamed.
func ModelSupportsAuto(model string) bool {
	if regexp.MustCompile(`^claude-\d`).MatchString(model) {
		return false // claude-3-x style ids predate auto mode
	}
	m := claudeModelID.FindStringSubmatch(model)
	if m == nil {
		return true
	}
	major, _ := strconv.Atoi(m[2])
	minor := 0
	if m[3] != "" && len(m[3]) <= 2 { // a date suffix (8 digits) is not a minor version
		minor, _ = strconv.Atoi(m[3])
	}
	switch m[1] {
	case "fable":
		return true
	case "haiku":
		return major > 5 || major == 5 && minor >= 5
	}
	return major > 4 || major == 4 && minor >= 6
}
```

Run: `cd core && go test ./agent -run 'ManualCause|ModelSupportsAuto'` → PASS.

- [ ] **Step 3: Write the failing app tests**

In the agentapp test file: build the `Agent` with injectable probes — `claudeListsAuto func() bool` (default: runs `claude --help` with a 3 s timeout and checks the `--permission-mode` choices contain `"auto"`) and `managedOff func() bool` (default: reads `/Library/Application Support/ClaudeCode/managed-settings.json`, true when `permissions.disableAutoMode == "disable"`; any error → false). Test:

```go
func TestManualModeShowsTheCauseAndTheModelFix(t *testing.T) {
	a := newTestAgent(t) // existing helper; set a.model = "claude-sonnet-4-5", a.claudeListsAuto = func() bool { return true }, a.managedOff = func() bool { return false }
	a.ManualMode(agent.Coordinator, true)
	tr := a.Tray()
	if tr.Mode != "Coordinator is in Manual mode: its model is too old for auto mode" || tr.ModeFix == nil || tr.ModeFix.ID != "agent:fix-model" {
		t.Fatalf("%+v", tr)
	}
	a.ManualMode(agent.Coordinator, false)
	if tr := a.Tray(); tr.Mode != "" || tr.ModeFix != nil {
		t.Fatalf("not cleared: %+v", tr)
	}
}

func TestOldClaudeOffersTheUpdateCommandToCopy(t *testing.T) {
	a := newTestAgent(t) // model "", claudeListsAuto false
	a.ManualMode("fix-login", true)
	tr := a.Tray()
	if tr.Mode != "A worker is in Manual mode: Claude Code is too old for auto mode" || tr.ModeFix == nil || tr.ModeFix.Copy != "claude update" || tr.ModeFix.ID != "" {
		t.Fatalf("%+v", tr)
	}
}

func TestFixModelClearsTheModelAndRestarts(t *testing.T) {
	a, saved, restarted := newTestAgentWithSave(t) // records the settings change and Restart calls
	a.Menu("agent:fix-model")
	if saved() == nil || *saved() != nil /* agent.model set back to null = Claude Code's default */ || !restarted() {
		t.Fatal("fix not applied")
	}
}
```

(Adapt helper names to the file; the fix sets `agent.model` to null through the app's settings save path — `App.save` or the settings service the `Agent` already has — and calls `a.handle.Restart()`.)

In `core/app/app_test.go`: `State()` with an `AgentTray{Mode: "Coordinator is in Manual mode: …"}` lists that line in `StatusLines`, and the tray JSON carries `agentModeFix` `{id,title,copy}`.

- [ ] **Step 4: Implement**

- `core/app` (`tray.go`): `type TrayFix struct { ID string `json:"id,omitempty"`; Title string `json:"title"`; Copy string `json:"copy,omitempty"` }`; `AgentTray` gains `Mode string` and `ModeFix *TrayFix`; `App.State()` appends `Mode` to `StatusLines` when non-empty and exposes `ModeFix` as `agentModeFix` in the tray state.
- `agentapp.Agent`: fields `manual map[string]bool`, `claudeListsAuto`, `managedOff`; `ManualMode(agent string, manual bool)` updates the map and calls `hooks.RefreshTray`; `Tray()` fills `Mode`/`ModeFix` when any agent is manual: subject "Coordinator" if the coordinator is manual, else "A worker"; cause from `agent.ManualCauseOf(model, a.claudeListsAuto(), a.managedOff())`, computed once per change (not on every `Tray()` call — cache it in `ManualMode`); fix: `CauseOldModel` → `&TrayFix{ID: "agent:fix-model", Title: "Use a supported model"}`, `CauseOldClaude` → `&TrayFix{Title: "Copy update command", Copy: "claude update"}`, else nil.
- Pass the `Agent` as `Deps.Modes` where the runtime `Deps` are built.
- `Agent.Menu`: `case "agent:fix-model":` set `agent.model` to null via the settings save, emit `settings.changed` the way other settings writes from Go do, then `a.handle.Restart()`.

- [ ] **Step 5: Run all tests**

Run: `cd core && go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add core/agent/automode.go core/agent/automode_test.go core/app
git commit -m "app: say why an agent is in Manual mode and offer the fix (#97)"
```

---

### Task 8: Tray fix item (Swift)

**Files:**
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Tray/TrayMenu.swift` (decode `agentModeFix`, add the item under the status lines)
- Modify: `mac/DeskPixie/MenuBar/MenuBarMenu.swift` and `mac/DeskPixie/Engine/EngineCoordinator.swift` (route: `id` → `app.action`; `copy` → pasteboard)
- Test: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/TrayMenuTests.swift`

**Interfaces:**
- Consumes: tray state JSON `agentModeFix: {id?, title, copy?}` from Task 7.

- [ ] **Step 1: Write the failing tests** (in `TrayMenuTests.swift`, using its existing builders)

```swift
func testAModeFixWithAnIdIsAnAction() throws {
    let state = try TrayAppState.decode(#"{"statusLines":["Coordinator is in Manual mode: its model is too old for auto mode"],"agentModeFix":{"id":"agent:fix-model","title":"Use a supported model"}}"#)
    let items = TrayMenu.build(state)
    let fix = try XCTUnwrap(items.first { $0.title == "Use a supported model" })
    XCTAssertEqual(TrayMenu.route(fix), .action("agent:fix-model"))
}

func testAModeFixWithCopyCopiesAndCallsNothing() throws {
    let state = try TrayAppState.decode(#"{"statusLines":["A worker is in Manual mode: Claude Code is too old for auto mode"],"agentModeFix":{"title":"Copy update command","copy":"claude update"}}"#)
    let fix = try XCTUnwrap(TrayMenu.build(state).first { $0.title == "Copy update command" })
    XCTAssertEqual(TrayMenu.route(fix), .copy("claude update"))
}
```

(Match the real names: the state type, the builder, the route enum; add a `.copy(String)` route case if none exists.)

- [ ] **Step 2: Run to fail; implement; run to pass**

Run: `cd mac/Packages/DeskPixieCore && swift test --filter TrayMenuTests` → FAIL, then implement: decode `agentModeFix` (optional), insert an enabled item right after the status lines, route `.action(id)` when `id` is set, else `.copy(copy)`. In `MenuBarMenu.swift`/`EngineCoordinator.swift` handle `.copy` with `NSPasteboard.general.clearContents(); NSPasteboard.general.setString(text, forType: .string)`. Run again → PASS, then `make -C mac build` → BUILD SUCCEEDED.

- [ ] **Step 3: Commit**

```bash
git add mac
git commit -m "mac: the tray offers the Manual-mode fix (#97)"
```

---

### Task 9: Tap to approve

**Files:**
- Modify: `core/agent/confirm.go` (`Confirmations.DecideToken`)
- Modify: `core/agent/controller.go` (`InDecide`, `DecideInput`)
- Modify: `core/agent/bubble.go:9` (`UpdateItem.Token`)
- Modify: `core/app/app.go:~1110-1165` (bridge `agent.decide`)
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/UpdatesBubbleModel.swift`, `mac/DeskPixie/Pixie/UpdatesBubbleView.swift`, `mac/DeskPixie/Pixie/UpdatesBubbleController.swift`
- Test: `core/agent/confirm_test.go`, `core/agent/controller_test.go`, `core/app/app_test.go`, `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/UpdatesBubbleModelTests.swift`

**Interfaces:**
- Produces: `func (c *Confirmations) DecideToken(token string, allow bool) (Decided, bool)`; `func DecideInput(token string, allow bool) Input`; `UpdateItem.Token string` (json `token,omitempty`, set only for pending questions); bridge `agent.decide {token: string, allow: bool}` → `{}`; `AgentHandle.Decide(token string, allow bool)`.

- [ ] **Step 1: Write the failing Go tests**

```go
func TestDecideTokenDecidesOnceAndIgnoresUnknownTokens(t *testing.T) {
	c := NewConfirmations(120_000)
	tok, _ := c.Request("r1", Coordinator, "", 0)
	if _, ok := c.DecideToken("nope", true); ok {
		t.Fatal("unknown token decided")
	}
	d, ok := c.DecideToken(tok, true)
	if !ok || !d.Allow || d.Pending.ID != "r1" { // match Decided's real field names
		t.Fatalf("%+v", d)
	}
	if _, ok := c.DecideToken(tok, false); ok {
		t.Fatal("decided twice")
	}
}
```

Controller (append to `controller_test.go`): a coordinator permission request → its token from the `LineQuestion`; `DecideInput(token, true)` yields one `EfDecide` allow for that request id; `DecideInput(token, false)` on a fresh request yields a deny; `DecideInput("expired-or-unknown", true)` yields no effects. And the bubble: `bubbleItems()` for a pending question carries `Token` = its token; after it's decided the item is gone (existing behaviour) — assert `Token` is empty for non-question lines.

App (`app_test.go`): `agent.decide` with `{"token":"hawk","allow":true}` reaches `AgentHandle.Decide` (use the app test's agent fake); a malformed body returns an error.

- [ ] **Step 2: Run to fail; implement**

`confirm.go`:

```go
// DecideToken settles the request with token from a tap on Pixie's bubble (#97): the same
// outcome as the confirm word (allow) or a refusal (deny). Unknown or used tokens do nothing.
func (c *Confirmations) DecideToken(token string, allow bool) (Decided, bool) {
	p, ok := c.take(token)
	if !ok {
		return Decided{}, false
	}
	return Decided{p, allow}, true
}
```

`controller.go`: `InDecide // Token, Allow`; `Token string` and `Allow bool` on `Input` (reuse if present); `func DecideInput(token string, allow bool) Input { return Input{Kind: InDecide, Token: token, Allow: allow} }`; handler: on `ok`, emit the same effects `onUserSaid` emits for a voice decision (reuse the loop body that turns a `Decided` into `decideEffect` + job transition + `syncUpdates` — extract it to `func (c *AgentController) applyDecided(d Decided, now Millis, fx *[]Effect)` and call it from both places).

`runtime.go`: `func (h *AgentHandle) Decide(token string, allow bool) { h.Input(DecideInput(token, allow)) }`. `bubble.go`: set `Token` on items built from lines whose `Token` is pending. `app.go`: bridge case `"agent.decide"` decoding `{token, allow}` and calling the agent's `Decide`.

Run: `cd core && go vet ./... && go test -race ./...` → PASS.

- [ ] **Step 3: Write the failing Swift test** (`UpdatesBubbleModelTests.swift`)

```swift
func testAPendingQuestionOffersAllowAndDeny() throws {
    let line = try UpdateLine.decode(#"{"id":"1","text":"Run git push in focus? Say confirm hawk.","kind":"question","at":1,"token":"hawk"}"#)
    XCTAssertEqual(line.token, "hawk")
    XCTAssertTrue(line.offersDecision)
    let info = try UpdateLine.decode(#"{"id":"2","text":"Done.","kind":"reply","at":2}"#)
    XCTAssertFalse(info.offersDecision)
}
```

- [ ] **Step 4: Implement Swift; run to pass**

`UpdateLine` gains `token: String?` and `var offersDecision: Bool { kind == "question" && token != nil }`. `UpdatesBubbleView`: for such a line, an `HStack` with two small buttons, "Allow" (prominent) and "Deny", calling a closure `onDecide(token, allow)`. `UpdatesBubbleController` wires that closure to `engine.call("agent.decide", ["token": token, "allow": allow])` (match the engine call API used for `agent.updatesSeen`). Run `swift test --filter UpdatesBubbleModelTests` → PASS; `make -C mac build` → BUILD SUCCEEDED.

- [ ] **Step 5: Commit**

```bash
git add core mac
git commit -m "Tap to approve: Allow and Deny on Pixie's bubble (#97)"
```

---

### Task 10: Live smokes (temp folders only)

**Files:**
- Modify: `core/tmux/live_test.go` (or `core/herdr/live_test.go`, matching where the Codex worker live tests sit at :1028 / :888)
- Test: the same files, `-tags live`

- [ ] **Step 1: Add three live tests** next to the existing Codex worker live test, each with a `t.TempDir()` projects root and an isolated `CODEX_HOME` (symlinked `auth.json`, as Task 1 Step 3) so the user's Codex config is never written:
  1. `TestLiveCodexWorkerStartsInAnUntrustedRepo`: `create_project` a repo through `GitWorktrees.CreateProject`, `start_worker` kind codex there; the start succeeds and the temp `CODEX_HOME/config.toml` now trusts that path.
  2. `TestLiveCreateProjectThenStartWorker`: the coordinator-facing path end to end with a Claude worker.
  3. `TestLiveManualClaudeWorkerRunsRoutineWorkWithoutAsking`: a Claude worker started with `--permission-mode default` (override the recipe arg in the test) runs `git status` and edits a file in its worktree with no PermissionRequest reaching the fake controller, and a hook delivered `permission_mode: "default"`.

- [ ] **Step 2: Run them**

Run: `cd core && SMOKE_CODEX=1 go test -tags live ./tmux -run 'TestLive(CodexWorkerStartsInAnUntrustedRepo|CreateProjectThenStartWorker|ManualClaudeWorkerRunsRoutineWorkWithoutAsking)' -v -timeout 15m`
Expected: PASS. Then the full unit suites: `cd core && go vet ./... && go vet -tags live ./... && go test -race ./...`; `cd mac/Packages/DeskPixieCore && swift test`; `make -C mac build`.

- [ ] **Step 3: Commit**

```bash
git add core
git commit -m "live: fewer approvals smokes (#97)"
```
