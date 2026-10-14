package voice

// The conversation rules as a pure state machine (spec §5). No I/O, no goroutines and no
// clock: the caller passes `now` and carries out the returned effects, in order.
// One-for-one port of ai-mascot crates/voice-core/src/conversation.rs.

import (
	"encoding/json"
	"time"
)

const (
	// ErrorCooldown is how long the error state shows before Bit returns to idle.
	ErrorCooldown = 2 * time.Second
	// ReplyWatchdog is how long Thinking may last with no response open and no tool running
	// before giving up (also bounds a stalled Speaking phase).
	ReplyWatchdog = 15 * time.Second
	// MicPermissionWait is how long a conversation waits for the user to answer macOS's
	// microphone prompt.
	MicPermissionWait = 60 * time.Second
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
	InHotkey   InputKind = iota
	InModel              // Event: something the voice session reported
	InToolDone           // CallID finished; End = it asked to end the conversation
	InTick               // PlaybackIdle; sent about 30 times a second by the runtime
	InFailed             // Failure
	InInject             // Notice: say something about it once nobody is talking
	InHoldOpen           // Hold: while on, the silence timeout is the hold timeout and Bit rests in delegating
	InMicLive            // the mic is actually on (the permission prompt may have shown first)
)

// Input is one stimulus for Conversation.Step. Build it with the *Input constructors.
type Input struct {
	Kind         InputKind
	Event        Event   // InModel
	CallID       string  // InToolDone
	End          bool    // InToolDone
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
func TickInput(playbackIdle bool) Input { return Input{Kind: InTick, PlaybackIdle: playbackIdle} }
func FailedInput(f Failure) Input       { return Input{Kind: InFailed, Failure: f} }
func InjectInput(n Notice) Input        { return Input{Kind: InInject, Notice: n} }
func HoldOpenInput(on bool) Input       { return Input{Kind: InHoldOpen, Hold: on} }
func MicLiveInput() Input               { return Input{Kind: InMicLive} }

// EffectKind mirrors the Rust `Effect` enum.
type EffectKind int

const (
	EfConnect EffectKind = iota
	EfStartMic
	EfStopMic
	EfClose
	EfPlay            // Audio
	EfResponseStarted // a new reply started playing: remember the playback position to compute "heard"
	EfInterrupt       // the user talked over Bit: flush playback and send CmdCancelResponse{HeardMs}
	EfFlushPlayback
	EfPlayChime  // Chime (played only when RuntimeConfig.Chimes)
	EfInvokeTool // CallID, ToolName, Args
	EfMascot     // State
	EfStatus     // Status; "" clears the status line (Rust's Status(None))
	EfInject     // Text: send CmdInject{Text, Respond: true}
	EfSignal     // Signal: tell subscribers
)

// Effect is one thing the caller must do, in order.
type Effect struct {
	Kind     EffectKind
	Audio    []int16         // EfPlay
	Chime    Chime           // EfPlayChime
	CallID   string          // EfInvokeTool
	ToolName string          // EfInvokeTool
	Args     json.RawMessage // EfInvokeTool
	State    MascotState     // EfMascot
	Status   string          // EfStatus
	Text     string          // EfInject
	Signal   VoiceSignal     // EfSignal
}

// ConversationConfig sets the silence timeouts.
type ConversationConfig struct {
	IdleTimeout time.Duration // silence timeout while listening
	HoldTimeout time.Duration // silence timeout while a hold is on; 0 = IdleTimeout
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
}

// NewConversation starts in PhaseIdle.
func NewConversation(cfg ConversationConfig) *Conversation {
	hold := cfg.HoldTimeout
	if hold == 0 {
		hold = cfg.IdleTimeout
	}
	return &Conversation{
		phase:        PhaseIdle,
		idleTimeout:  cfg.IdleTimeout,
		holdTimeout:  hold,
		playbackIdle: true,
		toolTurns:    map[string]uint64{},
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

// restState is the state Bit rests in outside a conversation.
func (c *Conversation) restState() MascotState {
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
			fx = append(fx, Effect{Kind: EfConnect}, mascotFx(StateThinking))
		case PhaseConnecting:
			c.phase = PhaseIdle
			fx = append(fx, Effect{Kind: EfClose}, mascotFx(c.restState()), c.closedSignal())
		default:
			fx = c.end(fx, true)
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
			fx = append(fx, mascotFx(c.restState()))
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
			fx = append(fx, mascotFx(c.restState()))
		case c.listeningArmed() && c.micLive && c.pendingTools == 0 && elapsed(c.silenceSince, now, c.timeout()):
			fx = c.end(fx, false)
		case c.phase == PhaseThinking && c.openResponses == 0 && c.pendingTools == 0 &&
			elapsed(c.thinkingSince, now, ReplyWatchdog):
			c.thinkingSince = time.Time{}
			c.listen(now)
			fx = append(fx, mascotFx(StateListening))
		case c.phase == PhaseSpeaking && elapsed(c.stalledSince, now, ReplyWatchdog):
			c.stalledSince = time.Time{}
			if c.ending {
				fx = c.end(fx, false)
			} else {
				c.listen(now)
				fx = append(fx, mascotFx(StateListening))
			}
		default:
			fx = c.checkTurnEnd(now, fx)
		}
		fx = c.tryInject(now, fx)
	case InToolDone:
		if c.IsActive() {
			if c.pendingTools > 0 {
				c.pendingTools--
			}
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
				// The adapter asks the model for a reply that uses the result.
				c.turnReplySeen = false
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
				mascotFx(StateListening),
				Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigOpened}})
		}
	case EvSpeechStarted:
		c.turn++ // the user's new turn: earlier tool calls no longer belong to it
		switch c.phase {
		case PhaseSpeaking:
			c.listen(time.Time{})
			c.ending = false
			fx = append(fx, Effect{Kind: EfInterrupt}, mascotFx(StateListening))
		case PhaseThinking:
			c.thinkingSince = time.Time{}
			c.ending = false
			c.listen(time.Time{})
			fx = append(fx, mascotFx(StateListening))
		case PhaseListening:
			c.listen(time.Time{})
		}
	case EvSpeechStopped:
		if c.phase == PhaseListening {
			c.phase = PhaseThinking
			c.thinkingSince = now
			c.turnReplySeen = false
			fx = append(fx, mascotFx(StateThinking))
		}
	case EvAudioDelta:
		switch {
		// A new reply, either to the user's turn or (rarely) unprompted while quiet.
		case c.phase == PhaseThinking || c.listeningArmed():
			c.phase = PhaseSpeaking
			c.thinkingSince = time.Time{}
			c.playbackIdle = false
			c.responseAudioPending = false
			fx = append(fx, Effect{Kind: EfResponseStarted}, mascotFx(StateIdle), Effect{Kind: EfPlay, Audio: ev.Audio})
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
		fx = c.checkTurnEnd(now, fx)
	case EvToolCall:
		c.pendingTools++
		c.toolTurns[ev.CallID] = c.turn
		fx = append(fx, Effect{Kind: EfInvokeTool, CallID: ev.CallID, ToolName: ev.ToolName, Args: ev.Args})
	case EvTranscript:
		if ev.Role == "user" {
			fx = append(fx, Effect{Kind: EfSignal, Signal: VoiceSignal{Kind: SigUserSaid, Text: ev.Text}})
		}
	case EvError:
		if ev.Fatal {
			fx = c.fail(Failure{Kind: FailModelError}, now, fx)
		}
	}
	return fx
}

// checkTurnEnd: after a response, back to listening, or end if end_conversation was called.
func (c *Conversation) checkTurnEnd(now time.Time, fx []Effect) []Effect {
	if c.openResponses > 0 || !c.turnReplySeen || c.pendingTools > 0 {
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
		fx = append(fx, mascotFx(StateListening))
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
	c.phase = PhaseThinking
	c.thinkingSince = now
	c.turnReplySeen = false
	return append(fx, Effect{Kind: EfInject, Text: n.Text}, mascotFx(StateThinking))
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
		mascotFx(c.restState()),
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
		mascotFx(StateError),
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
}

func mascotFx(s MascotState) Effect { return Effect{Kind: EfMascot, State: s} }
