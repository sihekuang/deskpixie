// Package pixhost is the daemon side of pix: sessions you start yourself (#89).
package pixhost

import (
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pix/describe"
)

// MaxDescription is a description's length in runes.
const MaxDescription = describe.Max

// AgentInterval is the least time between two agent descriptions that both go into the
// history; a quicker one replaces the last (an agent refining its first words).
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

// Set records text from src; false when it was ignored (empty, or not a description).
func (d *Descriptions) Set(src Source, text string, now time.Time) bool {
	text = CleanDescription(text)
	if text == "" {
		return false
	}
	before := d.Current()
	quick := src == SourceAgent && !d.lastAgent.IsZero() && now.Sub(d.lastAgent) < AgentInterval && before == d.by[SourceAgent]
	d.by[src] = text
	if src == SourceAgent {
		d.lastAgent = now
	}
	if !quick {
		d.remember(before)
	}
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

// Named is yours, else the agent's: a description someone wrote, not one pix guessed from a
// prompt; "" when there is none.
func (d *Descriptions) Named() string {
	if d.by[SourceUser] != "" {
		return d.by[SourceUser]
	}
	return d.by[SourceAgent]
}

// Past is the earlier descriptions, newest first.
func (d *Descriptions) Past() []string { return slices.Clone(d.past) }

// CleanDescription is s on one line, spaces collapsed, at most MaxDescription runes, or ""
// when it is a fragment of a message (describe.Clean).
func CleanDescription(s string) string { return describe.Clean(s) }
