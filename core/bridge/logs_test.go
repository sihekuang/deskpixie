package bridge

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #225: once configured, the core's log also goes to logs/deskpixie.log; after Close it goes
// back to stderr alone.
func TestTheCoresLogGoesToTheAppLog(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, Options{})
	if err := e.Configure(cfgIn(t, dir, "")); err != nil {
		t.Fatal(err)
	}
	log.Printf("bridge test line %s", t.Name())
	b, _ := os.ReadFile(filepath.Join(dir, "logs", "deskpixie.log"))
	if !strings.Contains(string(b), "bridge test line "+t.Name()) {
		t.Fatalf("%q", b)
	}
	// What the app logs while it starts (the canvas watcher) is kept too.
	if !strings.Contains(string(b), "canvas: watching") {
		t.Fatalf("startup lines missing: %q", b)
	}
	e.Close()
	log.Printf("after close %s", t.Name())
	b, _ = os.ReadFile(filepath.Join(dir, "logs", "deskpixie.log"))
	if strings.Contains(string(b), "after close") {
		t.Fatal("still logging to a closed app's file")
	}
}

// The export knows the metrics log.
func TestTheExportIncludesTheMetricsLog(t *testing.T) {
	dir := t.TempDir()
	metrics := filepath.Join(t.TempDir(), "metrics.log")
	_ = os.WriteFile(metrics, []byte("m\n"), 0o644)
	e := newEngine(t, Options{MetricsLogPath: metrics})
	if err := e.Configure(cfgIn(t, dir, "")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "logs.zip")
	out := e.Call("logs.export", []byte(`{"path":"`+dst+`","app":"t","os":"t"}`))
	if fi, err := os.Stat(dst); err != nil || fi.Size() == 0 {
		t.Fatal(string(out), err)
	}
}

// The metrics log lives with the other logs in the app's data folder, so an isolated app
// (DESKPIXIE_SUPPORT_DIR) never writes the real user's; nothing is written before Configure.
func TestTheMetricsLogIsInTheAppsLogsFolder(t *testing.T) {
	dir := t.TempDir()
	e := New(Options{MetricsInterval: 20 * time.Millisecond, Secrets: noSecrets, Agent: NoAgent,
		Run: func(string, ...string) error { return nil }})
	t.Cleanup(e.Close)
	time.Sleep(80 * time.Millisecond)
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, "Library", "Logs", "DeskPixie")); err == nil {
		t.Fatal("wrote under HOME")
	}
	if err := e.Configure(cfgIn(t, dir, "")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "logs", "metrics.log"))
		return len(b) > 0
	})
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A Configure that fails leaves the running app's log where it was.
func TestAFailedConfigureKeepsTheAppLog(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, Options{})
	if err := e.Configure(cfgIn(t, dir, "")); err != nil {
		t.Fatal(err)
	}
	// No pixie pack anywhere: the new app can't be built.
	if err := e.Configure([]byte(`{"appSupport":"` + t.TempDir() + `","bundledPixies":"` + t.TempDir() + `"}`)); err == nil {
		t.Fatal("the bad config was accepted")
	}
	log.Printf("after a failed configure %s", t.Name())
	b, _ := os.ReadFile(filepath.Join(dir, "logs", "deskpixie.log"))
	if !strings.Contains(string(b), "after a failed configure") {
		t.Fatalf("the log stopped: %q", b)
	}
}
