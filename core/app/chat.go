package app

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/sihekuang/deskpixie/core/chat"
	"github.com/sihekuang/deskpixie/core/settings"
)

// The text box under Pixie (#221): chat.send, chat.stop and chat.mode, and chat.recipients,
// who a message in the chat window can go to on its own (#302); what you send joins the
// chat thread (thread.go, #302); the agent's lines come back as `chat.line` events, the mode as `replyMode`, a hotkey press in text mode as
// `chat.focus`.

// fromWindow is chat.send's `from` for the chat window (#302).
const fromWindow = "window"

// errNoCoordinator: chat.send with the agent off.
var errNoCoordinator = errors.New("The coordinator is off.")

func (a *App) registerChat(r Registrar) {
	r.Register("chat.send", func(args json.RawMessage) (any, error) {
		// The attachments waiting go with it: the core takes them, and the thread shows what it took.
		var in struct {
			Text string `json:"text"`
			// Session is the address of the one recipient chosen (#302): a session of yours or
			// one of Pixie's workers, which the coordinator is asked to tell. Absent: Pixie.
			Session *string `json:"session"`
			// From is "window" for the chat window, which has the @ list: only there does a
			// message go to one recipient by rule. The box under Pixie leaves it out and sends
			// as it always has.
			From string `json:"from"`
		}
		if err := decodeArgs("chat.send", args, &in); err != nil {
			return nil, err
		}
		if a.agent == nil {
			return nil, errNoCoordinator
		}
		agents, yours := rosterOf(a.agent.Roster())
		rs := chat.Recipients(agents, yours)
		// The session is named as the roster names it (in the prompt and in a refusal), else,
		// once the roster has let it go, as the thread last named it.
		label, kind := "", "session"
		if in.Session != nil && *in.Session != "" {
			label = a.sessionLabel(*in.Session)
			for _, r := range append(yours, agents...) {
				if r.Name == *in.Session && r.Label != "" {
					label = r.Label
					if r.Kind == "worker" {
						kind = "worker"
					}
					break
				}
			}
		}
		if in.From == fromWindow {
			// One recipient per message: refused here, so every shell's window gets the same rule.
			if err := chat.CheckOneRecipient(in.Text, in.Session, label, rs); err != nil {
				return nil, err
			}
		} else if in.Session != nil && *in.Session == "" {
			in.Session = nil // the box: no address, Pixie
		}
		if in.Session != nil {
			to := *in.Session
			// Always a new job: never taken for the answer to a question waiting in the box.
			sent, err := a.agent.ChatDelegate("Tell the " + label + " " + kind + ": " + in.Text)
			if err != nil {
				return nil, err
			}
			a.threadSent(sent.Job, in.Text, sent.Attached, to)
			return map[string]any{"job": sent.Job}, nil
		}
		sent, err := a.agent.ChatSend(in.Text)
		if err != nil {
			return nil, err
		}
		a.threadSent(sent.Job, in.Text, sent.Attached, "")
		if sent.Answered {
			a.threadSentAnswer(sent.Job, sent.Session, strings.TrimSpace(in.Text))
		}
		return map[string]any{"job": sent.Job}, nil
	})
	r.Register("chat.recipients", func(json.RawMessage) (any, error) {
		return map[string]any{"recipients": a.recipients()}, nil
	})
	r.Register("chat.stop", func(json.RawMessage) (any, error) {
		if a.agent != nil {
			a.agent.ChatStop()
		}
		return struct{}{}, nil
	})
	r.Register("chat.mode", func(json.RawMessage) (any, error) {
		cur := a.store.Current()
		return map[string]any{"mode": cur.ReplyMode, "alwaysShowCaption": cur.AlwaysShowCaption,
			"captionDismissSecs": cur.CaptionDismissSecs, "captionFadeSecs": cur.CaptionFadeSecs, "replyHistoryMins": cur.ReplyHistoryMins}, nil
	})
}

// applyReplyMode is a live replyMode change: the agent stops opening conversations for updates
// in text mode, an open voice conversation ends, and the shell redraws the toggle.
func (a *App) applyReplyMode(mode string) {
	text := mode == settings.ReplyText
	if a.agent != nil {
		a.agent.SetTextMode(text)
	}
	if text && a.rt != nil && a.rt.IsOpen() {
		a.rt.Hotkey() // ends it, as the hotkey would
	}
	a.emit("replyMode", map[string]any{"mode": mode})
}

// fieldsOf is v's JSON object as event fields (nil when it isn't one).
func fieldsOf(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// recipients are who a message can go to on its own now (#302), from the roster as it is.
func (a *App) recipients() []chat.Recipient {
	if a.agent == nil {
		return []chat.Recipient{}
	}
	return recipientsOf(a.agent.Roster())
}

// recipientsOf reads the roster (agent.RosterView, which this package doesn't import) by its
// JSON: the rows that can be told something.
func recipientsOf(view any) []chat.Recipient {
	return chat.Recipients(rosterOf(view))
}

// rosterOf is the roster's rows: Pixie's agents, and your sessions, reachable or not.
func rosterOf(view any) (agents, yours []chat.RosterRow) {
	var v struct {
		Agents []chat.RosterRow `json:"agents"`
		Yours  []chat.RosterRow `json:"yours"`
	}
	if b, err := json.Marshal(view); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v.Agents, v.Yours
}
