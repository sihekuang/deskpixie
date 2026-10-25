package pix

import (
	"regexp"
	"strings"
	"time"
)

// Menus an agent shows in place of its prompt (#306): Codex's update, folder and hooks
// questions at startup, Claude Code's folder trust. Enter there picks the highlighted option
// (pix's Enter once ran Codex's update), and only you should answer them: pix types nothing
// while one shows, and says which one it waits for.

// codexMenuFoot is the key line under a Codex picker ("enter continue · esc skip"). It is the
// last line on the screen; at its prompt Codex shows its own footer there instead.
var codexMenuFoot = regexp.MustCompile(`^enter \S.* · esc \S+$`)

// menuOn is the menu lines show in place of tool's prompt: "update", "trust" (the folder),
// "hooks", "menu" (another one), or "" for none.
func menuOn(tool string, lines []string) string {
	var rows []string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			rows = append(rows, t)
		}
	}
	switch {
	case len(rows) == 0:
	case tool == "codex" && codexMenuFoot.MatchString(rows[len(rows)-1]):
		for _, t := range rows {
			switch {
			case strings.HasPrefix(t, "Update available · "):
				return "update"
			case t == "Hooks need review":
				return "hooks"
			case t == "Folder access":
				return "trust"
			}
		}
		return "menu"
	case tool == "claude":
		// Its dialog's question and its first option as an option, not quoted in a transcript.
		ask, yes := false, false
		for _, t := range rows {
			ask = ask || strings.HasPrefix(t, "Quick safety check:")
			yes = yes || strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, "❯")), "1. Yes, I trust this folder")
		}
		if ask && yes {
			return "trust"
		}
	}
	return ""
}

// menuSettle is how long the screen must show no menu, after the agent starts or a menu
// leaves, before pix types: Codex draws its prompt first and a startup menu over it a moment
// later (0.2-1 s seen), and one menu may follow another. A session keeps the one it started with.
var menuSettle = 3 * time.Second

// menuHold is why pix can't type now for the agent's menus: the menu on screen, "starting"
// while one may still be drawn, or "" when it can.
func (s *Session) menuHold() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.menu != "":
		return s.menu
	case s.now().Sub(s.menuAt) < s.settle:
		return "starting"
	}
	return ""
}

// menuShown is the menu on screen now ("" for none).
func (s *Session) menuShown() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.menu
}

// menuUp: a menu showed between pix's paste and its Enter, so the message wasn't sent.
type menuUp struct{ menu string }

func (m menuUp) Error() string { return "a menu showed before Enter: " + m.menu }

// menuWaits says, for the coordinator to pass on, which menu tool waits on you for.
func menuWaits(tool, menu string) string {
	switch menu {
	case "update":
		return tool + " is waiting for you to answer its update question."
	case "trust":
		return tool + " is waiting for you to say whether to trust its folder."
	case "hooks":
		return tool + " is waiting for you to say whether to trust its hooks."
	}
	return tool + " is showing a menu only you should answer."
}

// noteMenu keeps the menu lines show (PTY reader goroutine only). Keys you pressed while it
// showed answered it, so once it leaves nothing of yours is left in the prompt.
func (s *Session) noteMenu(lines []string) {
	menu := menuOn(s.o.tool, lines)
	s.mu.Lock()
	was := s.menu
	s.menu = menu
	if menu != "" {
		s.menuAt = s.now()
	}
	s.mu.Unlock()
	if menu == was {
		return
	}
	if menu == "" {
		s.o.log.Printf("menu %s answered", was)
		s.gate.Sent()
		return
	}
	s.o.log.Printf("menu %s on screen: holding messages", menu)
}
