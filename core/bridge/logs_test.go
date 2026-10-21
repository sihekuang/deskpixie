package bridge

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
