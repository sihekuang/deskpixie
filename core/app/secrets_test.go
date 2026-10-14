package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

type secretsReply struct {
	OpenAI  string            `json:"openai"`
	Gemini  string            `json:"gemini"`
	Sources map[string]string `json:"sources"`
}

func (h *harness) secrets() secretsReply {
	h.t.Helper()
	var s secretsReply
	if err := json.Unmarshal(h.call("settings.secrets", nil), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func TestKeychainValuesReplaceTheSetAndShowAsTheSource(t *testing.T) {
	h := newHarness(t)
	const fake = "sk-fake-keychain-value-123"
	h.call("secrets.keychain", map[string]any{"values": map[string]string{"OPENAI_API_KEY": fake, "HOME": "/nope"}})
	s := h.secrets()
	if s.OpenAI != "found" || s.Gemini != "missing" || s.Sources["openai"] != "keychain" || s.Sources["gemini"] != "" {
		t.Fatalf("%+v", s)
	}
	key, ok := h.app.deps.Secrets.APIKey("openai-realtime")
	if !ok || key.Expose() != fake {
		t.Fatal("the voice loop's source does not see the keychain value")
	}
	for _, m := range []string{"settings.secrets", "settings.snapshot", "app.state"} {
		if strings.Contains(string(h.call(m, nil)), fake) {
			t.Fatalf("%s leaks the key", m)
		}
	}

	h.call("secrets.keychain", map[string]any{"values": map[string]string{}})
	if s := h.secrets(); s.OpenAI != "missing" || s.Sources["openai"] != "" {
		t.Fatalf("not replaced: %+v", s)
	}
	if _, err := h.callErr("secrets.keychain", "not an object"); err == nil {
		t.Fatal("bad args accepted")
	}
}

// fixed is a SecretSource that cannot take Keychain values.
type fixed struct{}

func (fixed) APIKey(string) (voice.Secret, bool) { return voice.NewSecret("sk-fake"), true }
func (fixed) KeyHint(string) string              { return "" }

func TestKeychainValuesNeedASourceThatTakesThem(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) { d.Secrets = fixed{} })
	if _, err := h.callErr("secrets.keychain", map[string]any{"values": map[string]string{}}); err == nil {
		t.Fatal("expected an error")
	}
	if s := h.secrets(); s.OpenAI != "found" || s.Sources["openai"] != "env" {
		t.Fatalf("%+v", s)
	}
}
