package voice

// The conversation rules as a pure state machine (spec §5). No I/O, no goroutines and no
// clock: the caller passes `now` and carries out the returned effects, in order.
// One-for-one port of ai-mascot crates/voice-core/src/conversation.rs.

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// ErrorCooldown is how long the error state shows before Pixie returns to idle.
	ErrorCooldown = 2 * time.Second
	// ReplyWatchdog is how long Thinking may last with no response open and no tool running
	// before giving up (also bounds a stalled Speaking phase).
	ReplyWatchdog = 15 * time.Second
	// MicPermissionWait is how long a conversation waits for the user to answer macOS's
	// microphone prompt.
	MicPermissionWait = 60 * time.Second
	// HandOffGrace is how long after Pixie's reply to a request the hand-off waits before it
	// decides: a late transcript of the user's words, or a provider's late tool call, still
	// counts (#71).
	HandOffGrace = 1500 * time.Millisecond
	// ClientAckGrace: after a client delegation that ends the conversation once acknowledged,
	// how long it stays open at least, so GPT-Live's hand-off line can start (#70).
	ClientAckGrace = 3 * time.Second
)

// Phase is where the conversation is. Rust's Phase carries data in two variants; here that
// data lives on Conversation (see SilenceSince).
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseConnecting
	// PhaseListening: the silence timer is armed (SilenceSince ok) or off while the user talks.
	PhaseListening
	PhaseThinking
	PhaseSpeaking
	// PhaseCooldown: showing the error state until ErrorCooldown has passed.
	PhaseCooldown
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "Idle"
	case PhaseConnecting:
		return "Connecting"
	case PhaseListening:
		return "Listening"
	case PhaseThinking:
		return "Thinking"
	case PhaseSpeaking:
		return "Speaking"
	case PhaseCooldown:
		return "Cooldown"
	}
	return "Phase(?)"
}

// InputKind mirrors the Rust `Input` enum.
type InputKind int

const (
	InHotkey      InputKind = iota
	InModel                 // Event: something the voice session reported
	InToolDone              // CallID finished; End = it asked to end the conversation
	InTick                  // PlaybackIdle; sent about 30 times a second by the runtime
	InFailed                // Failure
	InInject                // Notice: say something about it once nobody is talking
	InHoldOpen              // Hold: while on, the silence timeout is the hold timeout and Pixie rests in delegating
	InMicLive               // the mic is actually on (the permission prompt may have shown first)
	InOpen                  // start a conversation if none is under way (unlike InHotkey, never ends one)
	InHandOffDone           // the hand-off's delegate call finished; Notice (Text "" = nothing to say)
)

// Input is one stimulus for Conversation.Step. Build it with the *Input constructors.
type Input struct {
	Kind         InputKind
	Event        Event   // InModel
	CallID       string  // InToolDone
	End          bool    // InToolDone
	EndAfter     bool    // InToolDone: end once the reply that uses the result has played
	PlaybackIdle bool    // InTick
	Failure      Failure // InFailed
	Notice       Notice  // InInject
	Hold         bool    // InHoldOpen
}

func HotkeyInput() Input        { return Input{Kind: InHotkey} }
func ModelInput(ev Event) Input { return Input{Kind: InModel, Event: ev} }
func ToolDoneInput(callID string, end bool) Input {
	return Input{Kind: InToolDone, CallID: callID, End: end}
}

// ToolDoneAfterReplyInput: CallID finished and asked for one more reply (e.g. "On it, I'll let
// you know"), after which the conversation ends.
func ToolDoneAfterReplyInput(callID string) Input {
	return Input{Kind: InToolDone, CallID: callID, EndAfter: true}
}
func TickInput(playbackIdle bool) Input { return Input{Kind: InTick, PlaybackIdle: playbackIdle} }
func FailedInput(f Failure) Input       { return Input{Kind: InFailed, Failure: f} }
func InjectInput(n Notice) Input        { return Input{Kind: InInject, Notice: n} }
func HoldOpenInput(on bool) Input       { return Input{Kind: InHoldOpen, Hold: on} }
func MicLiveInput() Input               { return Input{Kind: InMicLive} }
func OpenInput() Input                  { return Input{Kind: InOpen} }

