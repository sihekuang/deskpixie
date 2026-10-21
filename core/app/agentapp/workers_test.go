package agentapp

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/settings"
)

// Port of src-tauri/src/agent/workers.rs tests (8).

// The Rust compile-time assert `GIT_TIMEOUT < ENSURE_BUDGET` (the last budget relation).
func TestOneGitCallFitsInTheWorktreeBudget(t *testing.T) {
	if !(GitTimeout < agent.EnsureBudget) || !(GitTimeout < agentfs.EnsureBudget) {
		t.Fatalf("GitTimeout %v must stay below EnsureBudget %v", GitTimeout, agent.EnsureBudget)
	}
	if GitTimeout.Seconds() != 45 || GitCleanupTimeout.Seconds() != 60 {
		t.Fatal("the Rust values")
	}
}

func TestThePolicyFollowsTheSettings(t *testing.T) {
	w := settings.WorkerSettings{Enabled: true, Max: 0, Kinds: []string{"Claude", "codex", "gpt", "claude"}}
	want := agent.WorkerPolicy{Enabled: true, Max: 1, Kinds: []agent.AgentKind{agent.Claude, agent.Codex}}
	if got := Policy(w); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	def := settings.Default().Agent.Workers
	if Policy(def).Enabled {
		t.Fatal("off by default")
	}
	def.Max = 99
	if Policy(def).Max != 16 {
		t.Fatal("max 16")
	}
}

func tmpDir(t *testing.T, name string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d = filepath.Join(d, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNoUsableKindTurnsWorkersOff(t *testing.T) {
	w := settings.WorkerSettings{Enabled: true, Max: 4, Kinds: []string{"gpt"}}
	if Policy(w).Enabled {
		t.Fatal("enabled")
	}
	if _, err := WorkersOn(w, Policy(w)); err == nil || err.Error() != "Coordinator: workers off: no usable worker kind" {
		t.Fatal(err)
	}
	ok := settings.WorkerSettings{Enabled: true, Max: 4, Kinds: []string{"codex"}}
	if on, err := WorkersOn(ok, Policy(ok)); !on || err != nil {
		t.Fatal(on, err)
	}
	off := settings.WorkerSettings{Enabled: false, Max: 4}
	if on, err := WorkersOn(off, Policy(off)); on || err != nil {
		t.Fatal("off is not a problem")
	}
}

func TestAWorkersProblemTurnsOnlyWorkersOff(t *testing.T) {
	home := tmpDir(t, "setuphome")
	code := filepath.Join(home, "code")
	on := settings.WorkerSettings{Enabled: true, Max: 4, Kinds: []string{"claude"}}
	none := SetupWorkers(on, UsedFolder{Why: "no room"})
	if none.Policy.Enabled || none.Root != "" || none.Notice != "Coordinator: workers off: no projects folder" {
		t.Fatalf("%+v", none)
	}
	noKind := on
	noKind.Kinds = []string{"gpt"}
	nk := SetupWorkers(noKind, UsedFolder{Root: code})
	if nk.Policy.Enabled || nk.Root != "" || nk.Notice != "Coordinator: workers off: no usable worker kind" {
		t.Fatalf("%+v", nk)
	}
	good := SetupWorkers(on, UsedFolder{Root: code})
	if !good.Policy.Enabled || good.Notice != "" || good.Root != code {
		t.Fatalf("%+v", good)
	}
	// DeskPixie's own folder: workers run, and the tray says where (#216).
	fb := SetupWorkers(on, UsedFolder{Root: "/data/agent/projects", Fallback: true, Why: "outside home"})
	if !fb.Policy.Enabled || fb.Root != "/data/agent/projects" || fb.Notice != FallbackNotice {
		t.Fatalf("%+v", fb)
	}
	off := SetupWorkers(settings.Default().Agent.Workers, UsedFolder{Root: "/data/agent/projects", Fallback: true})
	if off.Policy.Enabled || off.Notice != "" {
		t.Fatal("off is not news")
	}
}

func TestTheProjectsRootExpandsHomeAndIsCanonical(t *testing.T) {
	home := tmpDir(t, "home")
	if err := os.MkdirAll(filepath.Join(home, "code/real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "code/real"), filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{
		"~/code":                                filepath.Join(home, "code"),
		"code":                                  filepath.Join(home, "code"),
		" " + filepath.Join(home, "code") + " ": filepath.Join(home, "code"),
		"~/link":                                filepath.Join(home, "code/real"),
	} {
		got, err := ProjectsRoot(raw, home)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
}

func TestBadProjectsRootsAreRefused(t *testing.T) {
	home := tmpDir(t, "badhome")
	if err := os.WriteFile(filepath.Join(home, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "~user/code", "~other", "~/missing", "~/file", "~", "/", "."} {
		if _, err := ProjectsRoot(bad, home); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestAProjectsRootMustBeStrictlyBelowHome(t *testing.T) {
	base := tmpDir(t, "belowhome")
	home := filepath.Join(base, "home")
	for _, d := range []string{filepath.Join(home, "code"), filepath.Join(base, "elsewhere")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{base, filepath.Join(base, "elsewhere"), "/Users", "/tmp"} {
		if _, err := ProjectsRoot(bad, home); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
	if got, err := ProjectsRoot("~/code", home); err != nil || got != filepath.Join(home, "code") {
		t.Fatal(got, err)
	}
}

func TestTheCoordinatorMcpConfigIsPrivateAndCarriesItsBearer(t *testing.T) {
	paths := NewPaths(filepath.Join(t.TempDir(), "wiring"))
	if err := WriteCoordinatorMcp(paths, 48217, "tok"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, paths.CoordinatorMcp())
	if !strings.Contains(text, "Bearer tok") || !strings.Contains(text, "http://127.0.0.1:48217/mcp") {
		t.Fatal(text)
	}
	info, err := os.Stat(paths.CoordinatorMcp())
	if err != nil || info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatal(info.Mode(), err)
	}
}

// TerminalDir is the projects folder when usable, else home.
func TestTheTerminalDirIsTheProjectsFolderOrHome(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{
		"~/code": filepath.Join(home, "code"), "~/missing": home, "": home, "/etc": home, "~": home,
	} {
		if got := TerminalDir(raw, home); got != want {
			t.Fatalf("%q: %q, want %q", raw, got, want)
		}
	}
}
