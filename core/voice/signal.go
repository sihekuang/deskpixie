package voice

// Notice is a line the app wants Pixie to say something about ("Coordinator reply: ...").
// Port of voice-core types.rs `Notice`.
type Notice struct {
	Text string
	// Sessions: the user's sessions the notice is about, for buttons that go to them (#261);
	// never said.
	Sessions []string
	// Held: the session whose message this notice says still waits (#183); never said. Once
	// that session has it, WithdrawHeld drops the notice if it hasn't been said yet (#339).
	Held string
}

// SignalKind enumerates what the voice loop tells the rest of the app.
type SignalKind int

const (
	// SigOpened: a conversation started (the session is ready and the mic is open).
	SigOpened SignalKind = iota
	// SigClosed: the conversation ended; Undelivered are injected notices never spoken.
	// Can arrive without a preceding SigOpened (failed connect, inject while idle).
	SigClosed
	// SigUserSaid: the user's transcribed words, in Text.
	SigUserSaid
)

// VoiceSignal is a port of voice-core types.rs `VoiceSignal`.
type VoiceSignal struct {
	Kind        SignalKind
	Undelivered []Notice // SigClosed
	Text        string   // SigUserSaid
}

// Chime is an earcon (synthesized by ChimeSamples; played when RuntimeConfig.Chimes).
type Chime int

const (
	ChimeStart Chime = iota
	ChimeEnd
)
