package voice

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Port of the 9 #[test]s in ai-mascot crates/voice-core/src/env_secrets.rs.

type kv = [2]string

func pairs(ps []DotenvPair) []kv {
	out := []kv{}
	for _, p := range ps {
		out = append(out, kv{p.Key, p.Value})
	}
	return out
}

func noEnv(string) (string, bool) { return "", false }

func TestParsesDotenvLines(t *testing.T) {
	text := "# comment\nOPENAI_API_KEY=sk-abc\nexport OTHER=\"quoted value\"\nSINGLE='x'\n\nBROKEN LINE\n"
	want := []kv{{"OPENAI_API_KEY", "sk-abc"}, {"OTHER", "quoted value"}, {"SINGLE", "x"}}
	if got := pairs(ParseDotenv(text)); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestReadsTheKeyFromADotenvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY=sk-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewEnvSecretsWithLookup([]string{path}, noEnv)
	if k, ok := src.APIKey("openai-realtime"); !ok || k.Expose() != "sk-from-file" {
		t.Fatalf("got %q %v", k.Expose(), ok)
	}
}

func TestEnvironmentWinsOverTheFileAndUnknownProvidersGetNone(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, func(name string) (string, bool) {
		return "sk-from-env", name == "OPENAI_API_KEY"
	})
	if k, ok := src.APIKey("openai-realtime"); !ok || k.Expose() != "sk-from-env" {
		t.Fatal("env key not found")
	}
	if _, ok := src.APIKey("someone-else"); ok {
		t.Fatal("unknown provider got a key")
	}
}

func TestEnvironmentWinsOverTheFile(t *testing.T) {
	// Extra case (not in Rust): with both present, the environment wins.
	path := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(path, []byte("OPENAI_API_KEY=sk-from-file\n"), 0o600)
	src := NewEnvSecretsWithLookup([]string{path}, func(name string) (string, bool) {
		return "sk-from-env", name == "OPENAI_API_KEY"
	})
	if k, _ := src.APIKey("openai-realtime"); k.Expose() != "sk-from-env" {
		t.Fatal("file beat the environment")
	}
}

func TestGeminiReadsItsOwnVariable(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, func(name string) (string, bool) {
		return "g-from-env", name == "GEMINI_API_KEY"
	})
	if k, ok := src.APIKey("gemini-live"); !ok || k.Expose() != "g-from-env" {
		t.Fatal("gemini key not found")
	}
	if _, ok := src.APIKey("openai-realtime"); ok {
		t.Fatal("no cross-provider fallback")
	}
}

func TestKeyVarsListsEveryProvidersVariable(t *testing.T) {
	vars := KeyVars()
	if !slices.Equal(vars, []string{"OPENAI_API_KEY", "GEMINI_API_KEY"}) {
		t.Fatalf("got %v", vars)
	}
	for _, p := range []string{"openai-realtime", "gemini-live", "openai-live"} {
		v, ok := VarFor(p)
		if !ok || !slices.Contains(vars, v) {
			t.Errorf("%s -> %q", p, v)
		}
	}
}

func TestKeyHintsNameTheVariableToSet(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, noEnv)
	if src.KeyHint("openai-realtime") != "OPENAI_API_KEY" || src.KeyHint("gemini-live") != "GEMINI_API_KEY" {
		t.Fatal("hints")
	}
	if src.KeyHint("someone-else") != "" {
		t.Fatal("unknown provider has a hint")
	}
}

func TestEmptyValuesCountAsMissing(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, func(string) (string, bool) { return "  ", true })
	if _, ok := src.APIKey("openai-realtime"); ok {
		t.Fatal("blank value counted")
	}
}

