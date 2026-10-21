package pix

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// #225: pix keeps its own log next to the app's, so an export has both sides of a session.
func TestTheLogIsInTheAppsLogsFolder(t *testing.T) {
	home := t.TempDir()
	env := func(string) string { return "" }
	want := filepath.Join(home, "Library", "Application Support", "com.sihekuang.deskpixie", "logs", "pix.log")
	if got := LogPath(home, env); got != want {
		t.Fatal(got)
	}
	// An isolated app (DESKPIXIE_SUPPORT_DIR) gets its pix's log too.
	iso := t.TempDir()
	if got := LogPath(home, func(k string) string {
		if k == "DESKPIXIE_SUPPORT_DIR" {
			return iso
		}
		return ""
	}); got != filepath.Join(iso, "logs", "pix.log") {
		t.Fatal(got)
	}
}

// The Log size setting is read from settings.json; missing, unreadable or 0 is the default.
func TestTheLogKeepsToTheLogSize(t *testing.T) {
	dir := t.TempDir()
	if got := logMax(dir); got != 2<<20 {
		t.Fatal(got)
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"logMaxMB": 5}`), 0o600)
	if got := logMax(dir); got != 5<<20 {
		t.Fatal(got)
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"logMaxMB": 0}`), 0o600)
	if got := logMax(dir); got != 2<<20 {
		t.Fatal(got)
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`not json`), 0o600)
	if got := logMax(dir); got != 2<<20 {
		t.Fatal(got)
	}
}

// Each line says which pix it came from; several run at once and share the file.
func TestLogLinesNameTheSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "pix.log")
	l := openLog(path, 2<<20, "claude")
	l.Printf("hello %d", 1)
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "pix "+strconv.Itoa(os.Getpid())+" claude: hello 1\n") {
		t.Fatalf("%q", b)
	}
	var none *Log
	none.Printf("ignored") // a nil log is off
}

// What a session does reaches the log: its commands (not the text you sent), status and end.
func TestASessionLogsWhatItDoes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pix.log")
	s := newSession(sessionOpts{tool: "claude", log: openLog(path, 2<<20, "claude")})
	s.client = &Client{}
	s.command(wire.Msg{Command: "nothing", Text: "private words"})
	s.setStatus("working")
	b, _ := os.ReadFile(path)
	for _, w := range []string{"command nothing: unknown command nothing", "status working"} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("missing %q in %q", w, b)
		}
	}
	if strings.Contains(string(b), "private words") {
		t.Fatal("the text went in the log")
	}
}

// A connection to DeskPixie that drops after registering is reported, so the log shows it.
func TestTheClientReportsADrop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = wire.NewDecoder(c).Next()
		_ = wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: "a"})
		c.Close()
	}()
	dropped := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister}, OnDropped: func() { dropped <- struct{}{} }}
	go c.Run(ctx)
	select {
	case <-dropped:
	case <-time.After(3 * time.Second):
		t.Fatal("no drop reported")
	}
}

// pix talks to the same DeskPixie it logs for: an isolated app's socket under
// DESKPIXIE_SUPPORT_DIR, or DESKPIXIE_PIX_SOCK when given.
func TestDaemonSocketFollowsTheAppsDataFolder(t *testing.T) {
	home := t.TempDir()
	iso, err := os.MkdirTemp("/tmp", "dp") // short enough for the socket to sit inside it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(iso) })
	env := map[string]string{"DESKPIXIE_SUPPORT_DIR": iso}
	if want := filepath.Join(iso, "agent", "pix.sock"); wire.SocketPath(iso, os.Getuid(), os.TempDir()) != want {
		t.Fatal("not a short path")
	}
	get := func(k string) string { return env[k] }
	if got := DaemonSocket(home, get); got != wire.SocketPath(iso, os.Getuid(), os.TempDir()) {
		t.Fatal(got)
	}
	env["DESKPIXIE_PIX_SOCK"] = "/x.sock"
	if got := DaemonSocket(home, get); got != "/x.sock" {
		t.Fatal(got)
	}
}
