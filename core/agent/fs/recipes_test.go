package agentfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

func launch(kind agent.AgentKind) agent.WorkerLaunch {
	return agent.WorkerLaunch{
		Name: "focus-fix", Kind: kind,
		Repo: "/projects/focus", Worktree: "/projects/focus/.worktrees/fix", Token: "secret-token",
	}
}

func TestAClaudeWorkerGetsTrustAPrivateMcpConfigAndItsArgs(t *testing.T) {
	d := tempDir(t)
	trust := &recordingTrust{}
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "worker-settings.json"), 48217, "haiku", trust)
	p, err := r.Prepare(launch(agent.Claude))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trust.granted, []string{"/projects/focus/.worktrees/fix"}) {
		t.Fatal(trust.granted)
	}
	config := filepath.Join(d, "mcp/focus-fix.json")
	if m := mode(t, config); m != 0o600 {
		t.Fatalf("%o", m)
	}
	var v struct {
		McpServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, config)), &v); err != nil {
		t.Fatal(err)
	}
	if v.McpServers["pixie"].Headers["Authorization"] != "Bearer secret-token" || v.McpServers["pixie"].URL != "http://127.0.0.1:48217/mcp" {
		t.Fatal(v)
	}
	if p.Spec.Cwd != "/projects/focus/.worktrees/fix" {
		t.Fatal(p.Spec.Cwd)
	}
	if !reflect.DeepEqual(p.Spec.Env, []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "secret-token"}}) {
		t.Fatal(p.Spec.Env)
	}
	settings := filepath.Join(d, "worker-settings.json")
	want := []string{"--permission-mode", "auto", "--settings", settings, "--setting-sources", "user", "--mcp-config", config, "--strict-mcp-config", "--model", "haiku"}
	if !reflect.DeepEqual(p.Spec.Args, want) {
		t.Fatal(p.Spec.Args)
	}
	for _, a := range p.Spec.Args {
		if strings.Contains(a, "secret-token") {
			t.Fatal("the token is never in argv")
		}
	}
	if p.Refusal != nil {
		t.Fatal("refusal")
	}
	if p.Ready != nil {
		t.Fatal("a Claude start is unchanged")
	}
	r.Discard("focus-fix")
	if exists(config) {
		t.Fatal("not discarded")
	}
}

type noTrust struct{}

func (noTrust) Grant(dir string) ([]string, error) {
	return nil, agent.TrustError(dir + " is outside the trusted roots")
}

func TestARefusedTrustFailsTheStartAndWritesNothing(t *testing.T) {
	d := tempDir(t)
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 1, "", noTrust{})
	_, err := r.Prepare(launch(agent.Claude))
	if err == nil || !strings.Contains(err.Error(), "outside the trusted roots") {
		t.Fatal(err)
	}
	if exists(filepath.Join(d, "mcp")) {
		t.Fatal("mcp folder created")
	}
}

func TestABadWorkerNameIsRefused(t *testing.T) {
	d := tempDir(t)
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 1, "", &recordingTrust{})
	l := launch(agent.Claude)
	l.Name = "../escape"
	if _, err := r.Prepare(l); err == nil {
		t.Fatal("expected an error")
	}
	r.Discard("../escape") // never touches anything outside the folder
}

// gitLayout lays out <d>/focus as git lays out a repo with the linked worktree
// `.worktrees/<name>` (admin folder `.git/worktrees/<name>`, absolute links). Returns (repo, its .git, the launch).
func gitLayout(t *testing.T, d, name string) (string, string, agent.WorkerLaunch) {
	t.Helper()
	repo := filepath.Join(d, "focus")
	git := filepath.Join(repo, ".git")
	for _, sub := range []string{"objects", "hooks", "info", "refs/heads"} {
		mustMkdir(t, filepath.Join(git, sub))
	}
	mustWrite(t, filepath.Join(git, "config"), "[core]\n\tbare = false\n")
	worktree := addWorktree(t, repo, name)
	l := launch(agent.Codex)
	l.Repo, l.Worktree = repo, worktree
	return repo, git, l
}

// addWorktree adds `.worktrees/<name>` to a gitLayout repo; returns the worktree.
func addWorktree(t *testing.T, repo, name string) string {
	t.Helper()
	admin, worktree := filepath.Join(repo, ".git/worktrees", name), filepath.Join(repo, ".worktrees", name)
	mustMkdir(t, admin)
	mustMkdir(t, worktree)
	mustWrite(t, filepath.Join(admin, "commondir"), "../..\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), worktree+"/.git\n")
	mustWrite(t, filepath.Join(worktree, ".git"), "gitdir: "+admin+"\n")
	return worktree
}

func added(p agent.PreparedWorker) []string {
	var out []string
	for i := 0; i+1 < len(p.Spec.Args); i++ {
		if p.Spec.Args[i] == "--add-dir" {
			out = append(out, p.Spec.Args[i+1])
		}
	}
	return out
}

func codexRecipes(d string) *FileWorkerRecipes {
	return NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 1, "", &recordingTrust{})
}

