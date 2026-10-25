package app

import (
	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/chat"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The delegation agent as the app sees it. core/app/agentapp builds it (the composition root
// that names the concrete agent types); the app only calls these methods. Port of the agent
// touchpoints in ai-mascot src-tauri/src/{lib.rs, actions.rs, tray.rs, voice/sink.rs}.

// AgentVoice is what the agent needs of the voice loop (*voice.Runtime).
type AgentVoice interface {
	Inject(voice.Notice)
	HoldOpen(on bool)
	// Open starts a conversation if Pixie is at rest (false: it did nothing).
	Open() bool
	Subscribe() <-chan voice.VoiceSignal
}

// AgentHooks are what the agent may do to the app. Each is safe from any goroutine.
type AgentHooks struct {
	// Celebrate (a job done) and FlashError (a job failed): Pixie's one-shots.
	Celebrate, FlashError func()
	// RefreshTray: the agent's tray rows or status changed.
	RefreshTray func()
	// ClearModel sets agent.model back to null (Claude Code's default) in settings.json.
	ClearModel func()
	// Notify shows a macOS notification (no sound: the agent chimes on its own).
	Notify func(title, body string)
	// NotifyUpdate is Notify for a bubble line (#199): the shell's click on it does what a click
	// on the line does. update is the line (agent.UpdateItem).
	NotifyUpdate func(title, body string, update any)
	// Updates is the updates bubble's list (the `updates` event's lines; never nil).
	Updates func(items any)
	// Reply is a reply card for the reply deck (the `reply.card` event's ReplyCard); a card that
	// gains a row comes again whole with the same id.
	Reply func(card any)
	// ThreadLine is a session update or coordinator line for the chat thread, ready to Add (#302).
	ThreadLine func(chat.Entry)
	// ThreadRequests is every open request now (from the updates list), whole each time, empty
	// when all are answered.
	ThreadRequests func([]chat.Entry)
	// ThreadAnswer is a typed job's answer (a done or failed ChatLine), one entry per turn; an
	// answer that grows comes again with the same id.
	ThreadAnswer func(chat.Entry)
	// ThreadLive is a typed job's step while it works (a working ChatLine's text), shown live in
	// the chat window and never stored; "" clears it. It clears before the job's answer comes.
	ThreadLive func(job, text string)
	// ThreadDelivered: these outbox lines were heard (replayed in a conversation) or seen (in the
	// bubble), so the entries holding them are read.
	ThreadDelivered func([]uint64)
	// Roster is the roster beside Pixie (the `agents` event's roster), on every change (#91).
	Roster func(view any)
	// ShowPane asks the shell to bring up an agent's pane (the `showPane` event, #110).
	ShowPane func(job, worker string)
	// Focus asks the shell to show a terminal by walking a FocusPlan (the `focusPlan` event,
	// #116): the shell executes it, Go only decided it.
	Focus func(plan any)
	// Chat is a line for the text box (the `chat.line` event's job, status and text, #221).
	Chat func(line any)
	// LocalTools are Pixie's local tools (NewLocalTools), the very instances the voice loop
	// holds, plus open_settings, which only the coordinator has (#288): the agent serves them to
	// the coordinator over MCP. Never to workers.
	LocalTools []tools.Tool
}

// Agent is the running delegation agent.
type Agent interface {
	// Tools are the voice tools (delegate, jobs, answer_job); Persona the persona section.
	Tools() []voice.Tool
	Persona() string
	// Tray is Tray ▸ Coordinator: job rows, the status line and the standing hint.
	Tray() AgentTray
	// Menu runs an agent:* menu id; false for an id that is not the agent's.
	Menu(id string) bool
	// OpenTerminal is a click on Pixie: attach a terminal to the agent session (async).
	OpenTerminal()
	// AttachVoice links the voice loop once it exists.
	AttachVoice(v AgentVoice)
	// OnMouth is each mouth level the voice loop reports (the echo gate).
	OnMouth(level int)
	// Decide: a tap on Allow or Deny in the bubble for the permission question with this token.
	Decide(token string, allow bool)
	// Choose: a tap on option (from 1) of a session menu in the bubble (#105).
	Choose(token string, option int)
	// TypedAnswer and ChatAbout: an answer typed in the bubble, or "Chat about this", for a
	// session menu that takes one (#268).
	TypedAnswer(token, text string)
	ChatAbout(token string)
	// UpdatesSeen: the updates bubble showed these lines long enough to count as heard.
	UpdatesSeen(ids []uint64)
	// RepliesShown: the deck showed the cards holding these outbox lines (line ids, not card
	// ids): those lines count as delivered once shown.
	RepliesShown(ids []uint64)
	// PendingReplies is the deck's cards not yet shown ([]agent.ReplyCard, never nil), for the
	// shell's first read: the event may go out before the deck exists.
	PendingReplies() any
	// UpdatesDismissed: the user clicked these lines away (#199): a job's question goes too.
	UpdatesDismissed(ids []uint64)
	// Updates is the bubble's current list (the `updates` event's lines), for the shell's first
	// read: the event may go out before the shell's bubble exists. An empty list, never nil.
	Updates() any
	// SetAway is the shell's presence (screen locked, displays asleep or idle).
	SetAway(away bool)
	// FocusJob is a click on a bubble line: focus that job's (or worker's) pane in a terminal (async).
	FocusJob(job, worker string)
	// Roster is the roster beside Pixie as it is now, for the shell's first read.
	Roster() any
	// FocusAgent is a click on one of Pixie's agents in the roster: its pane in a terminal (async).
	FocusAgent(name string)
	// FocusSession is a click on one of your own sessions in the roster: the tab it runs in (async).
	FocusSession(name string)
	// SessionPlaces are where your sessions named run (#277), in order, from one listing: their
	// folders and terminal apps, for the paths in their replies. Names not yours are left out.
	SessionPlaces(names []string) []SessionPlace
	// ReportFocusOutcome: the shell finished executing a FocusPlan (#116). Failure becomes the
	// tray status line, as for OpenTerminal.
	ReportFocusOutcome(success bool, detail string)
	// ChatSend is a message typed in the text box (#221): where it went, or why it can't go.
	ChatSend(text string) (ChatSent, error)
	// ChatDelegate is a message typed in the text box that is always a new job, never the
	// answer to a waiting question: a reply to a session, told through the coordinator (#302).
	ChatDelegate(text string) (string, error)
	// ChatStop is Stop in the text box: cancel the typed job still running.
	ChatStop()
	// SetTextMode: replyMode is text (no conversation is opened for updates).
	SetTextMode(on bool)
	// Attachments wait for the next request (#240); nil when there is nowhere to keep them.
	Attachments() *attach.Store
	// Shutdown is the app's quit: deny pending permissions, stop the panes, clear the MCP
	// configs, stop the session host and the hook server. Idempotent; blocks ~3 s at most.
	Shutdown()
}

// ChatSent is where a message typed in the text box went (#302): its job, and whether it
// answered a waiting question: the job's own, or (Session set) the session's at that address.
type ChatSent struct {
	Job      string
	Answered bool
	Session  string
}

// SessionPlace is where one of your sessions runs (#277): its folder and its terminal app's
// bundle id ("" = unknown).
type SessionPlace struct {
	Folder, App string
}

// AgentFacts is an Agent that also gives each voice session a context block (its recent jobs),
// so Pixie picks up where it left off without a memory of its own.
type AgentFacts interface {
	Facts() string
}

// AgentFactory builds the agent from settings; nil when agent.enabled is false.
type AgentFactory func(cfg settings.Config, appSupport string, hooks AgentHooks) Agent