// HandOffDoneInput: the hand-off (EfHandOff) finished; Pixie says n (if any) once it's quiet.
func HandOffDoneInput(n Notice) Input { return Input{Kind: InHandOffDone, Notice: n} }

// EffectKind mirrors the Rust `Effect` enum.
type EffectKind int

const (
	EfConnect EffectKind = iota
	EfStartMic
	EfStopMic
	EfClose
	EfPlay            // Audio
	EfResponseStarted // a new reply started playing: remember the playback position to compute "heard"
	EfInterrupt       // the user talked over Pixie: flush playback and send CmdCancelResponse{HeardMs}
	EfFlushPlayback
	EfPlayChime  // Chime (played only when RuntimeConfig.Chimes)
	EfInvokeTool // CallID, ToolName, Args
	EfPixie      // State
	EfStatus     // Status; "" clears the status line (Rust's Status(None))
	EfInject     // Text: send CmdInject{Text, Respond: true}
	EfSignal     // Signal: tell subscribers
	// EfHandOff: Pixie gave a request a non-answer (#71), or claimed it handed it off without
	// calling delegate (Claim, #72); delegate Text (the user's words) with Context, then feed
	// HandOffDoneInput.
	EfHandOff
)

// Effect is one thing the caller must do, in order.
type Effect struct {
	Kind     EffectKind
	Audio    []int16         // EfPlay
	Chime    Chime           // EfPlayChime
	CallID   string          // EfInvokeTool
	ToolName string          // EfInvokeTool
	Args     json.RawMessage // EfInvokeTool
	// Canned (EfInvokeTool): don't run the tool, answer the call with this output (a delegate
	// call in a turn the app already handed off).
	Canned  json.RawMessage
	State   PixieState // EfPixie
	Status  string     // EfStatus
	Text    string     // EfInject; EfHandOff: the task
	Context string     // EfHandOff
	// Claim (EfHandOff): Pixie already said it handed it off, so a successful hand-off says
	// nothing more (#72).
	Claim bool
	// Answer (EfHandOff, client delegation): the user is answering a relayed "Coordinator question:".
	Answer bool
	Signal VoiceSignal // EfSignal
}

// ConversationConfig sets the silence timeouts.
type ConversationConfig struct {
	IdleTimeout time.Duration // silence timeout while listening
	HoldTimeout time.Duration // silence timeout while a hold is on; 0 = IdleTimeout
	// ProviderTurns: the provider decides whether to speak after a tool result
	// (Capabilities.OwnsTurnTaking), so a finished tool doesn't make the turn wait for a reply.
	ProviderTurns bool
	// HandOff: when Pixie answers a request with a non-answer and calls no tool, hand the request
	// to the delegate tool (voice.handOffWhenUnsure, and only with a delegate tool; #71).
	HandOff bool
	// CheckClaims: when Pixie's words claim an action whose tool wasn't called in the turn, correct
	// it (voice.checkClaims; #72). ClaimTools are the covered tools that exist (the runtime sets
	// them from its registry).
	CheckClaims bool
	ClaimTools  []string
	// ClientDelegation: the provider hands turns to the app without a task (SessionConfig.
	// ClientDelegation; #70). Its ClientDelegationTool calls get the task from the transcript.
	ClientDelegation bool
}

// DefaultConversationConfig: 7 s idle, 180 s hold-open.
func DefaultConversationConfig() ConversationConfig {
	return ConversationConfig{IdleTimeout: 7 * time.Second, HoldTimeout: 180 * time.Second}
}

