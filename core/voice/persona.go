package voice

import (
	_ "embed"
	"os"
	"strings"
)

//go:embed assets/persona.md
var defaultPersona string

// DefaultPersona is Bit's built-in persona, trimmed (Rust: DEFAULT_PERSONA.trim()).
func DefaultPersona() string { return strings.TrimSpace(defaultPersona) }

// Instructions returns the persona file's trimmed contents, or DefaultPersona when the
// path is empty, unreadable or blank.
func Instructions(personaPath string) string {
	if personaPath == "" {
		return DefaultPersona()
	}
	b, err := os.ReadFile(personaPath)
	text := strings.TrimSpace(string(b))
	if err != nil || text == "" {
		return DefaultPersona()
	}
	return text
}
