package agent_test

import (
	"slices"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// #87: a Codex coordinator starts as kind codex, resumes with `codex resume --last`, and waits
// for its input box with no refusal screen before it counts as up.

const codexIdle = "╭──╮\n│ › Ask Codex to do anything │\n╰──╯\n  ? for shortcuts"

func codexHostRig() (*AgentHost, *agenttest.FakeSessionHost) {
	fake := agenttest.NewFakeSessionHost()
	recipe := CoordinatorRecipe{
		Kind:     Codex,
		Cwd:      "/data/agent/workspace",
		Args:     []string{"--no-daemon", "-s", "workspace-write"},
		Env:      []EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "t"}},
		Refusals: CodexCoordinatorRefusals("/data/agent/workspace"),
		Ready:    CodexCoordinatorReady(),
	}
	return NewAgentHost(fake, agenttest.ClockAt(1_000_000), recipe), fake
}

func TestACodexCoordinatorStartsAsCodexAndResumesWithResumeLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, fake := codexHostRig()
		fake.SetScreen(Coordinator, codexIdle)
		if h.Kind() != Codex {
			t.Fatal(h.Kind())
		}
		wantEnsured(t, h, started(false))
		first := fake.Starts()[0]
		if first.Kind != Codex || first.Name != Coordinator || !slices.Equal(first.Args, []string{"--no-daemon", "-s", "workspace-write"}) {
			t.Fatalf("%+v", first)
		}
		// A re-creation resumes the workspace's last session: the subcommand goes first.
		fake.Kill(Coordinator)
		wantEnsured(t, h, started(true))
		second := fake.Starts()[1]
		if !slices.Equal(second.Args, []string{"resume", "--last", "--no-daemon", "-s", "workspace-write"}) || slices.Contains(second.Args, "--continue") {
			t.Fatal(second.Args)
		}
	})
}

func TestAClaudeCoordinatorStillContinues(t *testing.T) {
	h, fake, _ := hostRig()
	if h.Kind() != Claude {
		t.Fatal(h.Kind())
	}
	h.EnsureCoordinator(bg)
	fake.Kill(Coordinator)
	wantEnsured(t, h, started(true))
	if got := fake.Starts()[1].Args; !slices.Equal(got, []string{"--permission-mode", "auto", "--continue"}) {
		t.Fatal(got)
	}
}

func TestTheHooksReviewScreenFailsTheStartAndClosesThePane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, fake := codexHostRig()
		fake.SetScreen(Coordinator, "  Hooks need review\n  2 hooks are new or changed.\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)")
		wantHostErr(t, h, HostError{Kind: HostRefused, Detail: CodexHooksReviewText})
		if !slices.Contains(fake.Closed(), Coordinator) {
			t.Fatal(fake.Closed())
		}
		if got := fake.Keys(); len(got) != 0 {
			t.Fatal("typed into the screen:", got)
		}
	})
}

func TestACodexCoordinatorThatNeverShowsItsInputBoxFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, fake := codexHostRig()
		fake.SetScreen(Coordinator, "Loading…\nSigning in")
		wantHostErr(t, h, HostError{Kind: HostRefused, Detail: CodexCoordinatorNotReady})
	})
}

func TestARefusedStartSaysWhyInTheTray(t *testing.T) {
	if got := (HostError{Kind: HostRefused, Detail: CodexHooksReviewText}).Error(); got != CodexHooksReviewText {
		t.Fatal(got)
	}
}
