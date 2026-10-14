package agentfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func parseJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func projectEntry(t *testing.T, v map[string]any, key string) map[string]any {
	t.Helper()
	e, _ := v["projects"].(map[string]any)[key].(map[string]any)
	return e
}

func TestGrantsMergeIntoExistingKeysAndKeepTheMode(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent/workspace")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	mustWrite(t, config, `{"numStartups":3,"projects":{"/other":{"allowedTools":[]}}}`)
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewClaudeTrustStore(config, []string{filepath.Join(d, "agent")})
	got, err := store.Grant(ws)
	if err != nil || !reflect.DeepEqual(got, []string{ws}) {
		t.Fatal(got, err)
	}
	v := parseJSON(t, mustRead(t, config))
	if v["numStartups"] != float64(3) {
		t.Fatal(v)
	}
	if !reflect.DeepEqual(v["projects"].(map[string]any)["/other"], map[string]any{"allowedTools": []any{}}) {
		t.Fatal(v)
	}
	if projectEntry(t, v, ws)["hasTrustDialogAccepted"] != true {
		t.Fatal(v)
	}
	if m := mode(t, config); m != 0o600 {
		t.Fatalf("%o", m)
	}
}

func TestNothingOutsideTheAllowedRootIsTrusted(t *testing.T) {
	d := tempDir(t)
	config := filepath.Join(d, ".claude.json")
	store := NewClaudeTrustStore(config, []string{filepath.Join(d, "agent")})
	mustMkdir(t, filepath.Join(d, "elsewhere"))
	if _, err := store.Grant(filepath.Join(d, "elsewhere")); err == nil {
		t.Fatal("expected an error")
	}
	if exists(config) {
		t.Fatal("nothing written")
	}
}

func TestASymlinkedConfigIsWrittenThrough(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	real := filepath.Join(d, "real.json")
	mustWrite(t, real, "{}")
	link := filepath.Join(d, ".claude.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaudeTrustStore(link, []string{ws}).Grant(ws); err != nil {
		t.Fatal(err)
	}
	if m, _ := os.Lstat(link); m.Mode()&os.ModeSymlink == 0 {
		t.Fatal("still a link")
	}
	if !strings.Contains(mustRead(t, real), "hasTrustDialogAccepted") {
		t.Fatal("not written through")
	}
}

func TestAnUnreadableConfigIsLeftUntouched(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	mustWrite(t, config, `{"precious":true}`)
	if err := os.Chmod(config, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(config); err == nil {
		t.Skip("running as root: skipping the mode-000 case")
	}
	if _, err := NewClaudeTrustStore(config, []string{ws}).Grant(ws); err == nil {
		t.Fatal("expected an error")
	}
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, config); got != `{"precious":true}` {
		t.Fatal(got)
	}
	if n := count(t, d); n != 2 {
		t.Fatalf("temp file left: %d entries", n)
	}
}

func TestAnInvalidUTF8OrNonJSONConfigIsLeftUntouched(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	for _, bytes := range []string{"{\"a\":\"\xff\xfe\"}", "{ not json", "[1,2]"} {
		mustWrite(t, config, bytes)
		if _, err := NewClaudeTrustStore(config, []string{ws}).Grant(ws); err == nil {
			t.Fatalf("%q: expected an error", bytes)
		}
		if got := mustRead(t, config); got != bytes {
			t.Fatalf("%q changed to %q", bytes, got)
		}
	}
}

func TestADanglingSymlinkIsLeftAsIs(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	link := filepath.Join(d, ".claude.json")
	if err := os.Symlink(filepath.Join(d, "missing.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaudeTrustStore(link, []string{ws}).Grant(ws); err == nil {
		t.Fatal("expected an error")
	}
	if m, _ := os.Lstat(link); m.Mode()&os.ModeSymlink == 0 {
		t.Fatal("still a link")
	}
	if exists(filepath.Join(d, "missing.json")) {
		t.Fatal("nothing created")
	}
}

func TestAConfigChangedMidGrantIsMergedNotClobbered(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	mustWrite(t, config, `{"numStartups":1}`)
	var calls atomic.Int32
	store := NewClaudeTrustStore(config, []string{ws})
	store.beforeWrite = func(target string) {
		if calls.Add(1)-1 == 0 {
			mustWrite(t, target, `{"numStartups":2,"claudeWroteThis":true}`)
		}
	}
	if _, err := store.Grant(ws); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("merged %d times, want 2", calls.Load())
	}
	v := parseJSON(t, mustRead(t, config))
	if v["claudeWroteThis"] != true || v["numStartups"] != float64(2) || projectEntry(t, v, ws)["hasTrustDialogAccepted"] != true {
		t.Fatal(v)
	}
}

func TestAConfigThatKeepsChangingIsNotWritten(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	mustWrite(t, config, "{}")
	var n atomic.Int32
	store := NewClaudeTrustStore(config, []string{ws})
	store.beforeWrite = func(target string) {
		i := n.Add(1) - 1
		mustWrite(t, target, `{"n":`+string(rune('0'+i))+`}`)
	}
	if _, err := store.Grant(ws); err == nil {
		t.Fatal("expected an error")
	}
	if got := mustRead(t, config); got != `{"n":1}` {
		t.Fatalf("the other writer's text must survive: %s", got)
	}
	if c := count(t, d); c != 2 {
		t.Fatalf("temp file left: %d entries", c)
	}
}

func TestANewConfigIsPrivate(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	if _, err := NewClaudeTrustStore(config, []string{ws}).Grant(ws); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, config); m != 0o600 {
		t.Fatalf("%o", m)
	}
}

func TestAlreadyTrustedMeansNoWrite(t *testing.T) {
	d := tempDir(t)
	ws := filepath.Join(d, "agent")
	mustMkdir(t, ws)
	config := filepath.Join(d, ".claude.json")
	store := NewClaudeTrustStore(config, []string{ws})
	if _, err := store.Grant(ws); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(config)
	time.Sleep(20 * time.Millisecond)
	if _, err := store.Grant(ws); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(config)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("rewritten")
	}
}
