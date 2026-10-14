package voice

// API keys from the environment, falling back to .env files. Never logs values.
// Port of ai-mascot crates/voice-core/src/env_secrets.rs (+ the SecretSource trait).

import (
	"os"
	"slices"
	"strings"
)

// SecretSource finds a provider's API key.
type SecretSource interface {
	APIKey(provider string) (Secret, bool)
	// KeyHint names what the user should set when APIKey finds nothing (e.g.
	// "GEMINI_API_KEY"), for the "no API key" status line; "" when unknown. Never the key.
	KeyHint(provider string) string
}

// providerKeys: provider registry name → the environment variable that holds its key.
var providerKeys = [...][2]string{{"openai-realtime", "OPENAI_API_KEY"}, {"gemini-live", "GEMINI_API_KEY"}, {"openai-live", "OPENAI_API_KEY"}}

// VarFor is the environment variable holding provider's key.
func VarFor(provider string) (string, bool) {
	for _, p := range providerKeys {
		if p[0] == provider {
			return p[1], true
		}
	}
	return "", false
}

// KeyVars lists every provider key variable once, e.g. for scrubbing child-process
// environments.
func KeyVars() []string {
	out := make([]string, 0, len(providerKeys))
	for _, p := range providerKeys {
		if !slices.Contains(out, p[1]) {
			out = append(out, p[1])
		}
	}
	return out
}

// EnvSecrets looks in the process environment first, then in each .env file in order.
type EnvSecrets struct {
	dotenvPaths []string
	env         func(string) (string, bool)
}

// NewEnvSecrets reads the real environment, then dotenvPaths.
func NewEnvSecrets(dotenvPaths []string) *EnvSecrets {
	return NewEnvSecretsWithLookup(dotenvPaths, os.LookupEnv)
}

// NewEnvSecretsWithLookup replaces the environment lookup (tests).
func NewEnvSecretsWithLookup(dotenvPaths []string, env func(string) (string, bool)) *EnvSecrets {
	return &EnvSecrets{dotenvPaths: dotenvPaths, env: env}
}

// APIKey implements SecretSource. Blank values count as missing.
func (s *EnvSecrets) APIKey(provider string) (Secret, bool) {
	name, ok := VarFor(provider)
	if !ok {
		return Secret{}, false
	}
	if v, ok := s.env(name); ok && strings.TrimSpace(v) != "" {
		return NewSecret(strings.TrimSpace(v)), true
	}
	for _, p := range s.dotenvPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		found := false
		var v string
		for _, kv := range ParseDotenv(string(b)) {
			if kv.Key == name {
				v, found = kv.Value, true
				break
			}
		}
		if !found {
			continue
		}
		// Like Rust: the first file that defines the variable decides, even if blank.
		if strings.TrimSpace(v) == "" {
			return Secret{}, false
		}
		return NewSecret(strings.TrimSpace(v)), true
	}
	return Secret{}, false
}

// KeyHint implements SecretSource.
func (s *EnvSecrets) KeyHint(provider string) string {
	v, _ := VarFor(provider)
	return v
}

// Has reports whether provider has a key (for the settings "found/missing" rows).
func (s *EnvSecrets) Has(provider string) bool {
	_, ok := s.APIKey(provider)
	return ok
}

// DotenvPaths is where the app looks for a .env, in order: the working directory, then the
// app-data dir (when given). Rust also checks the workspace root in debug builds; a Go
// binary has no equivalent, so that entry is omitted.
func DotenvPaths(appDataDir string) []string {
	paths := []string{".env"}
	if appDataDir != "" {
		paths = append(paths, appDataDir+string(os.PathSeparator)+".env")
	}
	return paths
}

// DotenvPair is one KEY=value line.
type DotenvPair struct{ Key, Value string }

// ParseDotenv reads KEY=value lines; supports a leading BOM, "export ", # comments
// (whole-line, or after whitespace on unquoted values) and single/double quotes.
func ParseDotenv(text string) []DotenvPair {
	text = strings.TrimPrefix(text, "\ufeff")
	var out []DotenvPair
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsFunc(k, isSpace) {
			continue
		}
		out = append(out, DotenvPair{k, parseDotenvValue(strings.TrimSpace(v))})
	}
	return out
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\v' || r == '\f'
}

// A quoted value is the text up to its closing quote; an unquoted one ends at the first
// whitespace-then-# and is trimmed.
func parseDotenvValue(v string) string {
	for _, q := range []string{`"`, `'`} {
		if rest, ok := strings.CutPrefix(v, q); ok {
			if end := strings.Index(rest, q); end >= 0 {
				return rest[:end]
			}
		}
	}
	prevWS := false
	for i, c := range v {
		if c == '#' && prevWS {
			return strings.TrimSpace(v[:i])
		}
		prevWS = isSpace(c)
	}
	return v
}

// fixedSecrets is the RuntimeConfig.APIKey fallback: one key for any provider.
type fixedSecrets struct {
	key  string
	hint string
}

func (f fixedSecrets) APIKey(string) (Secret, bool) {
	return NewSecret(f.key), f.key != ""
}
func (f fixedSecrets) KeyHint(string) string { return f.hint }
