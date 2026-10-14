package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Port of transport.rs tests (6).

// tempSocket is a fresh directory's herdr.sock. Unix socket paths are limited to 104 bytes on
// macOS, so it lives under a short temp path rather than t.TempDir().
func tempSocket(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp(shortTempRoot(), "shh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "herdr.sock")
}

func shortTempRoot() string {
	if d := os.TempDir(); len(d) < 60 {
		return d
	}
	return "/tmp"
}

func listen(t testing.TB, socket string) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// serveOnce is a one-shot fake Herdr: reads one request line, records it, answers reply.
func serveOnce(t testing.TB, socket, reply string) <-chan map[string]any {
	l := listen(t, socket)
	got := make(chan map[string]any, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Write([]byte(reply + "\n"))
		var v map[string]any
		json.Unmarshal([]byte(line), &v)
		got <- v
	}()
	return got
}

func TestSendsOneJSONLineAndReturnsTheResult(t *testing.T) {
	socket := tempSocket(t)
	server := serveOnce(t, socket, strings.TrimSpace(fixture(t, "workspace_list.json")))
	tr := NewUnixTransport(socket)
	result, err := tr.Request(context.Background(), "workspace.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	json.Unmarshal(result, &r)
	if r["type"] != "workspace_list" {
		t.Fatalf("%v", r)
	}
	sent := <-server
	if sent["method"] != "workspace.list" {
		t.Fatalf("%v", sent)
	}
	assertJSON(t, sent["params"], `{}`)
	if id, _ := sent["id"].(string); !strings.HasPrefix(id, "am_") {
		t.Fatalf("%v", sent["id"])
	}
}

func TestHerdrErrorsKeepTheirCode(t *testing.T) {
	socket := tempSocket(t)
	serveOnce(t, socket, strings.TrimSpace(fixture(t, "error_agent_not_found.json")))
	_, err := NewUnixTransport(socket).Request(context.Background(), "agent.get", map[string]any{"target": "nope"})
	want := RPCError{Code: "agent_not_found", Message: "agent target nope not found"}
	var got RPCError
	if !errors.As(err, &got) || got != want {
		t.Fatalf("%#v", err)
	}
}

func TestAMissingSocketIsUnavailable(t *testing.T) {
	_, err := NewUnixTransport(tempSocket(t)).Request(context.Background(), "ping", map[string]any{})
	if RPCCode(err) != "socket_unavailable" {
		t.Fatalf("%v", err)
	}
}

func TestASilentServerTimesOut(t *testing.T) {
	socket := tempSocket(t)
	l := listen(t, socket)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		time.Sleep(5 * time.Second)
		c.Close()
	}()
	tr := NewUnixTransport(socket).WithTimeout(100 * time.Millisecond)
	_, err := tr.Request(context.Background(), "ping", map[string]any{})
	if RPCCode(err) != "client_timeout" {
		t.Fatalf("%v", err)
	}
}

func TestAnOversizedReplyIsABadResponse(t *testing.T) {
	socket := tempSocket(t)
	l := listen(t, socket)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte(strings.Repeat("a", MaxReply+10)))
	}()
	_, err := NewUnixTransport(socket).Request(context.Background(), "ping", map[string]any{})
	if RPCCode(err) != "bad_response" {
		t.Fatalf("%v", err)
	}
}

func TestUnparseableLinesAreBadResponses(t *testing.T) {
	if _, err := ParseResponse("nope"); RPCCode(err) != "bad_response" {
		t.Fatalf("%v", err)
	}
	if _, err := ParseResponse(`{"id":"x"}`); RPCCode(err) != "bad_response" {
		t.Fatalf("%v", err)
	}
}