// Conversation is the pure conversation state machine. Not safe for concurrent use; the
// runtime owns it from one goroutine.
type Conversation struct {
	phase Phase
	// silenceSince: when the silence timer was armed; zero while the user is talking (Listening).
	silenceSince time.Time
	// cooldownUntil: end of the error display (Cooldown).
	cooldownUntil time.Time

	idleTimeout time.Duration
	holdTimeout time.Duration
	held        bool
	// providerTurns: ConversationConfig.ProviderTurns.
	providerTurns bool

	openResponses        int  // ResponseCreated minus ResponseDone
	responseAudioPending bool // a response was created and its first audio has not arrived
	turnReplySeen        bool // a response has been created for the current user turn (or follow-up)
	thinkingSince        time.Time
	stalledSince         time.Time // Speaking looks stalled: quiet playback, no reply open/pending/expected
	playbackIdle         bool
	pendingTools         int  // until they finish, ResponseDone doesn't end the turn
	ending               bool // end_conversation was called: end once the reply finished playing
	micLive              bool
	micAskedAt           time.Time
	turn                 uint64            // counts the user's turns (each SpeechStarted)
	toolTurns            map[string]uint64 // running tool calls and the turn each was called in
	injects              []Notice          // waiting for a quiet moment, oldest first

	// The hand-off safety net (#71), per user turn.
	handOff     bool            // ConversationConfig.HandOff
	hoUser      string          // the user's words this turn (the last transcript)
	hoBot       []string        // Pixie's words this turn
	hoTool      bool            // a tool was called this turn
	hoTools     map[string]bool // the tools called this turn, for the claim check
	hoHandedOff bool            // the app handed this turn off (a late delegate call is answered, not run)
	hoSettled   bool            // decided for this turn (handed off, or ruled out)
	hoRepliedAt time.Time       // Pixie's reply to this turn finished
	hoPending   int             // hand-offs running (counted in pendingTools too)
	hoHistory   []string        // earlier turns, "User: …" / "Pixie: …", for the delegate's context

	// The claim check (#72) shares the per-turn state and the once-per-turn guard above.
	checkClaims bool
	claimTools  []string
	// claimFix: the correction queued for this turn and the tool it is about; a late call to that
	// tool (GPT-Live's backend can lag) withdraws it while it still waits.
	claimFix     Notice
	claimFixTool string

	// Client delegation (#70).
	clientDelegation bool
	// questionAsked: a "Coordinator question:" (not a confirm prompt) was relayed since the user's
	// last turn began; answerTurn: this turn is the user's first since one was relayed.
	questionAsked, answerTurn bool
	// endNotBefore: an ending conversation waits until then for GPT-Live's hand-off line.
	endNotBefore time.Time
}

// NewConversation starts in PhaseIdle.
func NewConversation(cfg ConversationConfig) *Conversation {
	hold := cfg.HoldTimeout
	if hold == 0 {
		hold = cfg.IdleTimeout
	}
	return &Conversation{
		phase:         PhaseIdle,
		idleTimeout:   cfg.IdleTimeout,
		holdTimeout:   hold,
		providerTurns: cfg.ProviderTurns,
		handOff:       cfg.HandOff,
		checkClaims:   cfg.CheckClaims,
		claimTools:    cfg.ClaimTools,

		clientDelegation: cfg.ClientDelegation,
		playbackIdle:     true,
		toolTurns:        map[string]uint64{},
	}
}

// Phase reports the current phase.
func (c *Conversation) Phase() Phase { return c.phase }

// SilenceSince reports when the silence timer was armed; ok is false outside Listening or
// while the user is talking.
func (c *Conversation) SilenceSince() (since time.Time, ok bool) {
	if c.phase != PhaseListening || c.silenceSince.IsZero() {
		return time.Time{}, false
	}
	return c.silenceSince, true
}

// IsActive: a conversation is connecting or under way (not Idle, not Cooldown).
func (c *Conversation) IsActive() bool {
	return c.phase != PhaseIdle && c.phase != PhaseCooldown
}

func (c *Conversation) timeout() time.Duration {
	if c.held {
		return c.holdTimeout
	}
	return c.idleTimeout
}

// restState is the state Pixie rests in outside a conversation.
func (c *Conversation) restState() PixieState {
	if c.held {
		return StateDelegating
	}
	return StateIdle
}

func (c *Conversation) listen(armedAt time.Time) {
	c.phase = PhaseListening
	c.silenceSince = armedAt
}

func (c *Conversation) listeningArmed() bool {
	return c.phase == PhaseListening && !c.silenceSince.IsZero()
}

