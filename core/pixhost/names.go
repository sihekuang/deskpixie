// Package pixhost is the daemon side of pix: sessions you start yourself (#89).
package pixhost

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/agent"
)

// MaxDescription is a description's length in runes.
const MaxDescription = 60

// AgentInterval is the least time between two accepted agent descriptions.
const AgentInterval = time.Minute

const keepPast = 5

// Address is `<repo>-<tool>`, then -2, -3…, not in taken (which holds worker names too).
func Address(gitRoot, tool string, taken []string) (string, bool) {
	repo := "repo"
	if gitRoot != "" {
		repo = agent.RepoSlug(filepath.Base(gitRoot))
	}
	for n := 1; n < 100; n++ {
		name := repo + "-" + tool
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		if agent.IsWorkerName(name) && !slices.Contains(taken, name) {
			return name, true
		}
	}
	return "", false
}

// Source is where a description came from; a higher one wins.
type Source int

const (
	SourceAuto Source = iota
	SourceAgent
	SourceUser
)

// Descriptions holds one session's description from each source.
type Descriptions struct {
	by        [3]string
	lastAgent time.Time
	past      []string
}

// Set records text from src; false when it was ignored (empty, or an agent update too soon).
func (d *Descriptions) Set(src Source, text string, now time.Time) bool {
	text = CleanDescription(text)
	if text == "" {
		return false
	}
	if src == SourceAgent && !d.lastAgent.IsZero() && now.Sub(d.lastAgent) < AgentInterval {
		return false
	}
	before := d.Current()
	d.by[src] = text
	if src == SourceAgent {
		d.lastAgent = now
	}
	d.remember(before)
	return true
}

// Clear drops src's description.
func (d *Descriptions) Clear(src Source) {
	before := d.Current()
	d.by[src] = ""
	d.remember(before)
}

// remember keeps before in the history when the current description moved on from it.
func (d *Descriptions) remember(before string) {
	after := d.Current()
	if before == "" || after == before {
		return
	}
	d.past = append([]string{before}, slices.DeleteFunc(d.past, func(s string) bool { return s == before || s == after })...)
	if len(d.past) > keepPast {
		d.past = d.past[:keepPast]
	}
}

// Current is the winning description, or "".
func (d *Descriptions) Current() string {
	for i := len(d.by) - 1; i >= 0; i-- {
		if d.by[i] != "" {
			return d.by[i]
		}
	}
	return ""
}

// Past is the earlier descriptions, newest first.
func (d *Descriptions) Past() []string { return slices.Clone(d.past) }

// CleanDescription is s on one line, spaces collapsed, at most MaxDescription runes.
func CleanDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxDescription {
		s = strings.TrimSpace(string([]rune(s)[:MaxDescription]))
	}
	return s
}

// AutoDescription is the prompt's first three content words (as worker names use).
func AutoDescription(prompt string) string { return CleanDescription(agent.TaskWords(prompt)) }
