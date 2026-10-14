package httpserver

import (
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Constants copied verbatim from ai-mascot crates/agent-http/src/hooks.rs.
const (
	// MaxBody: hook bodies above this are refused (413). 1 MiB: a Stop hook carries the full
	// last assistant message and PostToolUse carries Write file contents, so a small cap would
	// lose replies. (hooks.rs MAX_BODY)
	MaxBody = 1024 * 1024
	// PermissionLimit is the longest a PermissionRequest waits here: past the app's 120 s
	// deny, under the hook's 125 s. (hooks.rs PERMISSION_LIMIT)
	PermissionLimit = 122 * time.Second
	// maxText: agent text kept from a hook (the rest is dropped). (hooks.rs MAX_TEXT)
	maxText = 8 * 1024
	// maxStopText: Stop messages keep their LAST this-many bytes: the coordinator ends with its
	// `<spoken>` block. (hooks.rs MAX_STOP_TEXT)
	maxStopText = 64 * 1024
	// maxToolInput: PostToolUse `tool_input` larger than this (serialised) is dropped to null.
	// (hooks.rs MAX_TOOL_INPUT)
	maxToolInput = 8 * 1024
)

// DecisionBody is the PermissionRequest answer Claude Code expects (always with status 200).
func DecisionBody(d agent.Decision) map[string]any {
	behavior := "deny"
	if d.Allow {
		behavior = "allow"
	}
	decision := map[string]any{"behavior": behavior}
	// Rust's Option<String>: "" means no message.
	if !d.Allow && d.Message != "" {
		decision["message"] = d.Message
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}}
}

func deny(w http.ResponseWriter, message string) {
	json200(w, DecisionBody(agent.Deny(message)))
}

// keep says whether c is kept from agent text: no control characters (except newline and tab)
// and no invisible Unicode format characters (zero-width, bidi overrides, BOM).
func keep(c rune) bool {
	if c == '\n' || c == '\t' {
		return true
	}
	if unicode.IsControl(c) { // Rust char::is_control: category Cc
		return false
	}
	switch {
	case c >= '\u200B' && c <= '\u200F', c >= '\u202A' && c <= '\u202E', c >= '\u2066' && c <= '\u2069', c == '\uFEFF':
		return false
	}
	return true
}

// clean is agent text as data: unsafe characters removed, length capped (head kept, maxText
// characters).
func clean(text string) string {
	var b strings.Builder
	n := 0
	for _, c := range text {
		if n == maxText {
			break
		}
		if keep(c) {
			b.WriteRune(c)
			n++
		}
	}
	return b.String()
}

// cleanTail is like clean, but keeps the tail: the last maxStopText bytes, cut on a character
// boundary.
func cleanTail(text string) string {
	kept := strings.Map(func(c rune) rune {
		if keep(c) {
			return c
		}
		return -1
	}, text)
	start := max(len(kept)-maxStopText, 0)
	for start < len(kept) && !utf8.RuneStart(kept[start]) {
		start++
	}
	return kept[start:]
}

// capped is tool_input as given, or nil when it serialises larger than maxToolInput.
func capped(v any) any {
	text, err := encodeJSON(v)
	if err != nil || len(text) > maxToolInput {
		return nil
	}
	return v
}

// stateOf is a state report's status: its "status" (working, idle or blocked), else the one
// its Claude Code hook stands for (UserPromptSubmit working, Stop idle, Notification blocked).
func stateOf(obj map[string]any) (agent.PaneStatus, bool) {
	if s, ok := obj["status"].(string); ok {
		switch st := agent.ParsePaneStatus(s); st {
		case agent.PaneWorking, agent.PaneIdle, agent.PaneBlocked:
			return st, true
		}
		return agent.PaneUnknown, false
	}
	switch obj["hook_event_name"] {
	case "UserPromptSubmit":
		return agent.PaneWorking, true
	case "Stop":
		return agent.PaneIdle, true
	case "Notification":
		return agent.PaneBlocked, true
	}
	return agent.PaneUnknown, false
}

func (s *Server) hook(w http.ResponseWriter, r *http.Request, event string) {
	// Every failure on the permission route is a 200 deny: Claude Code must never fall back to
	// its native prompt inside the unattended pane.
	permission := event == "permission"
	refuse := func(status int, message string) {
		if permission {
			deny(w, message)
		} else {
			w.WriteHeader(status)
		}
	}
	// A browser page cannot be a hook caller.
	if hasOrigin(r) {
		refuse(http.StatusForbidden, "Not authorised.")
		return
	}
	agentName, ok := s.bearer(r)
	if !ok {
		refuse(http.StatusUnauthorized, "Not authorised.")
		return
	}
	if event != "stop" && event != "permission" && event != "post-tool" && event != "state" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, tooLarge, err := readBody(w, r, MaxBody)
	if err != nil {
		if tooLarge {
			refuse(http.StatusRequestEntityTooLarge, "Request too large.")
		} else {
			refuse(http.StatusBadRequest, "Could not read the request.")
		}
		return
	}
	input, _ := decodeJSON(body)
	obj, _ := input.(map[string]any)
	switch event {
	case "stop":
		if message, ok := obj["last_assistant_message"].(string); ok {
			s.rt.Deliver(agent.HookEvent{Kind: agent.HookStop, Agent: agentName, Message: cleanTail(message)})
		}
		json200(w, map[string]any{})
	case "state":
		status, ok := stateOf(obj)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.rt.Deliver(agent.HookEvent{Kind: agent.HookState, Agent: agentName, Status: status})
		json200(w, map[string]any{})
	case "post-tool":
		if toolName, ok := obj["tool_name"].(string); ok {
			s.rt.Deliver(agent.HookEvent{Kind: agent.HookPostTool, Agent: agentName, ToolName: clean(toolName), ToolInput: capped(obj["tool_input"])})
		}
		json200(w, map[string]any{})
	default:
		toolName, okName := obj["tool_name"].(string)
		toolInput, okInput := obj["tool_input"]
		if !okName || !okInput {
			deny(w, "Malformed permission request.")
			return
		}
		reply := make(chan agent.Decision, 1)
		ev := agent.HookEvent{Kind: agent.HookPermission, Agent: agentName, ToolName: clean(toolName), ToolInput: toolInput, Reply: reply}
		if !s.rt.Deliver(ev) {
			deny(w, "The app is shutting down.")
			return
		}
		decision := agent.Deny("The app could not decide.")
		timer := time.NewTimer(s.permissionLimit)
		defer timer.Stop()
		select {
		case d, ok := <-reply:
			if ok {
				decision = d
			}
		case <-timer.C:
		case <-r.Context().Done():
			// The caller hung up (Rust drops the handler future); nobody reads the answer.
		}
		json200(w, DecisionBody(decision))
	}
}
