// Package compat names the Herdr versions the herdr adapter supports, by socket protocol. It
// is a leaf (no imports from the app) so the settings help text and the adapter share one
// label.
package compat

import (
	"fmt"
	"strings"
)

// Version is one Herdr release line the adapter speaks, by the socket protocol its CLI and
// server report in `herdr status --json`.
type Version struct {
	Protocol uint32
	// Release is the Herdr version (or line) as the user sees it ("0.7.5", "0.9.x").
	Release string
}

// Supported is every Herdr version the adapter speaks, oldest first. The CLI and the running
// server must report the same protocol, and it must be one of these.
var Supported = []Version{
	{Protocol: 17, Release: "0.7.5"},
	{Protocol: 22, Release: "0.9.x"},
}

// SupportedVersions is Supported as the user reads it (status line, settings help, docs).
// TestTheLabelNamesEverySupportedVersion keeps it in step with the table.
const SupportedVersions = "Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)"

// Find is the supported version speaking protocol p.
func Find(p uint32) (Version, bool) {
	for _, v := range Supported {
		if v.Protocol == p {
			return v, true
		}
	}
	return Version{}, false
}

// IsSupported: the adapter speaks protocol p.
func IsSupported(p uint32) bool {
	_, ok := Find(p)
	return ok
}

// Describe is "Herdr A (protocol a), B (protocol b) or C (protocol c)".
func Describe(versions []Version) string {
	parts := make([]string, len(versions))
	for i, v := range versions {
		parts[i] = fmt.Sprintf("%s (protocol %d)", v.Release, v.Protocol)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return "Herdr " + parts[0]
	}
	return "Herdr " + strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}