func TestACodexWorkerNeedsNoFileAndNeverTrustsAFolder(t *testing.T) {
	d := tempDir(t)
	_, git, l := gitLayout(t, d, "fix")
	for _, sub := range []string{"refs/heads/pixie", "logs/refs/heads/pixie"} {
		mustMkdir(t, filepath.Join(git, sub))
	}
	trust := &recordingTrust{}
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 48217, "haiku", trust)
	p, err := r.Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(d, "mcp")) {
		t.Fatal("no MCP file for codex")
	}
	if len(trust.granted) != 0 {
		t.Fatal("~/.claude.json is Claude's")
	}
	if p.Spec.Kind != agent.Codex {
		t.Fatal(p.Spec.Kind)
	}
	want := []string{git + "/objects", git + "/worktrees/fix", git + "/refs/heads/pixie", git + "/logs/refs/heads/pixie"}
	if !reflect.DeepEqual(added(p), want) {
		t.Fatalf("%v", added(p))
	}
	for _, a := range p.Spec.Args {
		if strings.Contains(a, "hooks") || strings.Contains(a, "config") || a == "-C" {
			t.Fatalf("hooks and config stay read-only: %q", a)
		}
		if a == "haiku" {
			t.Fatal("agent.model is a Claude model")
		}
		if strings.Contains(a, "secret-token") {
			t.Fatal("token in argv")
		}
	}
	if !reflect.DeepEqual(p.Spec.Env, []agent.EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "secret-token"}}) {
		t.Fatal(p.Spec.Env)
	}
	if p.Refusal == nil {
		t.Fatal("the trust prompt fails the start")
	}
	wantScreens := [][]string{{"Trust this folder?", "1. Trust and continue"}, {"Do you trust the contents of this directory"}}
	if !reflect.DeepEqual(p.Refusal.Screens, wantScreens) {
		t.Fatal(p.Refusal.Screens)
	}
	wantReason := fmt.Sprintf("Codex asks to trust this repo before it will work in it; I won't trust it for you. Run `codex` once in %s and choose Trust, then ask again.", l.Repo)
	if p.Refusal.Reason != wantReason {
		t.Fatal(p.Refusal.Reason)
	}
	notReady := fmt.Sprintf("Codex didn't become ready (its screen ends with \"{last}\"); open Codex in %s to see why.", l.Repo)
	if p.Ready == nil || *p.Ready != (agent.StartReady{Marker: "Ask Codex to do anything", Reason: notReady, Settle: agent.CodexReadySettle}) {
		t.Fatalf("up only once its input box shows: %+v", p.Ready)
	}
	for _, a := range p.Spec.Args {
		if strings.Contains(a, "projects") || strings.Contains(a, "trust") {
			t.Fatalf("the app never trusts a folder: %q", a)
		}
	}
	// The live smoke's hook: extra args by repo, appended last.
	only := l.Repo
	extra := func(repo string) []string {
		if repo == only {
			return []string{"-c", "projects={}"}
		}
		return nil
	}
	smoke := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 48217, "", trust).WithCodexExtraArgs(extra)
	sp, err := smoke.Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	args := sp.Spec.Args
	if !reflect.DeepEqual(args[len(args)-2:], []string{"-c", "projects={}"}) {
		t.Fatal(args)
	}
	other := l
	other.Repo = filepath.Join(d, "elsewhere")
	op, err := smoke.Prepare(other)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(op.Spec.Args, "projects={}") {
		t.Fatal("only for its repo")
	}
}

func TestACodexWorkerFollowsAGitdirFileAndCreatesThePixieRefDirs(t *testing.T) {
	d := tempDir(t)
	repo, real := filepath.Join(d, "focus"), filepath.Join(d, "real.git")
	mustMkdir(t, repo)
	mustMkdir(t, filepath.Join(real, "objects"))
	mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+real+"\n")
	l := launch(agent.Codex)
	l.Repo, l.Worktree = repo, filepath.Join(repo, ".worktrees/fix")
	p, err := codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{real + "/objects", real + "/refs/heads/pixie", real + "/logs/refs/heads/pixie"}
	if !reflect.DeepEqual(added(p), want) {
		t.Fatal(added(p))
	}
}

func TestThePixieRefDirsAreMadeAgainAfterGitPrunedThem(t *testing.T) {
	d := tempDir(t)
	_, git, l := gitLayout(t, d, "fix")
	if exists(filepath.Join(git, "refs/heads/pixie")) || exists(filepath.Join(git, "logs")) {
		t.Fatal("precondition")
	}
	p, err := codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if !isDir(filepath.Join(git, "refs/heads/pixie")) || !isDir(filepath.Join(git, "logs/refs/heads/pixie")) {
		t.Fatal("ref dirs not made")
	}
	want := []string{git + "/objects", git + "/worktrees/fix", git + "/refs/heads/pixie", git + "/logs/refs/heads/pixie"}
	if !reflect.DeepEqual(added(p), want) {
		t.Fatal(added(p))
	}
}