func elapsed(since, now time.Time, d time.Duration) bool {
	return !since.IsZero() && now.Sub(since) >= d
}

// Step applies one input at time now and returns the effects to carry out, in order.
func (c *Conversation) Step(in Input, now time.Time) []Effect {
	var fx []Effect
	switch in.Kind {
	case InHotkey:
		switch c.phase {
		case PhaseIdle, PhaseCooldown:
			c.resetTurn()
			c.phase = PhaseConnecting
			fx = append(fx, Effect{Kind: EfConnect}, pixieFx(StateThinking))
		case PhaseConnecting:
			c.phase = PhaseIdle
			fx = append(fx, Effect{Kind: EfClose}, pixieFx(c.restState()), c.closedSignal())
		default:
			fx = c.end(fx, true)
		}
	case InOpen:
		// Only from rest: a conversation starting, open, closing or showing an error is left alone.
		if c.phase == PhaseIdle {
			c.resetTurn()
			c.phase = PhaseConnecting
			fx = append(fx, Effect{Kind: EfConnect}, pixieFx(StateThinking))
		}
	case InFailed:
		fx = c.fail(in.Failure, now, fx)
	case InInject:
		if c.IsActive() {
			c.injects = append(c.injects, in.Notice)
			fx = c.tryInject(now, fx)
		} else {
			fx = append(fx, Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigClosed, Undelivered: []Notice{in.Notice}}})
		}
	case InMicLive:
		if c.IsActive() && !c.micLive {
			c.micLive = true
			if c.listeningArmed() {
				c.silenceSince = now
			}
		}
	case InHoldOpen:
		c.held = in.Hold
		if c.phase == PhaseIdle {
			fx = append(fx, pixieFx(c.restState()))
		}
	case InTick:
		c.playbackIdle = in.PlaybackIdle
		stalled := c.phase == PhaseSpeaking && in.PlaybackIdle && c.openResponses == 0 &&
			c.pendingTools == 0 && !c.turnReplySeen
		if !stalled {
			c.stalledSince = time.Time{}
		} else if c.stalledSince.IsZero() {
			c.stalledSince = now
		}
		if c.IsActive() && !c.micLive && elapsed(c.micAskedAt, now, MicPermissionWait) {
			return c.fail(Failure{Kind: FailMicPermissionTimeout}, now, fx)
		}
		switch {
		case c.phase == PhaseCooldown && !now.Before(c.cooldownUntil):
			c.phase = PhaseIdle
			fx = append(fx, pixieFx(c.restState()))
		case c.listeningArmed() && c.micLive && c.pendingTools == 0 && elapsed(c.silenceSince, now, c.timeout()):
			fx = c.end(fx, false)
		case c.phase == PhaseThinking && c.openResponses == 0 && c.pendingTools == 0 &&
			elapsed(c.thinkingSince, now, ReplyWatchdog):
			c.thinkingSince = time.Time{}
			c.listen(now)
			fx = append(fx, pixieFx(StateListening))
		case c.phase == PhaseSpeaking && elapsed(c.stalledSince, now, ReplyWatchdog):
			c.stalledSince = time.Time{}
			if c.ending {
				fx = c.end(fx, false)
			} else {
				c.listen(now)
				fx = append(fx, pixieFx(StateListening))
			}
		default:
			fx = c.checkTurnEnd(now, fx)
		}
		fx = c.maybeHandOff(now, fx)
		fx = c.tryInject(now, fx)
	case InHandOffDone:
		if c.IsActive() && c.hoPending > 0 {
			c.hoPending--
			if c.pendingTools > 0 {
				c.pendingTools--
			}
			if in.Notice.Text != "" {
				c.injects = append(c.injects, in.Notice)
			}
			if c.listeningArmed() {
				c.silenceSince = now
			}
			fx = c.checkTurnEnd(now, fx)
			fx = c.tryInject(now, fx)
		}
	case InToolDone:
		if c.IsActive() {
			if c.pendingTools > 0 {
				c.pendingTools--
			}
			// The claim check waits HandOffGrace from here for the reply that uses the result.
			c.hoRepliedAt = now
			turn, known := c.toolTurns[in.CallID]
			delete(c.toolTurns, in.CallID)
			thisTurn := known && turn == c.turn
			if in.End {
				// Only a goodbye from the current turn ends the conversation: a user who is
				// talking, or who has already asked something new, interrupted it.
				talking := c.phase == PhaseListening && c.silenceSince.IsZero()
				if thisTurn && !talking {
					c.ending = true
				}
			} else {
				if in.EndAfter && thisTurn && !(c.phase == PhaseListening && c.silenceSince.IsZero()) {
					// Ends once the reply below has played; the user talking cancels it.
					c.ending = true
				}
				// The adapter asks the model for a reply that uses the result. A provider that
				// owns turn-taking may not reply; its next reply opens a response anyway.
				if !c.providerTurns {
					c.turnReplySeen = false
				}
				if in.EndAfter && c.clientDelegation {
					// GPT-Live says "checking" at once and its hand-off line a moment later,
					// in the same reply or a new one: give it ClientAckGrace to start (#70).
					c.endNotBefore = now.Add(ClientAckGrace)
				}
				if c.phase == PhaseThinking {
					// The follow-up reply gets a full watchdog period.
					c.thinkingSince = now
				}
				if c.listeningArmed() {
					// The tool's work counted as activity: restart the silence timer.
					c.silenceSince = now
				}
			}
			fx = c.checkTurnEnd(now, fx)
		}
	case InModel:
		if c.IsActive() {
			fx = c.onModel(in.Event, now, fx)
		}
	}
	return fx
}

