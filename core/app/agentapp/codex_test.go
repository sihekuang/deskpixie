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
	// The renderings since sessions get asked about by description (pix, #89), byte for byte; with workers on, since #97 (create_project) and #126 (prefer an existing pix session over a worker), #141 (the app asks before a push or merge; no made-up confirm words), #143 (each session verbatim by default), #146 (match a session by its last turn too), and #153 (hand a request on: your session, then Pixie's worker, then yourself; edit a repo after asking), and #157 (several requests at once: read together, in order; Pixie's context is never an instruction), and #160 (a request is the person's own words: work out several things, their order and corrections), and #161 (pick the likeliest session yourself; ask only when you can't tell), and #164 (no Recall: requests: answer from memory, else hand it on) and #165 (a request handed to a session is done once sent), and #177 (a session in the same project gets it, questions too, before you look; the app says the hand-off, so the done block is short), and #183 (message_session may return waiting, with why), #187 (flush_session, or force, when they say send it anyway) #189 (a session on an older pix says so) and #237 (keep a worker that made something until the person is done with it; close a lookup), and #240 (`Attached:` lines are passed on unchanged), and #246 (a session tool can answer awaiting_confirmation), and #288 (open_settings shows a setting in the Settings window), and #320 (stop_and_send_session: stop a busy session and send what waits, on their say-so), and #373 (a forced send stops a busy session and goes past their typing).
	for _, c := range []struct{ root, sum string }{
		{"", "ead6d99f4ad602b33064024ce4e2502a7ad4de6f7feebe54d4a11187dc0ba632"},
		{"/Users/x/code", "0574a178331398faca70e7c98eaf87112a57321b79211c55f9ff83d4f1b1c4b9"},
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
	kept := agent.KeptAgents{CoordinatorToken: "t", CoordinatorStamp: "s", Workers: []agent.KeptWorker{{Token: "w"}}}
	if got := KeptFor(kept, agent.Claude, "s"); got.CoordinatorToken != "t" || len(got.Workers) != 1 {
		t.Fatalf("%+v", got)
	}
	if got := KeptFor(kept, agent.Codex, "s"); got.CoordinatorToken != "" || len(got.Workers) != 1 {
		t.Fatalf("%+v", got) // the workers are still this run's
	}
	kept.CoordinatorKind = agent.Codex
	if got := KeptFor(kept, agent.Codex, "s"); got.CoordinatorToken != "t" {
		t.Fatalf("%+v", got)
	}
}

// #189: a kept coordinator started with other instructions, settings or arguments than this run
// would start it with (or by an app from before stamps) is not adopted: its pane is replaced by a
// start that resumes its conversation.
func TestAKeptCoordinatorStartedDifferentlyIsNotAdopted(t *testing.T) {
	kept := agent.KeptAgents{CoordinatorToken: "t", CoordinatorStamp: "old", Workers: []agent.KeptWorker{{Token: "w"}}}
	if got := KeptFor(kept, agent.Claude, "new"); got.CoordinatorToken != "" || len(got.Workers) != 1 {
		t.Fatalf("%+v", got)
	}
	kept.CoordinatorStamp = ""
	if got := KeptFor(kept, agent.Claude, "new"); got.CoordinatorToken != "" {
		t.Fatalf("a kept file from before stamps: %+v", got)
	}
}

// The stamp changes with what the coordinator reads at its start (its instructions, its
// settings) and how it is started (kind, arguments), and only then.
func TestTheCoordinatorStampFollowsWhatItStartsWith(t *testing.T) {
	dir := t.TempDir()
	md, settings := filepath.Join(dir, "CLAUDE.md"), filepath.Join(dir, "settings.json")
	os.WriteFile(md, []byte("rules"), 0o644)
	os.WriteFile(settings, []byte("{}"), 0o644)
	recipe := agent.CoordinatorRecipe{Kind: agent.Claude, Args: []string{"--model", "opus"}}
	base := CoordinatorStamp(recipe, md, settings)
	if base == "" || CoordinatorStamp(recipe, md, settings) != base {
		t.Fatal("not stable", base)
	}
	changed := map[string]func(){
		"instructions": func() { os.WriteFile(md, []byte("new rules"), 0o644) },
		"settings":     func() { os.WriteFile(settings, []byte(`{"hooks":1}`), 0o644) },
		"arguments":    func() { recipe.Args = []string{"--model", "haiku"} },
		"kind":         func() { recipe.Kind = agent.Codex },
	}
	for _, what := range []string{"instructions", "settings", "arguments", "kind"} {
		changed[what]()
		if got := CoordinatorStamp(recipe, md, settings); got == base {
			t.Fatalf("%s changed, the stamp didn't", what)
		} else {
			base = got
		}
	}
	// The token isn't part of it: a kept coordinator keeps its token.
	recipe.Env = []agent.EnvVar{{Key: agent.TokenEnv, Value: "x"}}
	if CoordinatorStamp(recipe, md, settings) != base {
		t.Fatal("the env changed the stamp")
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
	// Then its effort, low by default (#177).
	if !slices.Equal(spec.Args[len(spec.Args)-4:], []string{"-m", "gpt-6-sol", "-c", `model_reasoning_effort="low"`}) {
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

func TestBundledPixIsBesideTheApp(t *testing.T) {
	if got := BundledPix("/Applications/DeskPixie.app/Contents/MacOS/DeskPixie"); got != "/Applications/DeskPixie.app/Contents/Helpers/pix" {
		t.Fatal(got)
	}
}
