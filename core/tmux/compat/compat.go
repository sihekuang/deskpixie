// Package compat names the tmux versions the tmux session host supports and reads `tmux -V`.
// It is a leaf (no imports from the app) so the settings help text and the adapter share one
// label.
package compat

import (
	"strconv"
	"strings"
)

// SupportedVersions is the label settings and the version-mismatch line show. 3.2 brought
// what the host relies on: `new-window -e` and the pane options it sets.
const SupportedVersions = "tmux 3.2 or newer"

// MinMajor and MinMinor are the oldest supported release.
const (
	MinMajor = 3
	MinMinor = 2
)

// Version is a release read from `tmux -V`; Known false when it names no number (a build from
// master, or a vendor label).
type Version struct {
	Major, Minor int
	Known        bool
}

// Parse reads `tmux -V`'s output: "tmux 3.3a", "tmux next-3.6", "tmux master".
func Parse(out string) Version {
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "tmux" {
		return Version{}
	}
	v := strings.TrimPrefix(f[1], "next-")
	majorText, rest, ok := strings.Cut(v, ".")
	if !ok {
		return Version{}
	}
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return Version{}
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	minor, err := strconv.Atoi(rest[:end])
	if err != nil {
		return Version{}
	}
	return Version{Major: major, Minor: minor, Known: true}
}

// Supported: v is 3.2 or newer, or doesn't say.
func Supported(v Version) bool {
	if !v.Known {
		return true
	}
	return v.Major > MinMajor || (v.Major == MinMajor && v.Minor >= MinMinor)
}