func (c *Conversation) onModel(ev Event, now time.Time, fx []Effect) []Effect {
	switch ev.Kind {
	case EvReady:
		if c.phase == PhaseConnecting {
			c.listen(now)
			c.micAskedAt = now
			fx = append(fx,
				Effect{Kind: EfStatus},
				Effect{Kind: EfPlayChime, Chime: ChimeStart},
				Effect{Kind: EfStartMic},
				pixieFx(StateListening),
				Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigOpened}})
		}
	case EvSpeechStarted:
		c.turn++ // the user's new turn: earlier tool calls no longer belong to it
		c.newHandOffTurn()
		switch c.phase {
		case PhaseSpeaking:
			c.listen(time.Time{})
			c.ending = false
			fx = append(fx, Effect{Kind: EfInterrupt}, pixieFx(StateListening))
		case PhaseThinking:
			c.thinkingSince = time.Time{}
			c.ending = false
			c.listen(time.Time{})
			fx = append(fx, pixieFx(StateListening))
		case PhaseListening:
			c.listen(time.Time{})
		}
	case EvSpeechStopped:
		if c.phase == PhaseListening {
			c.phase = PhaseThinking
			c.thinkingSince = now
			c.turnReplySeen = false
			fx = append(fx, pixieFx(StateThinking))
		}
	case EvAudioDelta:
		switch {
		// A new reply, either to the user's turn or (rarely) unprompted while quiet.
		case c.phase == PhaseThinking || c.listeningArmed():
			c.phase = PhaseSpeaking
			c.thinkingSince = time.Time{}
			c.playbackIdle = false
			c.responseAudioPending = false
			fx = append(fx, Effect{Kind: EfResponseStarted}, pixieFx(StateIdle), Effect{Kind: EfPlay, Audio: ev.Audio})
		case c.phase == PhaseSpeaking:
			c.playbackIdle = false
			// First audio of a follow-up response (e.g. after a tool call): re-mark playback.
			if c.responseAudioPending {
				c.responseAudioPending = false
				fx = append(fx, Effect{Kind: EfResponseStarted})
			}
			fx = append(fx, Effect{Kind: EfPlay, Audio: ev.Audio})
		}
		// Otherwise: trailing audio of a reply the user talked over; dropped.
	case EvResponseCreated:
		c.openResponses++
		c.turnReplySeen = true
		c.responseAudioPending = true
	case EvResponseDone:
		if c.openResponses > 0 {
			c.openResponses--
		}
		if c.openResponses == 0 {
			c.hoRepliedAt = now
		}
		fx = c.checkTurnEnd(now, fx)
	case EvToolCall:
		c.hoTool = true
		if c.hoTools == nil {
			c.hoTools = map[string]bool{}
		}
		tool := ev.ToolName
		if tool == ClientDelegationTool {
			tool = DelegateTool // for the claim check, a client delegation is a delegate call
		}
		c.hoTools[tool] = true
		c.withdrawClaimFix(tool)
		c.pendingTools++
		c.toolTurns[ev.CallID] = c.turn
		inv := Effect{Kind: EfInvokeTool, CallID: ev.CallID, ToolName: ev.ToolName, Args: ev.Args}
		if ev.ToolName == ClientDelegationTool {
			if c.hoHandedOff {
				inv.Canned = alreadyHandedOff
			} else {
				inv.Args = c.clientDelegationArgs()
				c.hoHandedOff = true
			}
		} else if ev.ToolName == DelegateTool && c.hoHandedOff {
			// The app already handed this turn to the agent; a provider that delegates late
			// (GPT-Live's backend) would start the same job twice.
			inv.Canned = alreadyHandedOff
		}
		fx = append(fx, inv)
	case EvTranscript:
		if ev.Role == "user" {
			fx = append(fx, Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigUserSaid, Text: ev.Text}})
			if t := strings.TrimSpace(ev.Text); t != "" {
				if c.clientDelegation && c.hoUser != "" && len(c.hoBot) == 0 {
					// GPT-Live's transcript of one utterance can come in pieces (a pause, or
					// words said while Pixie talked): keep them all for the task (#70).
					t = c.hoUser + " " + t
				}
				c.hoUser = t
			}
		} else if t := strings.TrimSpace(ev.Text); t != "" {
			c.hoBot = append(c.hoBot, t)
		}
	case EvError:
		if ev.Fatal {
			f := Failure{Kind: FailModelError}
			if ev.Failure != nil {
				f = *ev.Failure
			}
			fx = c.fail(f, now, fx)
		}
	}
	return fx
}