func TestHandlesCRLFAndBOM(t *testing.T) {
	text := "\ufeffOPENAI_API_KEY=sk-1\r\nOTHER=two\r\n"
	want := []kv{{"OPENAI_API_KEY", "sk-1"}, {"OTHER", "two"}}
	if got := pairs(ParseDotenv(text)); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestInlineCommentsAndHashes(t *testing.T) {
	text := "A=sk-x # note\nB=\"sk-y\" # note\nC='sk-z'   # note\nD=a#b\nE=\"has # inside\"\n"
	want := []kv{{"A", "sk-x"}, {"B", "sk-y"}, {"C", "sk-z"}, {"D", "a#b"}, {"E", "has # inside"}}
	if got := pairs(ParseDotenv(text)); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestSecretsReportsFoundOrMissing(t *testing.T) {
	// Extra (for settings.secrets): Status maps each provider to found/missing.
	src := NewEnvSecretsWithLookup(nil, func(name string) (string, bool) {
		return "k", name == "GEMINI_API_KEY"
	})
	if !src.Has("gemini-live") || src.Has("openai-realtime") {
		t.Fatal("Has")
	}
}

// --- Keychain values (#59): environment → Keychain → .env ---

func writeDotenv(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeychainValuesSitBetweenTheEnvironmentAndDotenv(t *testing.T) {
	path := writeDotenv(t, "OPENAI_API_KEY=sk-fake-file\nGEMINI_API_KEY=g-fake-file\n")
	src := NewEnvSecretsWithLookup([]string{path}, func(name string) (string, bool) {
		return "sk-fake-env", name == "OPENAI_API_KEY"
	})
	src.SetKeychain(map[string]string{"OPENAI_API_KEY": "sk-fake-kc", "GEMINI_API_KEY": "g-fake-kc"})
	if k, ok := src.APIKey("openai-realtime"); !ok || k.Expose() != "sk-fake-env" {
		t.Fatalf("env should beat keychain, got %q", k.Expose())
	}
	if k, ok := src.APIKey("gemini-live"); !ok || k.Expose() != "g-fake-kc" {
		t.Fatalf("keychain should beat .env, got %q", k.Expose())
	}
	if got := src.Source("openai-realtime"); got != SourceEnv {
		t.Fatal(got)
	}
	if got := src.Source("gemini-live"); got != SourceKeychain {
		t.Fatal(got)
	}
	src.SetKeychain(nil)
	if k, _ := src.APIKey("gemini-live"); k.Expose() != "g-fake-file" {
		t.Fatalf(".env after keychain cleared, got %q", k.Expose())
	}
	if got := src.Source("gemini-live"); got != SourceDotenv {
		t.Fatal(got)
	}
}

func TestBlankKeychainValuesCountAsMissing(t *testing.T) {
	path := writeDotenv(t, "GEMINI_API_KEY=g-fake-file\n")
	src := NewEnvSecretsWithLookup([]string{path}, noEnv)
	src.SetKeychain(map[string]string{"OPENAI_API_KEY": "   ", "GEMINI_API_KEY": ""})
	if _, ok := src.APIKey("openai-realtime"); ok {
		t.Fatal("blank keychain value counted")
	}
	if src.Source("openai-realtime") != SourceNone {
		t.Fatal("missing key has a source")
	}
	if k, _ := src.APIKey("gemini-live"); k.Expose() != "g-fake-file" {
		t.Fatal("blank keychain value hid the .env one")
	}
}

func TestSetKeychainReplacesTheSetAndIgnoresUnknownVariables(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, noEnv)
	src.SetKeychain(map[string]string{"OPENAI_API_KEY": "sk-fake-1", "PATH": "/evil"})
	if k, ok := src.APIKey("openai-realtime"); !ok || k.Expose() != "sk-fake-1" {
		t.Fatal("keychain value not used")
	}
	src.SetKeychain(map[string]string{"GEMINI_API_KEY": " g-fake-2 "})
	if _, ok := src.APIKey("openai-realtime"); ok {
		t.Fatal("old value survived a replace")
	}
	if k, _ := src.APIKey("gemini-live"); k.Expose() != "g-fake-2" {
		t.Fatalf("got %q", k.Expose())
	}
	if _, ok := src.APIKey("someone-else"); ok {
		t.Fatal("unknown provider got a key")
	}
}

func TestKeychainValuesAreSafeForConcurrentUse(t *testing.T) {
	src := NewEnvSecretsWithLookup(nil, noEnv)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			src.SetKeychain(map[string]string{"OPENAI_API_KEY": "sk-fake"})
		}
	}()
	for range 200 {
		src.APIKey("openai-realtime")
		src.Source("openai-realtime")
	}
	<-done
}
