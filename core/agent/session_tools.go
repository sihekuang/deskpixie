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
var SessionTools = [8]string{"list_sessions", "message_session", "flush_session", "stop_and_send_session", "interrupt_session", "close_session", "describe_session", "set_session_verbatim"}

// IsSessionTool reports whether name is one of SessionTools.
func IsSessionTool(name string) bool { return slices.Contains(SessionTools[:], name) }

// SessionCatalog is the session tools' `tools/list` entries.
func SessionCatalog() []map[string]any {
	name := [2]string{"name", "The session's name from list_sessions."}
	return []map[string]any{
		{
			"name":        "list_sessions",
			"description": "Claude Code and Codex sessions the user started themselves with pix (in WebStorm, iTerm, …): name, description, past descriptions, what its last turn came to, tool, repo, folder, where, status, when it started and was last active. Match what the user says to a description, past description or last turn; if several fit, ask which one by description (and where it runs or when it started), never by name.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name":        "message_session",
			"description": "Send a message to a session the user started, once the session can take it. It's typed in directly, never relayed through Claude Code's own cross-session messaging. A session with verbatim on (the default; list_sessions shows it) gets the text exactly as given, as if the user typed it, so send the user's own words when relaying what they said and never claim they approved something they didn't. With verbatim off it arrives prefixed \"Coordinator says: …\". Only a reply with status \"sent\" means it was delivered.",
			"inputSchema": objectSchema(map[string]any{
				"name":  map[string]any{"type": "string", "description": name[1]},
				"text":  map[string]any{"type": "string", "description": "The message. Quote the user's own words when relaying what they said."},
				"force": map[string]any{"type": "boolean", "description": "Only when the user says to send it anyway: it goes in now, with any messages still waiting: a busy session is stopped for it, and it goes in even while they're typing there (their unsent text comes back after). A question on screen still holds it until it's answered."},
			}, "name", "text"),
		},
		{
			"name":        "flush_session",
			"description": "Send a session's waiting messages in now, when the user says to send it anyway (\"push it through\", \"it's stuck\"): a busy session is stopped for them (its work in progress is cut short), and they go in even while the user is typing there, their unsent text set aside and put back. A question on screen still holds them until it's answered.",
			"inputSchema": objectSchema(stringProps(name), "name"),
		},
		{
			"name":        "stop_and_send_session",
			"description": "Stop a busy session's current turn (like Esc) so its waiting messages go in now, only when the user says to stop it and send (\"stop it and send it\"). The work in progress is cut short. They go in even while the user is typing there (their unsent text comes back after); only a question on screen holds them until it's answered.",
			"inputSchema": objectSchema(stringProps(name), "name"),
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
		{
			"name":        "set_session_verbatim",
			"description": "Turn verbatim on or off for a session the user started, when the user asks. On (the default): messages are typed exactly as given. Off: they arrive prefixed \"Coordinator says: …\".",
			"inputSchema": objectSchema(map[string]any{
				"name":     map[string]any{"type": "string", "description": name[1]},
				"verbatim": map[string]any{"type": "boolean", "description": "True to send messages as given, false to prefix them."},
			}, "name", "verbatim"),
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
	Verbatim         bool     `json:"verbatim"`              // messages are typed as given, not prefixed (#143)
	Started          string   `json:"started,omitempty"`     // "1h30m ago"
	LastActive       string   `json:"last_active,omitempty"` // "3m ago"
	LastTurn         string   `json:"last_turn,omitempty"`   // its last turn's gist, "Asked: …" for a question (#146)
	Waiting          int      `json:"waiting,omitempty"`     // messages waiting to be delivered to it (#183)
	Outdated         bool     `json:"outdated,omitempty"`    // it runs an older pix than the app's (#189)
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

// Delivery is what became of a message to your session (#183): Status "typed" (it went in at
// once) or "waiting" (pix holds it), with Reason why it waits: "working", "blocked" (on a
// question) or "typing" (you are); or a menu on its screen only you should answer (#306):
// "update", "trust" (the folder), "hooks", "agents" (Codex's list of agents), "menu" (another
// one), and "starting" while one may still show. Empty from a pix too old to say.
type Delivery struct {
	Status string
	Reason string
	// Outdated: the session runs another pix than the app's (#189).
	Outdated bool
	// Seq (#339): waiting, the number pix gave the message (or the last one waiting); typed,
	// every message up to it went in.
	Seq uint64
	// Run: the pix numbering Seq belongs to, which starts again at 1 when a new pix takes the
	// name. Set, a Seq of 0 means none went in yet; "" from a pix too old to number its
	// messages, whose 0 means all of them.
	Run string
}

// DeliveryPrompter prompts a session and says whether the message was typed or waits.
type DeliveryPrompter interface {
	PromptDelivery(ctx context.Context, name, text string) (Delivery, error)
}

// Flusher forces a session's waiting messages in now (#187, #373): a busy turn is stopped and
// they go past your typing. "typed", or still "waiting" with why (a question or a menu on screen,
// or a turn that hasn't stopped yet); they go in once it's out of the way. They start with the
// note for what was forced (#340).
type Flusher interface {
	Flush(ctx context.Context, name string, notes ForcedNotes) (Delivery, error)
}

// StopSender is the same forced send from stop_and_send_session (#320); a pix from before #373
// stops a busy turn for it but doesn't go past your typing.
type StopSender interface {
	StopAndSend(ctx context.Context, name string, notes ForcedNotes) (Delivery, error)
}

// ForcedNotes are what forced messages start with (#340): Typing when they went past your
// typing, Stopped when a busy turn was stopped for them; "" for none.
type ForcedNotes struct{ Typing, Stopped string }

// The notes a message forced into your session starts with, so its terminal shows what
// happened (#340).
const (
	StoppedNote = "[stopped and sent]"
	TypingNote  = "[sent while you were typing]"
)

// forcedNotes are the notes a forced send of call starts with; none with the setting off
// (call.Mark). pix adds one only when it does stop a turn or go past your typing.
func forcedNotes(call McpCall) ForcedNotes {
	if !call.Mark {
		return ForcedNotes{}
	}
	return ForcedNotes{Typing: TypingNote, Stopped: StoppedNote}
}

// deliveryReply is message_session's, flush_session's or stop_and_send_session's reply for d.
func deliveryReply(name string, d Delivery) McpReply {
	v := map[string]any{"name": name, "status": "sent"}
	if d.Status == "waiting" {
		v = map[string]any{"name": name, "status": "waiting", "reason": d.Reason, "why": WaitingWhy(d.Reason)}
	}
	if d.Outdated {
		v["outdated"], v["update"] = true, OutdatedUpdate
	}
	return McpReply{Value: v, Seq: d.Seq, Run: d.Run}
}

// OutdatedUpdate is what to tell the person about a session on an older pix (#189).
const OutdatedUpdate = "This session runs an older pix than the app: newer things (like flush_session) may not work there. " +
	"It updates when the person restarts it with pix and --resume (pix claude --resume, or pix codex resume)."

// WaitingWhy is why a waiting message waits, for the coordinator and Pixie.
func WaitingWhy(reason string) string {
	switch reason {
	case "working":
		return "The session is busy; it gets the message with its next step, or when it stops. If the person wants it now, stop_and_send_session can stop it and send."
	case "blocked":
		return "The session is waiting on a question; it gets the message once that's answered."
	case "typing":
		return "The person is typing in that session; it gets the message once they send or clear what they typed."
	case "stopping":
		return "The session was asked to stop and hasn't yet; it gets the message once it has. Don't stop it again."
	case "update":
		return "The session is waiting for the person to answer its update question on its screen; it gets the message once they have. Only the person should answer it."
	case "trust":
		return "The session is waiting for the person to say whether to trust its folder; it gets the message once they have. Only the person should answer it."
	case "hooks":
		return "The session is waiting for the person to say whether to trust its hooks; it gets the message once they have. Only the person should answer it."
	case "agents":
		return "The session is showing its list of agents (Codex's Agent command center); it gets the message once the person is back at its prompt. Only the person should leave it."
	case "menu":
		return "The session is showing a menu; it gets the message once the person closes it."
	case "starting":
		return "The session is still starting; it gets the message in a moment."
	}
	return "It gets the message as soon as it can."
}

// VerbatimSetter keeps each session's verbatim setting (#143): on, the default, a message is
// typed exactly as given; off, it is prefixed "Coordinator says: ".
type VerbatimSetter interface {
	Verbatim(name string) (bool, error)
	SetVerbatim(name string, on bool) error
}

func isSessionCall(k McpCallKind) bool {
	switch k {
	case CallListSessions, CallMessageSession, CallFlushSession, CallStopAndSendSession, CallInterruptSession, CallCloseSession, CallDescribeSession, CallSetSessionVerbatim:
		return true
	}
	return false
}

// sessionTool runs a session tool on host, the CompositeHost; any other host has no sessions.
// mask is applied to what sessions wrote (descriptions, last turns) before the coordinator
// reads it: it removes pending confirm words.
func sessionTool(ctx context.Context, host SessionHost, call McpCall, mask func(string) string) McpReply {
	c, ok := host.(*CompositeHost)
	if !ok {
		if call.Kind == CallListSessions {
			return McpReply{Value: map[string]any{"sessions": []SessionRow{}}}
		}
		return McpReply{Err: errors.New("Sessions the user starts with pix are not available.")}
	}
	sec := c.Secondary()
	if call.Kind == CallListSessions {
		rows, err := userSessionRows(ctx, sec, mask)
		if err != nil {
			return McpReply{Err: err}
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
		// Each session's own setting (#143) decides: verbatim, the default, types the text as
		// given; off, it is labeled "Coordinator says: ".
		text := call.Text
		if v, ok := sec.(VerbatimSetter); ok {
			on, verr := v.Verbatim(call.Name)
			if verr != nil {
				return McpReply{Err: verr}
			}
			if !on {
				text = CoordinatorSays(call.Text)
			}
		}
		if dp, ok := sec.(DeliveryPrompter); ok {
			d, derr := dp.PromptDelivery(ctx, call.Name, text)
			if derr != nil {
				return McpReply{Err: derr}
			}
			if f, ok := sec.(Flusher); ok && call.Force && d.Status == "waiting" {
				if d, derr = f.Flush(ctx, call.Name, forcedNotes(call)); derr != nil {
					return McpReply{Err: derr}
				}
			}
			return deliveryReply(call.Name, d)
		}
		err, status = sec.Prompt(ctx, call.Name, text), "sent"
	case CallFlushSession:
		f, ok := sec.(Flusher)
		if !ok {
			return McpReply{Err: errors.New("This session's pix can't send waiting messages in; restart it to update pix.")}
		}
		d, ferr := f.Flush(ctx, call.Name, forcedNotes(call))
		if ferr != nil {
			return McpReply{Err: ferr}
		}
		return deliveryReply(call.Name, d)
	case CallStopAndSendSession:
		st, ok := sec.(StopSender)
		if !ok {
			return McpReply{Err: errors.New("This session's pix can't stop it to send; restart it to update pix.")}
		}
		d, serr := st.StopAndSend(ctx, call.Name, forcedNotes(call))
		if serr != nil {
			return McpReply{Err: serr}
		}
		return deliveryReply(call.Name, d)
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
	case CallSetSessionVerbatim:
		v, ok := sec.(VerbatimSetter)
		if !ok {
			return McpReply{Err: errors.New("Verbatim settings are not available.")}
		}
		if err = v.SetVerbatim(call.Name, call.Verbatim); err == nil {
			return McpReply{Value: map[string]any{"name": call.Name, "verbatim": call.Verbatim}}
		}
	}
	if err != nil {
		return McpReply{Err: err}
	}
	return McpReply{Value: map[string]any{"name": call.Name, "status": status}}
}

// userSessionRows are the sessions you started with pix on sec, as list_sessions shows them.
func userSessionRows(ctx context.Context, sec SessionHost, mask func(string) string) ([]SessionRow, error) {
	list, err := sec.List(ctx)
	if err != nil {
		return nil, err
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
		past := make([]string, len(u.PastDescriptions))
		for i, d := range u.PastDescriptions {
			past[i] = mask(d)
		}
		last := mask(u.LastTurn)
		if last != "" && u.LastTurnAsks {
			last = "Asked: " + last
		}
		rows = append(rows, SessionRow{Name: a.Name, Description: mask(u.Description), PastDescriptions: past,
			Tool: u.Tool, Repo: u.Repo, Cwd: a.Cwd, Where: u.Where, Status: a.Status.String(), Reachable: u.Reachable, Verbatim: u.Verbatim,
			Started: ago(u.Started, now), LastActive: ago(u.LastActive, now), LastTurn: last, Waiting: u.Waiting, Outdated: u.Outdated})
	}
	return rows, nil
}

// RequestNote goes under a new request (#153): your open pix sessions, then Pixie's live workers
// with their job's task, then the order to hand the request on in, so the coordinator sees who
// is already on it without having to look. "" with neither open.
func RequestNote(sessions []SessionRow, workers []WorkerRow, task func(JobID) string) string {
	workers = slices.DeleteFunc(slices.Clone(workers), func(w WorkerRow) bool { return !w.Status.IsLive() })
	if len(sessions) == 0 && len(workers) == 0 {
		return ""
	}
	var b strings.Builder
	if len(sessions) > 0 {
		b.WriteString("Your open pix sessions:")
		for _, r := range sessions {
			fmt.Fprintf(&b, "\n- %s: %q, %s repo, %s", r.Name, r.Description, r.Repo, r.Status)
			if r.LastTurn != "" {
				fmt.Fprintf(&b, ", last turn %q", clipRunes(r.LastTurn, maxNoteTurn))
			}
		}
		b.WriteString("\n")
	}
	if len(workers) > 0 {
		b.WriteString("Pixie's workers:")
		for _, w := range workers {
			fmt.Fprintf(&b, "\n- %s: %s repo, %s, on %q", w.Name, w.Repo, w.Status, clipRunes(task(w.JobID), maxNoteTurn))
		}
		b.WriteString("\n")
	}
	b.WriteString("Hand this on in this order: a session working in the same project or repo, questions about it too (message_session), else a worker on it (message_worker), else do it yourself.")
	return b.String()
}

// maxNoteTurn: the most of a session's last turn SessionsNote quotes.
const maxNoteTurn = 160

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