// checkTurnEnd: after a response, back to listening, or end if end_conversation was called.
func (c *Conversation) checkTurnEnd(now time.Time, fx []Effect) []Effect {
	if c.openResponses > 0 || !c.turnReplySeen || c.pendingTools > 0 {
		return fx
	}
	if c.ending && now.Before(c.endNotBefore) {
		return fx
	}
	var finished bool
	switch c.phase {
	case PhaseSpeaking:
		finished = c.playbackIdle
	case PhaseThinking:
		finished = true
	case PhaseListening:
		finished = c.ending
	}
	if !finished {
		return fx
	}
	if c.ending {
		return c.end(fx, false)
	}
	if c.phase != PhaseListening {
		c.thinkingSince = time.Time{}
		c.listen(now)
		fx = append(fx, pixieFx(StateListening))
	}
	return fx
}

// tryInject delivers the oldest notice when nobody is talking: listening with the silence
// timer armed, playback drained, no reply open and no tool running.
func (c *Conversation) tryInject(now time.Time, fx []Effect) []Effect {
	quiet := c.listeningArmed() && c.playbackIdle && c.openResponses == 0 && c.pendingTools == 0
	if !quiet || len(c.injects) == 0 {
		return fx
	}
	n := c.injects[0]
	c.injects = c.injects[1:]
	if isCoordinatorQuestion(n.Text) && !strings.Contains(n.Text, "Say confirm") {
		c.questionAsked = true
	}
	// What Pixie says next answers the notice, not the user's words.
	c.hoSettled = true
	c.phase = PhaseThinking
	c.thinkingSince = now
	c.turnReplySeen = false
	return append(fx, Effect{Kind: EfInject, Text: n.Text}, pixieFx(StateThinking))
}

// closedSignal drains the queued notices into a SigClosed signal.
func (c *Conversation) closedSignal() Effect {
	var undelivered []Notice
	if len(c.injects) > 0 {
		undelivered = c.injects
		c.injects = nil
	}
	return Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigClosed, Undelivered: undelivered}}
}

