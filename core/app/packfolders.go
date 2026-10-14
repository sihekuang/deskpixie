package app

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
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
