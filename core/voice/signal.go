package voice

// Notice is a line the app wants Pixie to say something about ("Coordinator reply: ...").
// Port of voice-core types.rs `Notice`.
type Notice struct {
	Text string
	// Sessions: the user's sessions the notice is about, for buttons that go to them (#261);
	// never said.
	Sessions []string
	// Held: the session whose message this notice says still waits (#183), and HeldSeq the
	// number pix gave that message (0: not known); never said. Once that session has it,
	// WithdrawHeld drops the notice if it hasn't been said yet (#339).
	Held    string
	HeldSeq uint64
	// Question: the id of the question this notice asks you (0: none); never said. Once it is
	// answered or gone, WithdrawQuestion drops the notice if it hasn't been said yet (#490).
	Question uint64
}

// SignalKind enumerates what the voice loop tells the rest of the app.
type SignalKind int

const (
	// SigOpened: a conversation started (the session is ready and the mic is open, or MicOff).
	SigOpened SignalKind = iota
	// SigClosed: the conversation ended; Undelivered are injected notices never spoken.
	// Can arrive without a preceding SigOpened (failed connect, inject while idle).
	SigClosed
	// SigUserSaid: the user's transcribed words, in Text.
	SigUserSaid
	// SigListening: the conversation opened with MicOff turns its mic on: you pressed the hotkey,
	// or it asked you something (#495).
	SigListening
)

// VoiceSignal is a port of voice-core types.rs `VoiceSignal`.
type VoiceSignal struct {
	Kind        SignalKind
	Undelivered []Notice // SigClosed
	Text        string   // SigUserSaid
	MicOff      bool     // SigOpened: opened only to speak, so you can't answer in it unless SigListening follows (#433)
}

// Chime is an earcon (synthesized by ChimeSamples; played when RuntimeConfig.Chimes).
type Chime int

const (
	ChimeStart Chime = iota
	ChimeEnd
)