func (c *Conversation) end(fx []Effect, flush bool) []Effect {
	if flush {
		fx = append(fx, Effect{Kind: EfFlushPlayback})
	}
	fx = append(fx,
		Effect{Kind: EfStopMic},
		Effect{Kind: EfClose},
		Effect{Kind: EfPlayChime, Chime: ChimeEnd},
		pixieFx(c.restState()),
		c.closedSignal())
	c.phase = PhaseIdle
	c.silenceSince = time.Time{}
	c.resetTurn()
	return fx
}

func (c *Conversation) fail(f Failure, now time.Time, fx []Effect) []Effect {
	if !c.IsActive() {
		return fx
	}
	fx = append(fx,
		Effect{Kind: EfFlushPlayback},
		Effect{Kind: EfStopMic},
		Effect{Kind: EfClose},
		pixieFx(StateError),
		Effect{Kind: EfPlayChime, Chime: ChimeEnd},
		Effect{Kind: EfStatus, Status: f.Status()},
		c.closedSignal())
	c.phase = PhaseCooldown
	c.silenceSince = time.Time{}
	c.cooldownUntil = now.Add(ErrorCooldown)
	c.resetTurn()
	return fx
}

func (c *Conversation) resetTurn() {
	c.openResponses = 0
	c.turnReplySeen = false
	c.thinkingSince = time.Time{}
	c.stalledSince = time.Time{}
	c.playbackIdle = true
	c.pendingTools = 0
	clear(c.toolTurns)
	c.ending = false
	c.micLive = false
	c.micAskedAt = time.Time{}
	c.newHandOffTurn()
	c.hoPending = 0
	c.hoHistory = nil
	c.questionAsked, c.answerTurn = false, false
	c.endNotBefore = time.Time{}
}

// ---------- the hand-off safety net (#71) ----------

// handOffHistoryLines bounds the context: the last few lines of earlier turns.
const (
	handOffHistoryLines = 6
	handOffLineRunes    = 300
)

// newHandOffTurn starts a user turn: the last turn's words become history.
func (c *Conversation) newHandOffTurn() {
	if c.hoUser != "" {
		c.hoHistory = append(c.hoHistory, "User: "+clip(c.hoUser))
	}
	if len(c.hoBot) > 0 {
		c.hoHistory = append(c.hoHistory, "Pixie: "+clip(strings.Join(c.hoBot, " ")))
	}
	if n := len(c.hoHistory); n > handOffHistoryLines {
		c.hoHistory = append([]string(nil), c.hoHistory[n-handOffHistoryLines:]...)
	}
	c.hoUser, c.hoBot = "", nil
	c.answerTurn, c.questionAsked = c.questionAsked, false
	c.hoTool, c.hoSettled, c.hoHandedOff = false, false, false
	c.hoTools = nil
	c.claimFix, c.claimFixTool = Notice{}, ""
	c.hoRepliedAt = time.Time{}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= handOffLineRunes {
		return s
	}
	return string(r[:handOffLineRunes]) + "…"
}

// maybeHandOff decides, once per turn and HandOffGrace after Pixie's reply (and its tools)
// finished, never while the conversation is ending:
//   - the hand-off (#71): Pixie's reply to a request was a non-answer and no tool was called, so
//     the request goes to the delegate tool;
//   - else the claim check (#72): Pixie's words claim an action whose tool wasn't called. A
//     delegate claim ("let me hand that off") to a request is made true by handing the request
//     off; any other claim gets a correction notice, so Pixie doesn't guess the tool's arguments.
func (c *Conversation) maybeHandOff(now time.Time, fx []Effect) []Effect {
	if !(c.handOff || c.checkClaims) || c.hoSettled || !c.IsActive() || c.phase == PhaseConnecting {
		return fx
	}
	handOff := c.handOff && !c.hoTool
	if c.ending || c.hoTools[EndConversationTool] || (!handOff && !c.checkClaims) {
		c.hoSettled = true
		return fx
	}
	if c.openResponses > 0 || c.pendingTools > 0 || !elapsed(c.hoRepliedAt, now, HandOffGrace) {
		return fx
	}
	if len(c.hoBot) == 0 {
		return fx // a transcript may still come; the next turn resets this
	}
	bot := strings.Join(c.hoBot, " ")
	var claim Claim
	hasClaim := false
	if c.checkClaims {
		claim, hasClaim = FindClaim(bot, c.hoTools, c.claimTools)
	}
	needUser := (hasClaim && claim.Tool == DelegateTool) || (handOff && !hasClaim)
	if needUser && c.hoUser == "" {
		return fx // the user's transcript may still come
	}
	c.hoSettled = true
	if handOff && ShouldHandOff(c.hoUser, bot) {
		return c.handOffTask(c.hoUser, false, fx)
	}
	if !hasClaim {
		return fx
	}
	if claim.Tool == DelegateTool {
		if !IsRequest(c.hoUser) {
			return fx // likely about earlier work: nothing new to hand off
		}
		task := c.hoUser
		if claim.Check {
			task = "Check: " + task
		}
		return c.handOffTask(task, true, fx)
	}
	if n := ClaimNotice(claim); n.Text != "" {
		c.injects = append(c.injects, n)
		c.claimFix, c.claimFixTool = n, claim.Tool
	}
	return fx
}

