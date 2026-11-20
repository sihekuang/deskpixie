package agent

import (
	"regexp"
	"strings"
)

// A coordinator waiting at a prompt in its terminal (#459): which prompt it shows, read from
// its screen, and what to tell the user to do about it. Claude Code's and Codex's.

// CoordinatorPrompt is the prompt a blocked coordinator shows.
type CoordinatorPrompt string

const (
	// PromptUnknown: none this knows (or the screen couldn't be read).
	PromptUnknown CoordinatorPrompt = ""
	// PromptTrust: whether to trust its folder (Claude Code's "Quick safety check", Codex's
	// "Trust this folder?").
	PromptTrust CoordinatorPrompt = "trust"
	// PromptAddDir: Claude Code's "Adding a new directory" for an --add-dir it hasn't seen (#457).
	PromptAddDir CoordinatorPrompt = "add-dir"
	// PromptHooks: Codex's "Hooks need review".
	PromptHooks CoordinatorPrompt = "hooks"
	// PromptUpdate: Codex's "Update available".
	PromptUpdate CoordinatorPrompt = "update"
	// PromptPermission: it asks to run or change something ("Do you want to…", "Would you like
	// to run…") with no hook request behind it.
	PromptPermission CoordinatorPrompt = "permission"
	// PromptMenu: another picker.
	PromptMenu CoordinatorPrompt = "menu"
)

// codexPickerFoot is the key line under a Codex picker ("enter continue · esc skip").
var codexPickerFoot = regexp.MustCompile(`^enter \S.* · esc \S+$`)

// ClassifyCoordinatorPrompt is the prompt the bottom of a kind coordinator's screen shows.
func ClassifyCoordinatorPrompt(kind AgentKind, screen string) CoordinatorPrompt {
	bottom := ScreenBottom(screen, ScreenBottomLines)
	if len(bottom) == 0 {
		return PromptUnknown
	}
	text := strings.Join(bottom, "\n")
	last := strings.TrimSpace(bottom[len(bottom)-1])
	has := func(marks ...string) bool {
		for _, m := range marks {
			if !strings.Contains(text, m) {
				return false
			}
		}
		return true
	}
	if kind == Codex {
		switch {
		case ShowsAnyScreen(screen, CodexHooksReviewScreens):
			return PromptHooks
		case ShowsCodexTrust(screen):
			return PromptTrust
		case has("Update available · "):
			return PromptUpdate
		case has("Would you like to"):
			return PromptPermission
		case codexPickerFoot.MatchString(last), strings.Contains(last, "esc back") && strings.Contains(last, "enter open"):
			return PromptMenu
		}
		return PromptUnknown
	}
	switch {
	case has("Adding a new directory", "Yes, add it"):
		return PromptAddDir
	case has("Yes, I trust this folder"), has("Quick safety check"):
		return PromptTrust
	case has("Do you want to", "Esc to cancel"):
		return PromptPermission
	case has("Enter to confirm"), strings.HasSuffix(last, "again to exit"):
		return PromptMenu
	}
	return PromptUnknown
}

// CoordinatorPromptGuide is what to tell the user about a kind coordinator waiting at prompt:
// which prompt it is and what to do there. The coordinator's folders are DeskPixie's own, so
// trusting them is what to pick; anything else is the user's call.
func CoordinatorPromptGuide(kind AgentKind, prompt CoordinatorPrompt) string {
	name := "Claude Code"
	if kind == Codex {
		name = "Codex"
	}
	switch prompt {
	case PromptTrust:
		pick := "Yes, I trust this folder"
		if kind == Codex {
			pick = "Trust and continue"
		}
		return name + " is asking whether to trust the coordinator's folder. It's DeskPixie's own folder: choose \"" + pick + "\"."
	case PromptAddDir:
		return name + " is asking to add a folder to the coordinator. If it's DeskPixie's canvas folder, choose \"Yes, add it\"."
	case PromptHooks:
		return name + " wants you to review its hooks. Choose what to trust there, and the coordinator goes on."
	case PromptUpdate:
		return name + " is asking whether to update. Answer it there, and the coordinator goes on."
	case PromptPermission:
		return name + " is asking permission for something. Read it there and answer."
	case PromptMenu:
		return name + " is showing a menu. Pick an option or press Esc, and the coordinator goes on."
	}
	return "The coordinator is waiting at a prompt in its terminal."
}

// CoordinatorPromptBrief is the bubble's few words for it.
func CoordinatorPromptBrief(prompt CoordinatorPrompt) string {
	return map[CoordinatorPrompt]string{
		PromptTrust:      "Asking to trust its folder.",
		PromptAddDir:     "Asking to add a folder.",
		PromptHooks:      "Asking you to review its hooks.",
		PromptUpdate:     "Asking whether to update.",
		PromptPermission: "Asking permission.",
		PromptMenu:       "Showing a menu.",
	}[prompt]
}
