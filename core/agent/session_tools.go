package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SessionTools are the coordinator's tools for the sessions you start yourself with pix
// (#89). They are available whether or not workers are on.
var SessionTools = [5]string{"list_sessions", "message_session", "interrupt_session", "close_session", "describe_session"}

// IsSessionTool reports whether name is one of SessionTools.
func IsSessionTool(name string) bool { return slices.Contains(SessionTools[:], name) }

// SessionCatalog is the session tools' `tools/list` entries.
func SessionCatalog() []map[string]any {
	name := [2]string{"name", "The session's name from list_sessions."}
	return []map[string]any{
		{
			"name":        "list_sessions",
			"description": "Claude Code and Codex sessions the user started themselves with pix (in WebStorm, iTerm, …): name, description, past descriptions, tool, repo, folder, where, status, when it started and was last active. Match what the user says to a description; if several fit, ask which one by description (and where it runs or when it started), never by name.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name":        "message_session",
			"description": "Send a message to a session the user started. It arrives as if the user typed it, when the session can take it. Only a reply with status \"sent\" means it was delivered.",
			"inputSchema": objectSchema(stringProps(name, [2]string{"text", "The message."}), "name", "text"),
		},
		{
			"name":        "interrupt_session",
			"description": "Interrupt a session the user started, like pressing Esc. It keeps running.",
			"inputSchema": objectSchema(stringProps(name), "name"),
		},
		{
			"name":        "close_session",
			"description": "End a session the user started. Only when the user asked for it; the user is asked to confirm.",
			"inputSchema": objectSchema(stringProps(name), "name"),
		},
		{
			"name":        "describe_session",
			"description": "Set the user's own description of a session they started (\"call the WebStorm one login bug\"). Empty text clears it.",
			"inputSchema": objectSchema(stringProps(name, [2]string{"text", "A few words, or empty to clear."}), "name"),
		},
	}
}

// SessionRow is a session you started, as list_sessions shows it.
type SessionRow struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	PastDescriptions []string `json:"past_descriptions,omitempty"`
	Tool             string   `json:"tool"`
	Repo             string   `json:"repo,omitempty"`
	Cwd              string   `json:"cwd,omitempty"`
	Where            string   `json:"where"`
	Status           string   `json:"status"`
	Reachable        bool     `json:"reachable"`
	Started          string   `json:"started,omitempty"`     // "1h30m ago"
	LastActive       string   `json:"last_active,omitempty"` // "3m ago"
}

// ago is how long before now t was, as list_sessions shows it ("" for no time).
func ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t).Truncate(time.Minute)
	if d < time.Minute {
		return "just now"
	}
	return strings.TrimSuffix(d.String(), "0s") + " ago" // 1h30m0s → 1h30m
}

// Describer sets your own description of a session.
type Describer interface {
	SetDescription(name, text string) error
}

func isSessionCall(k McpCallKind) bool {
	switch k {
	case CallListSessions, CallMessageSession, CallInterruptSession, CallCloseSession, CallDescribeSession:
		return true
	}
	return false
}

// sessionTool runs a session tool on host, the CompositeHost; any other host has no sessions.
func sessionTool(ctx context.Context, host SessionHost, call McpCall) McpReply {
	c, ok := host.(*CompositeHost)
	if !ok {
		if call.Kind == CallListSessions {
			return McpReply{Value: map[string]any{"sessions": []SessionRow{}}}
		}
		return McpReply{Err: errors.New("Sessions the user starts with pix are not available.")}
	}
	sec := c.Secondary()
	if call.Kind == CallListSessions {
		list, err := sec.List(ctx)
		if err != nil {
			return McpReply{Err: err}
		}
		rows, now := []SessionRow{}, time.Now()
		list = slices.DeleteFunc(list, func(a AgentInfo) bool { return a.Origin != OriginUser || a.User == nil })
		// The same project's sessions next to each other, oldest first.
		slices.SortStableFunc(list, func(a, b AgentInfo) int {
			if a.User.Repo != b.User.Repo {
				return strings.Compare(a.User.Repo, b.User.Repo)
			}
			return a.User.Started.Compare(b.User.Started)
		})
		for _, a := range list {
			u := a.User
			rows = append(rows, SessionRow{Name: a.Name, Description: u.Description, PastDescriptions: u.PastDescriptions,
				Tool: u.Tool, Repo: u.Repo, Cwd: a.Cwd, Where: u.Where, Status: a.Status.String(), Reachable: u.Reachable,
				Started: ago(u.Started, now), LastActive: ago(u.LastActive, now)})
		}
		return McpReply{Value: map[string]any{"sessions": rows}}
	}
	if !sec.Owns(call.Name) {
		if list, err := c.Primary().List(ctx); err == nil && slices.ContainsFunc(list, func(a AgentInfo) bool { return a.Name == call.Name }) {
			return McpReply{Err: fmt.Errorf("%s is a worker; use the worker tools.", call.Name)}
		}
		return McpReply{Err: fmt.Errorf("No session %s.", call.Name)}
	}
	var err error
	status := ""
	switch call.Kind {
	case CallMessageSession:
		err, status = sec.Prompt(ctx, call.Name, call.Text), "sent"
	case CallInterruptSession:
		err, status = sec.SendKeys(ctx, call.Name, []string{"Escape"}), "interrupted"
	case CallCloseSession:
		err, status = sec.Close(ctx, call.Name), "closing"
	case CallDescribeSession:
		d, ok := sec.(Describer)
		if !ok {
			return McpReply{Err: errors.New("Descriptions are not available.")}
		}
		if err = d.SetDescription(call.Name, call.Text); err == nil {
			return McpReply{Value: map[string]any{"name": call.Name, "description": call.Text}}
		}
	}
	if err != nil {
		return McpReply{Err: err}
	}
	return McpReply{Value: map[string]any{"name": call.Name, "status": status}}
}
