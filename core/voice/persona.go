package voice

import (
	_ "embed"
	"os"
	"strings"
)

//go:embed assets/persona.md
var defaultPersona string

// DefaultPersona is Bit's built-in persona.
func DefaultPersona() string { return defaultPersona }

// Instructions returns the persona file's contents, or DefaultPersona when the
// path is empty, unreadable or blank.
func Instructions(personaPath string) string {
	if personaPath == "" {
		return DefaultPersona()
	}
	b, err := os.ReadFile(personaPath)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return DefaultPersona()
	}
	return string(b)
}