func TestARelativeGitdirIsReadFromTheWorktree(t *testing.T) {
	d := tempDir(t)
	_, git, l := gitLayout(t, d, "fix")
	mustWrite(t, filepath.Join(l.Worktree, ".git"), "gitdir: ../../.git/worktrees/fix\n")
	mustWrite(t, filepath.Join(git, "worktrees/fix/gitdir"), "../../../.worktrees/fix/.git\n")
	// The process's cwd is elsewhere: the relative path is the worktree's.
	if cwd, _ := os.Getwd(); cwd == l.Worktree {
		t.Fatal("precondition")
	}
	p, err := codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(added(p), git+"/worktrees/fix") {
		t.Fatal(added(p))
	}
}

func hasWorktrees(list []string) bool {
	for _, a := range list {
		if strings.Contains(a, "worktrees") {
			return true
		}
	}
	return false
}

func TestAnotherWorktreesAdminDirIsNeverWritable(t *testing.T) {
	d := tempDir(t)
	repo, git, l := gitLayout(t, d, "fix")
	addWorktree(t, repo, "other")
	mustWrite(t, filepath.Join(l.Worktree, ".git"), "gitdir: "+git+"/worktrees/other\n")
	p, err := codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if hasWorktrees(added(p)) {
		t.Fatal(added(p))
	}
	// Its own admin dir, whose gitdir names another worktree, is dropped too.
	mustWrite(t, filepath.Join(l.Worktree, ".git"), "gitdir: "+git+"/worktrees/fix\n")
	mustWrite(t, filepath.Join(git, "worktrees/fix/gitdir"), repo+"/.worktrees/other/.git\n")
	p, err = codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if hasWorktrees(added(p)) {
		t.Fatal(added(p))
	}
}

func TestAReftableRepoIsRefusedForCodexOnly(t *testing.T) {
	for _, c := range []struct {
		name, config string
		dir          bool
	}{
		{"config", "[core]\n\tbare = false\n[extensions]\n\trefStorage = reftable\n", false},
		{"dir", "", true},
	} {
		d := tempDir(t)
		_, git, l := gitLayout(t, d, "fix")
		mustWrite(t, filepath.Join(git, "config"), c.config)
		if c.dir {
			mustMkdir(t, filepath.Join(git, "reftable"))
		}
		_, err := codexRecipes(d).Prepare(l)
		if err == nil || err.Error() != "This repo stores refs as reftable; Codex workers can't commit in it. Use a Claude worker." {
			t.Fatalf("%s: %v", c.name, err)
		}
		l.Kind = agent.Claude
		if _, err := codexRecipes(d).Prepare(l); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestATamperedWorktreeGitFileCannotPointOutsideTheGitDir(t *testing.T) {
	d := tempDir(t)
	repo, outside := filepath.Join(d, "focus"), filepath.Join(d, "outside")
	mustMkdir(t, filepath.Join(repo, ".git/objects"))
	mustMkdir(t, filepath.Join(repo, ".git/worktrees"))
	mustMkdir(t, outside)
	mustMkdir(t, filepath.Join(repo, ".worktrees/fix"))
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 1, "", &recordingTrust{})
	l := launch(agent.Codex)
	l.Repo, l.Worktree = repo, filepath.Join(repo, ".worktrees/fix")
	git := filepath.Join(repo, ".git")
	refs := []string{git + "/objects", git + "/refs/heads/pixie", git + "/logs/refs/heads/pixie"}
	for _, target := range []string{outside, filepath.Join(repo, ".git/worktrees"), filepath.Join(repo, ".git")} {
		mustWrite(t, filepath.Join(l.Worktree, ".git"), "gitdir: "+target+"\n")
		p, err := r.Prepare(l)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(added(p), refs) {
			t.Fatalf("%s: %v", target, added(p))
		}
	}
	// An object store that is a symlink out of the git dir: no --add-dir at all.
	if err := os.RemoveAll(filepath.Join(repo, ".git/objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".git/objects")); err != nil {
		t.Fatal(err)
	}
	p, err := r.Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.Spec.Args, "--add-dir") {
		t.Fatal(p.Spec.Args)
	}
}

func TestARepoWithoutAGitDirGetsNoAddDir(t *testing.T) {
	d := tempDir(t)
	r := NewFileWorkerRecipes(filepath.Join(d, "mcp"), filepath.Join(d, "w.json"), 1, "", &recordingTrust{})
	l := launch(agent.Codex)
	l.Repo = filepath.Join(d, "nothing")
	p, err := r.Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.Spec.Args, "--add-dir") {
		t.Fatal(p.Spec.Args)
	}
}

func TestStaleMcpConfigsAreCleared(t *testing.T) {
	d := tempDir(t)
	mustWrite(t, filepath.Join(d, "coordinator.json"), "{}")
	mustWrite(t, filepath.Join(d, "keep.txt"), "x")
	ClearMcpDir(d)
	if exists(filepath.Join(d, "coordinator.json")) {
		t.Fatal("not cleared")
	}
	if !exists(filepath.Join(d, "keep.txt")) {
		t.Fatal("keep.txt removed")
	}
	ClearMcpDir(filepath.Join(d, "missing")) // no folder: fine
}
