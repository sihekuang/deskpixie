package agentapp

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

// #87: Codex as the coordinator, on the app side.

func TestClaudesCoordinatorMdIsUnchanged(t *testing.T) {
	// The renderings since sessions get asked about by description (pix, #89), byte for byte; with workers on, since #97 (create_project) and #126 (prefer an existing pix session over a worker), #141 (the app asks before a push or merge; no made-up confirm words), #143 (each session verbatim by default), #146 (match a session by its last turn too), and #153 (hand a session's work and questions about it to that session).
	for _, c := range []struct{ root, sum string }{
		{"", "b07c8c4999ceaabe81c7ec18e6e79e6a26ce7bb50f2004399db2249543dea60f"},
		{"/Users/x/code", "d06dab5967cd8fcbeba55878bf4e3168589f03b4075df4efd6032f005e025fa4"},
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(RenderClaudeMd("/data/canvas", c.root)))); got != c.sum {
			t.Fatal(c.root, got)
		}
	}
}

func TestCodexGetsTheSamePromptAsAgentsMdWithItsOwnAskRule(t *testing.T) {
	for _, root := range []string{"", "/Users/x/code"} {
		claude, codex := RenderClaudeMd("/data/canvas", root), RenderAgentsMd("/data/canvas", root)
		if strings.Contains(codex, "AskUserQuestion") || !strings.Contains(codex, "request_user_input") || strings.Contains(codex, "{{") {
			t.Fatal(codex)
		}
		if strings.ReplaceAll(codex, CodexAskRule, ClaudeAskRule) != claude {
			t.Fatal("the prompts differ beyond the ask rule")
		}
	}
}

func TestInstallWritesAgentsMdBesideClaudeMd(t *testing.T) {
	paths := NewPaths(filepath.Join(t.TempDir(), "install"))
	if err := Install(paths, 48217, ""); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(paths.Workspace, "AGENTS.md")) != RenderAgentsMd(paths.Canvas, "") {
		t.Fatal("AGENTS.md")
	}
}

func TestTheCoordinatorRecipeFollowsTheKind(t *testing.T) {
	paths := NewPaths("/data")
	claude := CoordinatorRecipeFor(agent.Claude, paths, "opus", "", 48217, false)
	if claude.Kind != agent.Claude || !slices.Equal(claude.Args, CoordinatorArgs(paths, "opus", true)) || claude.Refusals != nil || claude.Ready != nil {
		t.Fatalf("%+v", claude)
	}
	codex := CoordinatorRecipeFor(agent.Codex, paths, "opus", "gpt-6-sol", 48217, true)
	if codex.Kind != agent.Codex || !slices.Equal(codex.Args, agent.CodexCoordinatorArgs(paths.Canvas, 48217, "gpt-6-sol", true)) {
		t.Fatalf("%+v", codex)
	}
	if len(codex.Refusals) != 2 || codex.Ready == nil || codex.Ready.Marker != agent.CodexIdleMarker {
		t.Fatalf("%+v", codex)
	}
	if !strings.Contains(codex.Refusals[1].Reason, paths.Workspace) {
		t.Fatal(codex.Refusals[1].Reason)
	}
}

func TestAKeptCoordinatorOfAnotherKindIsNotAdopted(t *testing.T) {
	kept := agent.KeptAgents{CoordinatorToken: "t", Workers: []agent.KeptWorker{{Token: "w"}}}
	if got := KeptFor(kept, agent.Claude); got.CoordinatorToken != "t" || len(got.Workers) != 1 {
		t.Fatalf("%+v", got)
	}
	if got := KeptFor(kept, agent.Codex); got.CoordinatorToken != "" || len(got.Workers) != 1 {
		t.Fatalf("%+v", got) // the workers are still this run's
	}
	kept.CoordinatorKind = agent.Codex
	if got := KeptFor(kept, agent.Codex); got.CoordinatorToken != "t" {
		t.Fatalf("%+v", got)
	}
}

func TestCoordinatorKindParsesTheSetting(t *testing.T) {
	for in, want := range map[string]agent.AgentKind{"claude": agent.Claude, "codex": agent.Codex, "": agent.Claude, "gemini": agent.Claude} {
		if got := CoordinatorKind(in); got != want {
			t.Fatal(in, got)
		}
	}
}

const codexIdleScreen = "╭──╮\n│ › Ask Codex to do anything │\n╰──╯\n  ? for shortcuts"

func TestACodexCoordinatorIsBuiltFromTheSetting(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Settings.Warm = true
		c.Settings.CoordinatorKind = "codex"
		m := "gpt-6-sol"
		c.Settings.CodexModel = &m
	})
	f.host.SetScreen(agent.Coordinator, codexIdleScreen)
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	spec := f.host.Starts()[0]
	// It resumes the workspace's last session, like Claude's --continue (Codex starts a new one
	// when there is none).
	if spec.Kind != agent.Codex || spec.Cwd != NewPaths(f.data).Workspace || !slices.Equal(spec.Args[:3], []string{"resume", "--last", "--no-daemon"}) {
		t.Fatalf("%+v", spec)
	}
	if !slices.Equal(spec.Args[len(spec.Args)-2:], []string{"-m", "gpt-6-sol"}) {
		t.Fatal(spec.Args)
	}
	// Herdr detects status itself: no state hooks; the bearer only in the env.
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "UserPromptSubmit") || strings.Contains(joined, f.coordinatorToken(t)) {
		t.Fatal(joined)
	}
	if _, err := os.Stat(NewPaths(f.data).CoordinatorMcp()); !os.IsNotExist(err) {
		t.Fatal("a Codex coordinator needs no MCP config file:", err)
	}
	if !slices.Equal(spec.Env, []agent.EnvVar{{Key: agent.TokenEnv, Value: f.coordinatorToken(t)}}) {
		t.Fatal(spec.Env)
	}
}

func TestAKeptClaudeCoordinatorIsReplacedByACodexOne(t *testing.T) {
	f := newFixture(t, nil)
	f.agent.Menu("agent:open")
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	f.agent.Shutdown()
	next := newFixture(t, func(c *Config) {
		c.AppSupport = f.data
		c.Settings.CoordinatorKind = "codex"
		c.Hosts = map[string]HostFactory{"herdr": func(HostDeps) (agent.SessionHost, error) { return f.host, nil }}
	})
	f.host.SetScreen(agent.Coordinator, codexIdleScreen)
	next.agent.Menu("agent:open")
	waitFor(t, "the Codex coordinator", func() bool { return len(f.host.Starts()) == 2 })
	if got := f.host.Starts()[1]; got.Kind != agent.Codex || got.Args[0] != "resume" {
		t.Fatalf("%+v", got) // it resumes the workspace's last Codex session, if any
	}
	if !slices.Contains(f.host.Closed(), agent.Coordinator) {
		t.Fatal("the Claude pane was not replaced", f.host.Closed())
	}
}
