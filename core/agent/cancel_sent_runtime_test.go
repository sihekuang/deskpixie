package agent_test

import (
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

// #332: jobs cancel of a job already closed, its request typed into your session, stops that
// session's turn the way its stop button does.
func TestCancellingADoneHandOffStopsYourSession(t *testing.T) {
	r, user := pixRig(t)
	id, err := r.h.Delegate("tell the landing session to add the Codex row", "")
	if err != nil {
		t.Fatal(err)
	}
	// The request is with the coordinator before the hand-off is reported.
	if !eventually(func() bool { return len(r.host.Prompts()) > 0 }) {
		t.Fatal("never sent")
	}
	r.h.Input(SessionSentInput("focus-claude", "deskpixie-landing"))
	if _, err := r.h.Cancel(id.String()); err != nil {
		t.Fatal(err)
	}
	user.SetStatus("focus-claude", PaneWorking)
	out := invoke(t, Jobs{H: r.h}, `{"action":"cancel","job_id":"`+id.String()+`"}`).(map[string]any)
	if out["status"] != "stopped" || out["session"] != "focus-claude" {
		t.Fatal(out)
	}
	if n := escapes(user.Keys(), "focus-claude"); n != 1 {
		t.Fatal(user.Keys())
	}
	// Idle by the next one: nothing to stop, and said so.
	user.SetStatus("focus-claude", PaneIdle)
	out = invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`).(map[string]any)
	if out["status"] != "not working" || escapes(user.Keys(), "focus-claude") != 1 {
		t.Fatal(out, user.Keys())
	}
}

func TestCancellingWithNoHandOffStillSaysNoJob(t *testing.T) {
	r, _ := pixRig(t)
	out := invoke(t, Jobs{H: r.h}, `{"action":"cancel","job_id":"job_000000000009"}`).(map[string]any)
	if out["error"] != "No open job job_000000000009." {
		t.Fatal(out)
	}
}
