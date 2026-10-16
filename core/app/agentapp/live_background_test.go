//go:build live

package agentapp_test

// Opt-in live check that a coordinator waiting on its background agent still takes requests,
// with the app checking that each prompt lands (#127, #128):
//
//	go test -tags live -run LiveBackground -v -timeout 15m ./app/agentapp/
//
// The real coordinator (haiku) in the Herdr smoke session ("deskpixie-smoke") starts a
// background agent that sleeps for minutes and ends its turn; Herdr then reports it working
// at its idle prompt. A second job must reach it and be answered while that agent sleeps,
// without being taken for lost and sent again. (The flicker at a turn's end that held the old
// inbox shut in #127 did not happen in these runs: the unit tests cover it.)

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
)

func TestLiveBackgroundAJobReachesACoordinatorWaitingOnItsBackgroundAgent(t *testing.T) {
	a, v, sm, _ := holdApp(t, "")
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sm.Supervisor(t))
	t.Cleanup(host.Shutdown)
	status := func() agent.PaneStatus {
		list, _ := host.List(context.Background())
		if i := slices.IndexFunc(list, func(a agent.AgentInfo) bool { return a.Name == agent.Coordinator }); i >= 0 {
			return list[i].Status
		}
		return agent.PaneUnknown
	}
	const sleep = 4 * time.Minute
	tool(t, a, "delegate", `{"task":"Use the Agent tool with run_in_background set to true to start one subagent whose only task is to run the shell command sleep 240 with Bash and then report done. Do not wait for it: as soon as it has started, reply that it started."}`)
	v.waitFor(t, "the coordinator's reply that the agent started", 3*time.Minute, func() bool {
		return v.since(0, "inject: Coordinator") >= 0
	})
	started := time.Now()
	t.Logf("ok   %s", v.all()[v.since(0, "inject: Coordinator")])

	// Herdr reports it working, at its idle prompt, while the agent sleeps.
	v.waitFor(t, "Herdr reporting the waiting coordinator working", time.Minute, func() bool { return status() == agent.PaneWorking })
	time.Sleep(5 * time.Second)
	if st := status(); st != agent.PaneWorking {
		t.Fatalf("Herdr reports %v once the turn is over", st)
	}
	t.Logf("ok   Herdr reports the coordinator working %v after its turn", time.Since(started).Round(time.Second))

	mark := len(v.all())
	tool(t, a, "delegate", `{"task":"Reply with only the word pear."}`)
	// The reply among whatever else the coordinator says meanwhile (progress, notes).
	pear := func() int {
		for i, e := range v.all() {
			if i >= mark && strings.HasPrefix(e, "inject: Coordinator") && strings.Contains(strings.ToLower(e), "pear") {
				return i
			}
		}
		return -1
	}
	v.waitFor(t, "the second job's reply while the background agent sleeps", 2*time.Minute, func() bool { return pear() >= 0 })
	if waited := time.Since(started); waited >= sleep {
		t.Fatalf("answered only after %v: the background agent had finished", waited)
	}
	t.Logf("ok   answered %v after the background agent started: %s", time.Since(started).Round(time.Second), v.all()[pear()])
}