// withdrawClaimFix drops this turn's queued correction when its tool is called after all.
func (c *Conversation) withdrawClaimFix(tool string) {
	if c.claimFixTool == "" || tool != c.claimFixTool {
		return
	}
	for i, n := range c.injects {
		if n == c.claimFix {
			c.injects = append(c.injects[:i:i], c.injects[i+1:]...)
			break
		}
	}
	c.claimFix, c.claimFixTool = Notice{}, ""
}

// handOffTask starts the app's hand-off of task (EfHandOff), with the earlier turns as context.
func (c *Conversation) handOffTask(task string, claim bool, fx []Effect) []Effect {
	c.hoPending++
	c.pendingTools++
	c.hoHandedOff = true
	answer := c.clientDelegation && c.answerTurn
	if answer {
		c.answerTurn = false
	}
	return append(fx, Effect{Kind: EfHandOff, Text: task, Context: c.handOffContext(), Claim: claim, Answer: answer})
}

// handOffContext is the earlier turns, for a delegated task's context ("" when none).
func (c *Conversation) handOffContext() string {
	if len(c.hoHistory) == 0 {
		return ""
	}
	return "Earlier in this voice conversation:\n" + strings.Join(c.hoHistory, "\n")
}

// clientDelegationArgs builds a client delegation's task from the transcript (#70): the user's
// words this turn (a question becomes a "Check: " fact), the earlier turns as context, and
// whether they answer a relayed "Coordinator question:". Without words this turn it falls back to
// the user's last earlier line.
func (c *Conversation) clientDelegationArgs() json.RawMessage {
	// The user's words since Pixie last spoke: a pause can split one request into turns.
	said := c.hoUser
	history := c.hoHistory
	for len(history) > 0 {
		rest, ok := strings.CutPrefix(history[len(history)-1], "User: ")
		if !ok {
			break
		}
		said = strings.TrimSpace(rest + " " + said)
		history = history[:len(history)-1]
	}
	if said == "" {
		for i := len(history) - 1; i >= 0; i-- {
			if rest, ok := strings.CutPrefix(history[i], "User: "); ok {
				said = rest
				break
			}
		}
	}
	ctx := ""
	if len(history) > 0 {
		ctx = "Earlier in this voice conversation:\n" + strings.Join(history, "\n")
	}
	a := ClientDelegationArgs{Task: said, Context: ctx, Answer: c.answerTurn}
	if !a.Answer && IsQuestion(said) {
		a.Task = CheckPrefix + said
	}
	if a.Answer {
		c.answerTurn = false // one answer per question
	}
	b, _ := json.Marshal(a)
	return b
}

// alreadyHandedOff answers a delegate call in a turn the app already handed off.
var alreadyHandedOff = json.RawMessage(`{"status":"already handed off","note":"The app already gave this request to the coordinator. Don't hand it off again; just say you'll let the user know."}`)

func pixieFx(s PixieState) Effect { return Effect{Kind: EfPixie, State: s} }
