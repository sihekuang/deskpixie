package bridge

import (
	"os"
	"testing"
)

// TestMain gives the package a throwaway home: the engine these tests start runs the agent,
// which creates the projects folder below home and pre-trusts its workspace in
// ~/.claude.json (#216). Neither may ever reach the real ones.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "bridge-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
