package agent

import (
	"fmt"
	"strings"
	"testing"
)

// Screens as Claude Code 2.1.329 and Codex 0.162 draw them, transcript above (#459).

const claudeAddDirScreen = `⏺ Done earlier.
────────────────────────────────────────────────────────────
 Adding a new directory:

 /Users/me/Library/Application Support/com.sihekuang.deskpixie/canvas

 This session hasn't worked here before. Is this a directory you created or one you trust?

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, add it

 Enter to confirm · Esc to cancel`

const claudeTrustScreen = ` Quick safety check: Is this a project you created or one you trust?

 Claude Code'll be able to read, edit, and execute files here.

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel`

const claudePermissionScreen = ` Bash command

   rm -rf build

 Do you want to proceed?
 ❯ 1. Yes
   2. No, and tell Claude what to do differently (esc)

 Esc to cancel · Tab to amend`

const claudeModelMenuScreen = ` Select model
 ❯ 1. Default (recommended)
   2. Opus
   3. Haiku

 Enter to confirm · Esc to exit`

const claudeIdleScreen = `⏺ OK
✻ Brewed for 1s
────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)`

const codexTrustPromptScreen = `> You are in /Users/me/Library/Application Support/com.sihekuang.deskpixie/agent/workspace

  Trust this folder?

› 1. Trust and continue
  2. Quit

  enter continue · esc quit`

const codexHooksScreen = `  Hooks need review

  2 hooks changed since you last trusted them.

› 1. Trust all and continue
  2. Review in /hooks

  enter continue · esc quit`

const codexUpdateScreen = `  ✨ Update available · 0.162.0 -> 0.163.0

› 1. Update now
  2. Skip
  3. Skip until next version

  enter continue · esc skip`

const codexApprovalScreen = `  Would you like to run the following command?

  $ rm -rf build

› 1. Yes, proceed (y)
  2. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel`

// codexAskingScreen: a reply that ends with a question, at a Codex prompt.
const codexAskingScreen = `• I updated the parser.

  Would you like to also update the tests? Press enter to send.

╭──╮
│ › Ask Codex to do anything │
╰──╯
  ? for shortcuts`

// claudeCtrlCScreen: one Ctrl-C at Claude Code's prompt.
const claudeCtrlCScreen = `────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────
  Press Ctrl-C again to exit`

const codexAgentsScreen = `  Agent command center
› explorer   running
  reviewer   idle
  esc back · enter open`

func TestClaudeCodesCoordinatorPromptsAreTold(t *testing.T) {
	for screen, want := range map[string]CoordinatorPrompt{
		claudeAddDirScreen:     PromptAddDir,
		claudeTrustScreen:      PromptTrust,
		claudePermissionScreen: PromptPermission,
		claudeModelMenuScreen:  PromptMenu,
		claudeIdleScreen:       PromptUnknown,
		claudeCtrlCScreen:      PromptUnknown, // not a menu: Esc won't help
		"":                     PromptUnknown,
		// Codex's screens mean nothing from Claude Code.
		codexUpdateScreen: PromptUnknown,
	} {
		if got := ClassifyCoordinatorPrompt(Claude, screen); got != want {
			t.Errorf("%q for\n%s", got, screen)
		}
	}
}

func TestCodexsCoordinatorPromptsAreTold(t *testing.T) {
	for screen, want := range map[string]CoordinatorPrompt{
		codexTrustPromptScreen: PromptTrust,
		codexTrustScreen:       PromptTrust, // as a live run showed it
		codexHooksScreen:       PromptHooks,
		codexUpdateScreen:      PromptUpdate,
		codexApprovalScreen:    PromptPermission,
		codexAgentsScreen:      PromptMenu,
		codexIdleScreenText:    PromptUnknown,
		codexAskingScreen:      PromptUnknown, // its reply asks, not an approval
		"":                     PromptUnknown,
		claudeAddDirScreen:     PromptUnknown,
	} {
		if got := ClassifyCoordinatorPrompt(Codex, screen); got != want {
			t.Errorf("%q for\n%s", got, screen)
		}
	}
}

const codexIdleScreenText = "╭──╮\n│ › Ask Codex to do anything │\n╰──╯\n  ? for shortcuts"

