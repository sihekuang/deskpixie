package voice

// FailureKind says why a conversation failed. Port of voice-core types.rs `Failure`.
type FailureKind int

const (
	// FailNoAPIKey: no key for the provider; Failure.Var names the variable that would hold it.
	FailNoAPIKey FailureKind = iota
	FailMicDenied
	// FailMicPermissionTimeout: the microphone prompt went unanswered for MicPermissionWait.
	FailMicPermissionTimeout
	FailConnectionLost
	FailModelError
	FailAudioDeviceChanged
	// FailSetup: setup problems such as an unknown provider name; Failure.Msg follows "Voice: ".
	FailSetup
)

// Failure is why a conversation failed; each maps to one tray status line (spec §7).
type Failure struct {
	Kind FailureKind
	Var  string // FailNoAPIKey: env var name, "" when unknown (Rust's None)
	Msg  string // FailSetup
}

// NoAPIKey builds FailNoAPIKey; pass "" when the variable name is unknown.
func NoAPIKey(envVar string) Failure { return Failure{Kind: FailNoAPIKey, Var: envVar} }

// SetupFailure builds FailSetup with the text that follows "Voice: ".
func SetupFailure(msg string) Failure { return Failure{Kind: FailSetup, Msg: msg} }

// Status is the exact tray status line for the failure.
func (f Failure) Status() string {
	switch f.Kind {
	case FailNoAPIKey:
		if f.Var != "" {
			return "Voice: no API key (set " + f.Var + ")"
		}
		return "Voice: no API key"
	case FailMicDenied:
		return "Voice: microphone access denied"
	case FailMicPermissionTimeout:
		return "Voice: no answer to the microphone prompt"
	case FailConnectionLost:
		return "Voice: connection lost"
	case FailModelError:
		return "Voice: model error"
	case FailAudioDeviceChanged:
		return "Voice: audio device changed"
	case FailSetup:
		return "Voice: " + f.Msg
	}
	return "Voice: error"
}
