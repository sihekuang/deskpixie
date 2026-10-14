package voice

import "testing"

// Port of types.rs `failure_status_lines_are_exact`.
func TestFailureStatusLinesAreExact(t *testing.T) {
	cases := []struct {
		f    Failure
		want string
	}{
		{NoAPIKey("OPENAI_API_KEY"), "Voice: no API key (set OPENAI_API_KEY)"},
		{NoAPIKey("GEMINI_API_KEY"), "Voice: no API key (set GEMINI_API_KEY)"},
		{NoAPIKey(""), "Voice: no API key"},
		{Failure{Kind: FailMicDenied}, "Voice: microphone access denied"},
		{Failure{Kind: FailMicPermissionTimeout}, "Voice: no answer to the microphone prompt"},
		{Failure{Kind: FailConnectionLost}, "Voice: connection lost"},
		{Failure{Kind: FailModelError}, "Voice: model error"},
		{Failure{Kind: FailAudioDeviceChanged}, "Voice: audio device changed"},
		{SetupFailure(`unknown provider "x"`), `Voice: unknown provider "x"`},
	}
	for _, c := range cases {
		if got := c.f.Status(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.f, got, c.want)
		}
	}
}
