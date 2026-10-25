package app

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/sihekuang/deskpixie/core/settings"
)

// The text box under Pixie (#221): chat.send, chat.stop and chat.mode; what you send joins the
// chat thread (thread.go, #302); the agent's lines come back as `chat.line` events, the mode as `replyMode`, a hotkey press in text mode as
// `chat.focus`.

// errNoCoordinator: chat.send with the agent off.
var errNoCoordinator = errors.New("The coordinator is off.")

func (a *App) registerChat(r Registrar) {
	r.Register("chat.send", func(args json.RawMessage) (any, error) {
		var in struct {
			Text        string   `json:"text"`
			Attachments []string `json:"attachments"`
			// Session is the address of the session you're replying to (#302): the coordinator
			// is asked to tell it.
			Session string `json:"session"`
		}
		if err := decodeArgs("chat.send", args, &in); err != nil {
			return nil, err
		}
		if a.agent == nil {
			return nil, errNoCoordinator
		}
		if in.Session != "" {
			// Always a new job: never taken for the answer to a question waiting in the box.
			job, err := a.agent.ChatDelegate("Tell the " + a.sessionLabel(in.Session) + " session: " + in.Text)
			if err != nil {
				return nil, err
			}
			a.threadSent(job, in.Text, in.Attachments, in.Session)
			return map[string]any{"job": job}, nil
		}
		sent, err := a.agent.ChatSend(in.Text)
		if err != nil {
			return nil, err
		}
		a.threadSent(sent.Job, in.Text, in.Attachments, "")
		if sent.Answered {
			a.threadSentAnswer(sent.Job, sent.Session, strings.TrimSpace(in.Text))
		}
		return map[string]any{"job": sent.Job}, nil
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
