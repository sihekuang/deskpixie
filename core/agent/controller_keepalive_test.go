package agent

import "testing"

// upCoordinator is a controller whose coordinator is running, with nothing to do.
func upCoordinator() *AgentController {
	c := ctl()
	c.Step(EnsureCoordinatorInput(), T0)
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	return c
}

// #118: a coordinator that exits with nothing to do is started again, after a short pause.
func TestAnIdleCoordinatorThatExitsIsStartedAgain(t *testing.T) {
	c := upCoordinator()
	wantNotContains(t, visible(c.Step(paneExited(Coordinator), T0)), eStart, "not at once")
	wantNotContains(t, c.Step(TickInput(), T0+CoordinatorRestartMs-1), eStart)
	wantContains(t, c.Step(TickInput(), T0+CoordinatorRestartMs), eStart)
	wantNotContains(t, c.Step(TickInput(), T0+2*CoordinatorRestartMs), eStart, "once")
}

func TestACoordinatorIsNotStartedAgainAfterTheAppQuits(t *testing.T) {
	c := upCoordinator()
	c.Step(ShutdownInput(), T0)
	c.Step(paneExited(Coordinator), T0)
	wantNotContains(t, c.Step(TickInput(), T0+CoordinatorRestartMs), eStart)
}

func TestACoordinatorIsNotStartedAgainWhileDelegationIsOff(t *testing.T) {
	c := upCoordinator()
	c.Step(paneExited(Coordinator), T0)
	c.Step(DisableInput("Coordinator: herdr not found"), T0)
	wantNotContains(t, c.Step(TickInput(), T0+CoordinatorRestartMs), eStart)
}

// #118: a start that fails (other than a setup problem) is tried again, waiting longer each time.
func TestAFailedStartIsTriedAgainWithABackoff(t *testing.T) {
	c := ctl()
	c.Step(EnsureCoordinatorInput(), T0)
	now := T0
	for i, wait := range []Millis{CoordinatorRetryMs, 2 * CoordinatorRetryMs, 4 * CoordinatorRetryMs} {
		c.Step(CoordinatorFailedInput(HostError{Kind: HostTimeout}), now)
		wantNotContains(t, c.Step(TickInput(), now+wait-1), eStart)
		if !contains(c.Step(TickInput(), now+wait), eStart) {
			t.Fatalf("retry %d not after %d ms", i+1, wait)
		}
		now += wait
	}
	// Up: the next failure waits the shortest time again.
	c.Step(CoordinatorUpInput(PaneIdle), now)
	c.Step(paneExited(Coordinator), now)
	c.Step(TickInput(), now+CoordinatorRestartMs)
	now += CoordinatorRestartMs
	c.Step(CoordinatorFailedInput(HostError{Kind: HostTimeout}), now)
	wantContains(t, c.Step(TickInput(), now+CoordinatorRetryMs), eStart)
}

func TestTheRetryWaitStopsGrowing(t *testing.T) {
	c := ctl()
	c.Step(EnsureCoordinatorInput(), T0)
	now := T0
	for range 20 {
		c.Step(CoordinatorFailedInput(HostError{Kind: HostTimeout}), now)
		now += CoordinatorRetryMaxMs
		if !contains(c.Step(TickInput(), now), eStart) {
			t.Fatal("waited longer than CoordinatorRetryMaxMs")
		}
	}
}

// A setup problem turns delegation off: no retry.
func TestAStartThatFindsNoHerdrIsNotTriedAgain(t *testing.T) {
	c := ctl()
	c.Step(EnsureCoordinatorInput(), T0)
	c.Step(CoordinatorFailedInput(HostError{Kind: HostNotFound}), T0)
	wantNotContains(t, c.Step(TickInput(), T0+CoordinatorRetryMaxMs), eStart)
}

// #118: opening a conversation makes sure the coordinator is running.
func TestOpeningAConversationStartsTheCoordinator(t *testing.T) {
	c := ctl()
	wantContains(t, c.Step(voiceOpened(), T0), eStart)
	c.Step(voiceClosed(), T0)
	wantNotContains(t, c.Step(voiceOpened(), T0), eStart, "already starting")
	c = upCoordinator()
	wantNotContains(t, c.Step(voiceOpened(), T0), eStart, "already up")
	c = ctl()
	c.Step(DisableInput("Coordinator: herdr not found"), T0)
	wantNotContains(t, c.Step(voiceOpened(), T0), eStart, "delegation off")
}
