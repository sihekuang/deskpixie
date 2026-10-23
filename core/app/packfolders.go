package app

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/sihekuang/deskpixie/core/packs"
	"github.com/sihekuang/deskpixie/core/settings"
)

// UserPacksDir is the user's pack folder under AppSupport; LegacyUserPacksDir is its name before
// the pixie rename (#68).
const (
	UserPacksDir       = "pixies"
	LegacyUserPacksDir = "mascots"
)

// UserPackRoots moves <appSupport>/mascots to <appSupport>/pixies when pixies/ doesn't exist yet
// (the #68 rename, once: a second run finds pixies/ and does nothing), makes sure pixies/
// exists, and returns the user pack roots to read: a mascots/ that is still there (both
// existed) first, then pixies/, so a pack in pixies/ wins over one with the same id.
func UserPackRoots(appSupport string) []string {
	cur := filepath.Join(appSupport, UserPacksDir)
	old := filepath.Join(appSupport, LegacyUserPacksDir)
	if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
		if m, err := os.Lstat(old); err == nil && m.IsDir() {
			if err := os.Rename(old, cur); err != nil {
				log.Printf("app: moving %s to %s: %v", old, cur, err)
			} else {
				log.Printf("app: moved the user's packs from %s to %s", old, cur)
			}
		}
	}
	if err := os.MkdirAll(cur, 0o755); err != nil {
		log.Printf("app: creating user pixies folder: %v", err)
	}
	var roots []string
	if m, err := os.Stat(old); err == nil && m.IsDir() {
		roots = append(roots, old)
	}
	return append(roots, cur)
}

// PackRoots are the pack roots to read, in order, and the user's pixies folder (#266): the
// bundled pixies, then the pixiesFolder setting when it is a folder, else UserPackRoots. A
// chosen folder that has gone falls back to <AppSupport>/pixies.
func PackRoots(bundled, appSupport string, folder *string, home string) (roots []string, user string) {
	if bundled != "" {
		roots = append(roots, bundled)
	}
	if folder != nil {
		if dir, ok := settings.PixiesFolder(*folder, home); ok {
			return append(roots, dir), dir
		}
		log.Printf("app: the pixies folder %q is not a folder; reading %s", *folder, UserPacksDir)
	}
	users := UserPackRoots(appSupport)
	return append(roots, users...), users[len(users)-1]
}

// The tray shows at most MaxPackProblems pixies that didn't load, each cut to MaxProblemLen
// characters, so a folder full of other things doesn't fill the menu.
const (
	MaxPackProblems = 5
	MaxProblemLen   = 120
)

// PackProblems are the tray's lines for the pixies that didn't load: "<id> didn't load:
// <why>", then "N more didn't load" past MaxPackProblems.
func PackProblems(rs []*packs.Rejected) []string {
	var out []string
	for i, r := range rs {
		if i == MaxPackProblems {
			return append(out, fmt.Sprintf("%d more didn't load", len(rs)-i))
		}
		line := []rune(fmt.Sprintf("%s didn't load: %v", r.ID, r.Err))
		if len(line) > MaxProblemLen {
			line = append(line[:MaxProblemLen-1], '…')
		}
		out = append(out, string(line))
	}
	return out
}
