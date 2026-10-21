package app

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func withMetricsLog(path string) harnessOpt {
	return func(d *Deps, _ *harness) { d.MetricsLogPath = path }
}

// #225: the logs keep to the Log size setting, read live.
func TestLogMaxBytesFollowsTheSetting(t *testing.T) {
	h := newHarness(t)
	if AppLogPath(h.dir) != filepath.Join(h.dir, "logs", "deskpixie.log") {
		t.Fatal(AppLogPath(h.dir))
	}
	if got := h.app.LogMaxBytes(); got != 2<<20 {
		t.Fatal(got)
	}
	h.set("logMaxMB", 5)
	if got := h.app.LogMaxBytes(); got != 5<<20 {
		t.Fatal(got)
	}
}

// Export Logs…: a zip of every log there is, and about.txt with what the shell knows.
func TestLogsExportZipsEveryLog(t *testing.T) {
	metrics := filepath.Join(t.TempDir(), "metrics.log")
	_ = os.WriteFile(metrics, []byte("m\n"), 0o644)
	h := newHarness(t, withMetricsLog(metrics))
	_ = os.MkdirAll(filepath.Join(h.dir, "logs"), 0o755)
	_ = os.WriteFile(AppLogPath(h.dir), []byte("app line\n"), 0o600)
	_ = os.WriteFile(filepath.Join(h.dir, "logs", "voice.log"), []byte("v\n"), 0o600)
	dst := filepath.Join(t.TempDir(), "DeskPixie logs.zip")
	out := h.call("logs.export", map[string]string{"path": dst, "app": "0.10.0 (42)", "os": "macOS 15.6"})
	if !strings.Contains(string(out), "DeskPixie logs.zip") {
		t.Fatal(string(out))
	}
	r, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	about := ""
	for _, f := range r.File {
		names = append(names, f.Name)
		if f.Name == "about.txt" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			about = string(b)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "about.txt,deskpixie.log,metrics.log,voice.log" {
		t.Fatal(names)
	}
	for _, want := range []string{"DeskPixie 0.10.0 (42)", "macOS 15.6", "Log size: 2 MB"} {
		if !strings.Contains(about, want) {
			t.Fatalf("about.txt lacks %q:\n%s", want, about)
		}
	}
}

func TestLogsExportNeedsAPath(t *testing.T) {
	h := newHarness(t)
	if _, err := h.callErr("logs.export", map[string]string{}); err == nil {
		t.Fatal("no error")
	}
}