func TestAPromptHighInTheTranscriptIsNotTheOneShown(t *testing.T) {
	// The trust dialog answered long ago scrolled up: only the bottom of the screen counts.
	screen := claudeTrustScreen + strings.Repeat("\n⏺ a line of the transcript", ScreenBottomLines) + "\n" + claudeIdleScreen
	if got := ClassifyCoordinatorPrompt(Claude, screen); got != PromptUnknown {
		t.Fatal(got)
	}
}

func TestEachPromptsGuideSaysWhatToPick(t *testing.T) {
	for _, c := range []struct {
		kind   AgentKind
		prompt CoordinatorPrompt
		want   string
	}{
		{Claude, PromptTrust, `choose "Yes, I trust this folder"`},
		{Codex, PromptTrust, `choose "Trust and continue"`},
		{Claude, PromptAddDir, `choose "Yes, add it"`},
		{Codex, PromptHooks, "Codex wants you to review its hooks"},
		{Codex, PromptUpdate, "Codex is asking whether to update"},
		{Claude, PromptPermission, "Claude Code is asking permission"},
		{Codex, PromptMenu, "Codex is showing a menu"},
		{Claude, PromptUnknown, "waiting at a prompt in its terminal"},
	} {
		if got := CoordinatorPromptGuide(c.kind, c.prompt); !strings.Contains(got, c.want) {
			t.Errorf("%v %q: %s", c.kind, c.prompt, got)
		}
		if c.prompt != PromptUnknown && CoordinatorPromptBrief(c.prompt) == "" {
			t.Errorf("no brief for %q", c.prompt)
		}
	}
}

// blockedAtPrompt: a coordinator of kind, up and idle, then at a prompt in its terminal long
// enough to be hinted, its screen not read back yet. Claude's reports its prompts.
func blockedAtPrompt(t *testing.T, kind AgentKind) *AgentController {
	t.Helper()
	c := ctlWith(func(s *Settings) { s.CoordinatorKind, s.ReportsSubmits = kind, kind == Claude })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(coordStatus(PaneBlocked), T0)
	wantContains(t, c.Step(TickInput(), T0+BlockedGraceMs), Effect{Kind: EfReadCoordinator})
	return c
}

const addDirGuide = "Claude Code is asking to add a folder to the coordinator. If it's DeskPixie's canvas folder, choose \"Yes, add it\". I'm bringing it up for you."

func TestTheUserIsToldWhichPromptTheCoordinatorWaitsAt(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	fx := visible(c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100))
	wantEffects(t, fx, eChime, eNotify(addDirGuide))
}

func TestARequestWaitingAtThePromptIsToldAndClosedOnceItLands(t *testing.T) {
	// 2026-11-20 11:03: asked while Claude Code asked to add the canvas; Pixie only "thought".
	c := blockedAtPrompt(t, Claude)
	fx := visible(c.Step(DelegateInput(job(1), "summarise the issues", ""), T0+3_050))
	// Held: it's at a prompt. Not told yet: the guidance, a moment later, says it.
	wantPrompts(t, fx)
	wantNotContains(t, fx, eNotify(heldAtPromptText))
	fx = visible(c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100))
	wantEffects(t, fx, eChime, eNotify(addDirGuide+" Your request goes through once you've answered."))
	// Answered: the request goes out, and once it's taken the user hears it went through.
	wantPrompts(t, c.Step(coordStatus(PaneIdle), T0+20_000), firstPrompt)
	fx = visible(submit(c, firstPrompt, T0+20_500))
	wantContains(t, fx, eNotify(pastPromptText))
	// Once.
	stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T0+30_000)
	c.Step(DelegateInput(job(2), "second", ""), T0+40_000)
	wantNotContains(t, submit(c, secondPrompt, T0+40_500), eNotify(pastPromptText))
}

func TestARequestMadeAfterTheHintSaysItWaits(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100)
	fx := visible(c.Step(DelegateInput(job(1), "summarise the issues", ""), T0+5_000))
	wantContains(t, fx, eNotify(heldAtPromptText))
}

func TestARequestBeforeAnyPromptSaysNothingExtra(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.ReportsSubmits = true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	fx := visible(c.Step(DelegateInput(job(1), "summarise the issues", ""), T0))
	wantNotContains(t, fx, eNotify(heldAtPromptText))
	wantNotContains(t, submit(c, firstPrompt, T0+500), eNotify(pastPromptText))
}

