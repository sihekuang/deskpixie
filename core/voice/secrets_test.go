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
	for _, p := range []string{"openai-realtime", "gemini-live"} {
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
