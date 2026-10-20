package wire

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Msg{Kind: KindRegister, Tool: "claude", Pid: 42, Cwd: "/r", Description: "x"}
	if err := Encode(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("one line expected: %q", buf.String())
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatal(err)
	}
	if got.V != Version || got.Tool != "claude" || got.Pid != 42 || got.Description != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestDecoderRejectsHugeLine(t *testing.T) {
	big := strings.Repeat("a", 2<<20)
	_, err := NewDecoder(strings.NewReader(`{"v":1,"text":"` + big + `"}` + "\n")).Next()
	if err == nil {
		t.Fatal("want error for > 1 MiB line")
	}
}

func TestSocketPathFallsBackWhenLong(t *testing.T) {
	short := SocketPath("/Users/a/Library/Application Support/x", 501, "/tmp/")
	if short != "/Users/a/Library/Application Support/x/agent/pix.sock" {
		t.Fatalf("short: %s", short)
	}
	long := SocketPath("/"+strings.Repeat("d", 120), 501, "/var/folders/T/")
	if long != "/var/folders/T/deskpixie-501/pix.sock" {
		t.Fatalf("long: %s", long)
	}
}

func TestTurnDurationRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	Encode(&buf, Msg{Kind: KindEvent, Event: "turn", Text: "done", DurationMs: 42_000, Source: "you"})
	got, err := NewDecoder(&buf).Next()
	if err != nil || got.DurationMs != 42_000 || got.Text != "done" || got.Source != "you" {
		t.Fatalf("%+v %v", got, err)
	}
}

// #189: a pix build is the sum of its binary: the same file, the same build; any other
// binary, another; none readable, "".
func TestBuildOfIsTheBinarysSum(t *testing.T) {
	dir := t.TempDir()
	a, b, link := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "link")
	os.WriteFile(a, []byte("pix one"), 0o755)
	os.WriteFile(b, []byte("pix two"), 0o755)
	os.Symlink(a, link)
	if BuildOf(a) == "" || BuildOf(a) != BuildOf(link) {
		t.Fatal(BuildOf(a), BuildOf(link))
	}
	if BuildOf(a) == BuildOf(b) {
		t.Fatal("two binaries, one build")
	}
	if BuildOf(filepath.Join(dir, "missing")) != "" {
		t.Fatal("a missing binary has a build")
	}
}

func TestRegisterCarriesTheBuild(t *testing.T) {
	var buf bytes.Buffer
	Encode(&buf, Msg{Kind: KindRegister, Build: "abc"})
	Encode(&buf, Msg{Kind: KindRegistered, Address: "x", Outdated: true})
	d := NewDecoder(&buf)
	if m, _ := d.Next(); m.Build != "abc" {
		t.Fatalf("%+v", m)
	}
	if m, _ := d.Next(); !m.Outdated {
		t.Fatalf("%+v", m)
	}
}