func TestACodexCoordinatorsPromptIsToldAndItsHeldRequestClosedOnceSent(t *testing.T) {
	// Codex reports no prompts here: typed is as sure as it gets.
	c := blockedAtPrompt(t, Codex)
	c.Step(DelegateInput(job(1), "summarise the issues", ""), T0+3_050)
	fx := visible(c.Step(CoordinatorScreenInput(codexUpdateScreen), T0+3_100))
	wantEffects(t, fx, eChime, eNotify("Codex is asking whether to update. Answer it there, and the coordinator goes on. I'm bringing it up for you. Your request goes through once you've answered."))
	fx = visible(c.Step(coordStatus(PaneIdle), T0+20_000))
	wantPrompts(t, fx, firstPrompt)
	wantContains(t, fx, eNotify(pastPromptText))
}

func TestAScreenReadAfterThePromptWentSaysNothing(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	c.Step(coordStatus(PaneIdle), T0+3_050)
	wantNone(t, visible(c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100)))
}

func TestARestartForgetsARequestHeldAtThePrompt(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	c.Step(DelegateInput(job(1), "summarise the issues", ""), T0+3_050)
	c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100)
	c.Step(paneExited(Coordinator), T0+4_000)
	c.Step(CoordinatorUpInput(PaneIdle), T0+10_000)
	wantNotContains(t, submit(c, firstPrompt, T0+10_500), eNotify(pastPromptText))
}

func TestARequestTypedJustAsThePromptCameIsHeldAndClosed(t *testing.T) {
	// #456 itself: typed, not yet landed, when Claude Code put its prompt up.
	c := ctlWith(func(s *Settings) { s.ReportsSubmits = true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	wantPrompts(t, c.Step(DelegateInput(job(1), "summarise the issues", ""), T0), firstPrompt)
	c.Step(coordStatus(PaneBlocked), T0+100)
	c.Step(TickInput(), T0+100+BlockedGraceMs)
	fx := visible(c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_200))
	wantEffects(t, fx, eChime, eNotify(addDirGuide+" Your request goes through once you've answered."))
	c.Step(coordStatus(PaneIdle), T0+5_000)
	wantContains(t, visible(submit(c, firstPrompt, T0+5_100)), eNotify(pastPromptText))
}

func TestACancelledHeldRequestIsNotSaidToGoThroughLater(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	c.Step(DelegateInput(job(1), "summarise the issues", ""), T0+3_050)
	c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100)
	c.Step(CancelInput(job(1)), T0+4_000)
	c.Step(coordStatus(PaneIdle), T0+10_000)
	// Much later, another request goes through: nothing about a prompt.
	c.Step(DelegateInput(job(2), "second", ""), T0+600_000)
	wantNotContains(t, submit(c, secondPrompt, T0+600_500), eNotify(pastPromptText))
}

func TestATypedRequestHearsItWentThroughInItsBox(t *testing.T) {
	c := blockedAtPrompt(t, Claude)
	c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+3_100)
	fx := c.Step(TypedDelegateInput(job(1), "summarise the issues", ""), T0+4_000)
	wantChatSays(t, fx, heldAtPromptText)
	c.Step(coordStatus(PaneIdle), T0+10_000)
	fx = submit(c, firstPrompt, T0+10_500)
	wantChatSays(t, fx, pastPromptText)
	wantNotContains(t, fx, eNotify(pastPromptText))
}

func TestAPermissionRequestComingFirstLeavesNoPromptGuidance(t *testing.T) {
	// The Allow bubble says it: no second "asking permission" from the screen.
	c := blockedAtPrompt(t, Claude)
	askPush(c, 7, T0+3_050)
	wantNotContains(t, visible(c.Step(CoordinatorScreenInput(claudePermissionScreen), T0+3_100)), eNotify(CoordinatorPromptGuide(Claude, PromptPermission)+" I'm bringing it up for you."))
}

func TestOnlyTheFirstScreenReadIsSaid(t *testing.T) {
	// A late read after the timed-out one's generic hint: said once.
	c := blockedAtPrompt(t, Claude)
	wantContains(t, visible(c.Step(CoordinatorScreenInput(""), T0+3_100)), eNotify(BlockedHint))
	wantNone(t, visible(c.Step(CoordinatorScreenInput(claudeAddDirScreen), T0+20_000)))
}

// wantChatSays: fx has a chat line (the text box) whose text has want.
func wantChatSays(t *testing.T, fx []Effect, want string) {
	t.Helper()
	for _, e := range fx {
		if e.Kind == EfChat && strings.Contains(fmt.Sprint(e.Chat), want) {
			return
		}
	}
	t.Fatalf("no chat line with %q in %v", want, fx)
}
