package agenttest

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Port of the tests in ai-mascot crates/agent-core/src/conformance.rs (6).

func spec() agent.AgentSpec {
	return agent.AgentSpec{Name: "probe", Kind: agent.Claude, Cwd: "/tmp"}
}

func TestTheFakeSessionHostConforms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := NewFakeSessionHost().HoldTurns()
		steps, err := RunConformance(context.Background(), host, spec(), Timeouts{Turn: time.Second, Settle: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != 7 {
			t.Fatalf("%v", steps)
		}
	})
}

func caught(t *testing.T, fault Fault) string {
	var msg string
	synctest.Test(t, func(t *testing.T) {
		host := NewFakeSessionHost().HoldTurns()
		host.Inject(fault)
		_, err := RunConformance(context.Background(), host, spec(), Timeouts{Turn: 150 * time.Millisecond, Settle: 10 * time.Millisecond})
		if err == nil {
			t.Fatal("the suite should reject this host")
		}
		msg = err.Error()
	})
	return msg
}

func TestAReplayedExitedIsCaught(t *testing.T) {
	if e := caught(t, ReplayExitedOnSubscribe); !strings.Contains(e, "duplicate Exited") {
		t.Fatal(e)
	}
}

func TestADoubleExitedIsCaught(t *testing.T) {
	if e := caught(t, DoubleExitOnClose); !strings.Contains(e, "duplicate Exited") {
		t.Fatal(e)
	}
}

func TestAnIgnoredEscapeIsCaught(t *testing.T) {
	if e := caught(t, IgnoreEscape); !strings.Contains(e, "Escape") {
		t.Fatal(e)
	}
}

func TestEnsureSessionKillingPanesIsCaught(t *testing.T) {
	if e := caught(t, EnsureKillsPanes); !strings.Contains(e, "missing from list") {
		t.Fatal(e)
	}
}

func TestAHostThatNeverHoldsATurnFailsTheEscapeCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := NewFakeSessionHost()
		_, err := RunConformance(context.Background(), host, spec(), Timeouts{Turn: 150 * time.Millisecond, Settle: 10 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "Working") {
			t.Fatal(err)
		}
	})
}
