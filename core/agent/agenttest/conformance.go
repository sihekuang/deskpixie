package agenttest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// What every SessionHost must do (spec §11.3, Liskov). Runs on FakeSessionHost in `go test` and
// can run on Herdr in an opt-in smoke run (a throwaway session).
// Port of ai-mascot crates/agent-core/src/conformance.rs.

// Timeouts bound the conformance checks.
type Timeouts struct {
	// Turn: how long an agent may take to start working on a prompt, and to finish a short turn.
	Turn time.Duration
	// Settle: how long to watch for a replayed duplicate Exited.
	Settle time.Duration
}

// RunConformance runs the checks with an agent started from spec. The agent must answer a
// one-line prompt and must stop when it receives Escape. Returns the steps that passed.
func RunConformance(ctx context.Context, host agent.SessionHost, spec agent.AgentSpec, t Timeouts) ([]string, error) {
	var steps []string
	name := spec.Name
	if err := host.EnsureSession(ctx); err != nil {
		return steps, fmt.Errorf("ensure_session: %v", err)
	}
	if err := host.EnsureSession(ctx); err != nil {
		return steps, fmt.Errorf("ensure_session (again): %v", err)
	}
	steps = append(steps, "ensure_session is idempotent")

	events := host.Subscribe()
	if _, err := host.StartAgent(ctx, spec); err != nil {
		return steps, fmt.Errorf("start_agent: %v", err)
	}
	if err := host.EnsureSession(ctx); err != nil {
		return steps, fmt.Errorf("ensure_session (after start): %v", err)
	}
	if err := expectListed(ctx, host, name, "start_agent / ensure_session"); err != nil {
		return steps, err
	}
	steps = append(steps, "start_agent → list shows it, and a later ensure_session keeps it")

	if err := host.Prompt(ctx, "no-such-agent", "hi"); !agent.IsHostError(err, agent.HostNoAgent) {
		return steps, fmt.Errorf("prompt to a missing agent should be NoAgent, got %v", err)
	}
	if err := host.Close(ctx, "no-such-agent"); !agent.IsHostError(err, agent.HostNoAgent) {
		return steps, fmt.Errorf("close of a missing agent should be NoAgent, got %v", err)
	}
	steps = append(steps, "prompt and close on a missing agent → NoAgent")

	// Startup must finish before the prompt checks, so its Working/Idle cannot satisfy them.
	if err := waitListedReady(ctx, host, name, t.Turn); err != nil {
		return steps, err
	}
	drain(events)

	isWorking := func(s agent.PaneStatus) bool { return s == agent.PaneWorking }
	// Long enough that a live Herdr sees Working: its screen-based detection can miss a ~1 s turn.
	first := "Write the numbers 1 to 80 separated by spaces, then the word OK."
	if err := host.Prompt(ctx, name, first); err != nil {
		return steps, fmt.Errorf("prompt: %v", err)
	}
	if err := waitFor(events, name, t.Turn, isWorking); err != nil {
		return steps, fmt.Errorf("prompt → Working: %v", err)
	}
	if err := waitFor(events, name, t.Turn, agent.PaneStatus.IsReady); err != nil {
		return steps, fmt.Errorf("prompt → Idle: %v", err)
	}
	pane, err := host.Read(ctx, name, 200)
	if err != nil {
		return steps, fmt.Errorf("read: %v", err)
	}
	if !strings.Contains(pane, "Write the numbers 1 to 80") {
		return steps, fmt.Errorf("the prompt text did not reach the pane: %q", pane)
	}
	steps = append(steps, "prompt → text in the pane, Working, then Idle")

	if err := host.Prompt(ctx, name, "Count from 1 to 300, one number per line, slowly."); err != nil {
		return steps, fmt.Errorf("prompt: %v", err)
	}
	if err := waitFor(events, name, t.Turn, isWorking); err != nil {
		return steps, fmt.Errorf("long prompt → Working: %v", err)
	}
	listed, err := host.List(ctx)
	if err != nil {
		return steps, fmt.Errorf("list: %v", err)
	}
	var found *agent.AgentInfo
	for i := range listed {
		if listed[i].Name == name {
			found = &listed[i]
		}
	}
	if found == nil || found.Status != agent.PaneWorking {
		return steps, fmt.Errorf("%s should be Working before Escape: %+v", name, found)
	}
	drain(events)
	if err := host.SendKeys(ctx, name, []string{"Escape"}); err != nil {
		return steps, fmt.Errorf("send_keys: %v", err)
	}
	if err := waitFor(events, name, t.Turn, agent.PaneStatus.IsReady); err != nil {
		return steps, fmt.Errorf("Escape → Idle: %v", err)
	}
	steps = append(steps, "send_keys(Escape) interrupts a working agent")

	if err := host.Close(ctx, name); err != nil {
		return steps, fmt.Errorf("close: %v", err)
	}
	if err := waitExit(events, name, t.Turn); err != nil {
		return steps, fmt.Errorf("close → Exited: %v", err)
	}
	listed, err = host.List(ctx)
	if err != nil {
		return steps, fmt.Errorf("list: %v", err)
	}
	for _, a := range listed {
		if a.Name == name {
			return steps, fmt.Errorf("%s still listed after close", name)
		}
	}
	steps = append(steps, "close → Exited")

	fresh := host.Subscribe()
	time.Sleep(t.Settle)
	for _, s := range []struct {
		label string
		rx    <-chan agent.PaneEvent
	}{{"first", events}, {"new", fresh}} {
		for {
			var ev agent.PaneEvent
			select {
			case ev = <-s.rx:
			default:
				goto next
			}
			if ev.Kind == agent.PaneEventExited && ev.Agent == name {
				return steps, fmt.Errorf("duplicate Exited for %s on the %s stream", name, s.label)
			}
		}
	next:
	}
	steps = append(steps, "no replayed duplicate Exited")
	return steps, nil
}

func expectListed(ctx context.Context, host agent.SessionHost, name, after string) error {
	listed, err := host.List(ctx)
	if err != nil {
		return fmt.Errorf("list: %v", err)
	}
	for _, a := range listed {
		if a.Name == name {
			return nil
		}
	}
	return fmt.Errorf("%s missing from list after %s: %+v", name, after, listed)
}

func waitListedReady(ctx context.Context, host agent.SessionHost, name string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		listed, err := host.List(ctx)
		if err != nil {
			return fmt.Errorf("list: %v", err)
		}
		for _, a := range listed {
			if a.Name == name && a.Status.IsReady() {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s never became ready after start_agent", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func drain(events <-chan agent.PaneEvent) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

var errTimedOut = errors.New("timed out")

func waitFor(events <-chan agent.PaneEvent, name string, limit time.Duration, want func(agent.PaneStatus) bool) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errTimedOut
		case ev, ok := <-events:
			if !ok {
				return errors.New("event stream ended")
			}
			if ev.Agent != name {
				continue
			}
			if ev.Kind == agent.PaneEventStatus && want(ev.Status) {
				return nil
			}
			if ev.Kind == agent.PaneEventExited {
				return errors.New("agent exited")
			}
		}
	}
}

func waitExit(events <-chan agent.PaneEvent, name string, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errTimedOut
		case ev, ok := <-events:
			if !ok {
				return errors.New("event stream ended")
			}
			if ev.Kind == agent.PaneEventExited && ev.Agent == name {
				return nil
			}
		}
	}
}
