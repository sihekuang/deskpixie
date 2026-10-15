package agent_test

import (
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

func TestRuntimeRoutesSessionToolsEvenWithWorkersOff(t *testing.T) {
	r := wRigWith(t, false)
	got := call(r, Coordinator, McpCall{Kind: CallListSessions})
	if got.Err != nil {
		t.Fatalf("list_sessions must not depend on workers: %v", got.Err)
	}
	if rows, _ := got.Value.(map[string]any)["sessions"].([]SessionRow); rows == nil {
		t.Fatalf("%+v", got.Value)
	}
}

func TestRuntimeSessionToolsAreTheCoordinators(t *testing.T) {
	r := wRigWith(t, true)
	wantErr(t, call(r, "focus-fix", McpCall{Kind: CallListSessions}), "This tool is not available to you.")
}
