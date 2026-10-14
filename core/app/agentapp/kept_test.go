package agentapp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

func TestKeptAgentsRoundTripPrivatelyAndAreReadOnce(t *testing.T) {
	path := NewPaths(t.TempDir()).Kept()
	want := agent.KeptAgents{
		CoordinatorToken: "c-tok",
		Workers: []agent.KeptWorker{{
			Plan:     agent.WorkerPlan{Name: "focus-upload", Slug: "upload", Kind: agent.Claude, Repo: "/p/focus", RepoName: "focus", Job: agent.JobIDFromBits(1), Task: "Fix it."},
			Worktree: "/p/focus/.worktrees/upload",
			Token:    "w-tok",
		}},
	}
	if err := KeptFile(path).Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	if got := LoadKept(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("not deleted after load", err)
	}
	if got := LoadKept(path); !reflect.DeepEqual(got, agent.KeptAgents{}) {
		t.Fatalf("missing: %+v", got)
	}
}

func TestAnUnreadableKeptFileKeepsNothingAndIsDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kept.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadKept(path); !reflect.DeepEqual(got, agent.KeptAgents{}) {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("not deleted", err)
	}
}
