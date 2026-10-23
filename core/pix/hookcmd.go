package pix

import (
	"io"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// hookWait: how long a hook waits for the parent pix's reply.
var hookWait = 4 * time.Second

// RunHook is `pix hook <event> [payload]`: forward the hook's JSON to the parent pix and print
// its reply. The JSON is the payload argument when given (Codex's notify), else stdin (Claude's
// hooks). Always exits 0: a missing parent must never block or fail the agent.
func RunHook(event, sock, payload string, stdin io.Reader, stdout io.Writer) int {
	if payload == "" {
		raw, _ := io.ReadAll(io.LimitReader(stdin, wire.MaxLine))
		payload = string(raw)
	}
	wait := hookWait
	if holds(event) {
		wait += MaxHold // its question may wait for Pixie's answer (#268)
	}
	reply, err := askLocal(sock, wire.Msg{Kind: wire.KindEvent, Event: "hook", Status: event, Text: payload}, wait)
	if err == nil && reply.Text != "" {
		io.WriteString(stdout, reply.Text)
	}
	return 0
}
