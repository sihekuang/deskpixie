package wire

import (
	"bytes"
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
