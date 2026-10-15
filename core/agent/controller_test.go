package agent

import (
	"fmt"
	"strings"
	"testing"
)

// Ported from crates/agent-core/src/controller_tests.rs (84 tests): one test per spec §6 row,
// plus delivery, permissions and recovery. Case names are the Rust names in CamelCase.

func ctl() *AgentController { return NewAgentController(DefaultSettings()) }

func ctlWith(edit func(*Settings)) *AgentController {
	s := DefaultSettings()
	edit(&s)
	return NewAgentController(s)
}

func delegate(c *AgentController, n uint64, task string) []Effect {
	return c.Step(DelegateInput(job(n), task, ""), T0)
}

// workingOnJob1: a coordinator that is up and idle, with job 1 sent to it.
func workingOnJob1() *AgentController {
	c := ctl()
	delegate(c, 1, "summarise the issues")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	return c
}

func stop(c *AgentController, message string, now Millis) []Effect {
	return c.Step(StopInput(Coordinator, message), now)
}

func openVoice(c *AgentController) { c.Step(voiceOpened(), T0) }

func lastProgress(t *testing.T, c *AgentController) string {
	t.Helper()
	v := c.Snapshot(T0)[0]
	if v.LastProgress == nil {
		t.Fatal("no last_progress")
	}
	return *v.LastProgress
}

func wantDisabled(t *testing.T, c *AgentController, want string) {
	t.Helper()
	got, ok := c.Disabled()
	if want == "" {
		if ok {
			t.Fatalf("disabled = %q, want none", got)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("disabled = %q (%v), want %q", got, ok, want)
	}
}

// ---------- §6 rows ----------

func TestDelegateCreatesAJobHoldsOpenAndStartsTheCoordinator(t *testing.T) {
	c := ctl()
	fx := delegate(c, 1, "summarise the issues")
	wantEffects(t, visible(fx), eHold(true), eStart)
	found := false
	for _, e := range fx {
		if e.Kind == EfAppend && e.File == JobsFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("no jobs append: %s", fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobDelegated)
}

func TestThePromptGoesOutOnceTheCoordinatorIsUpAndIdleAndTheJobIsWorking(t *testing.T) {
	c := ctl()
	delegate(c, 1, "summarise the issues")
	fx := visible(c.Step(CoordinatorUpInput(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("New request from the user: summarise the issues\n[ref job_000000000001]"))
	wantJobStatus(t, c, 1, JobWorking)
}

func TestContextIsAddedUnderTheTask(t *testing.T) {
	c := ctl()
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	fx := c.Step(DelegateInput(job(1), "fix it", "in focus"), T0)
	wantContains(t, fx, ePrompt("New request from the user: fix it\nin focus\n[ref job_000000000001]"))
}

func TestABusyCoordinatorGetsNothingUntilIdle(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneWorking), T0)
	fx := visible(delegate(c, 2, "second"))
	if anyKind(fx, EfPrompt) {
		t.Fatalf("%s", fmtEffects(fx))
	}
	// Job 1's turn ends (its Stop), then Herdr reports it idle.
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0)
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("New request from the user: second\n[ref job_000000000002]"))
}

func TestAWorkingBlockDeliversProgressAndStaysWorking(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := visible(stop(c, `<spoken ref="job_000000000001" status="working">Halfway through.</spoken>`, T0))
	wantEffects(t, fx, eInject("Coordinator reply: Halfway through."))
	wantJobStatus(t, c, 1, JobWorking)
	if got := lastProgress(t, c); got != "Halfway through." {
		t.Fatalf("last_progress %q", got)
	}
}

func TestAQuestionBlockAsksAndBypassesTheCap(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1 })
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	fx := visible(stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0))
	wantContains(t, fx, eNotify("B done."), "uses the only slot")
	fx = visible(stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0+10))
	wantContains(t, fx, eNotify("Which repo?"))
	wantJobStatus(t, c, 1, JobQuestion)
}

func TestDoneDeliversCheersAndReleasesTheHold(t *testing.T) {
	c := workingOnJob1()
	fx := visible(stop(c, "Done.\n<spoken ref=\"job_000000000001\" status=\"done\">There are 12 open issues.</spoken>", T0))
	wantEffects(t, fx, eChime, eNotify("There are 12 open issues."), eCue(CueHappy), eHold(false))
	wantJobStatus(t, c, 1, JobDone)
}

func TestAMissingStatusMeansDone(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001">All set.</spoken>`, T0)
	wantJobStatus(t, c, 1, JobDone)
}

func TestFailedCuesError(t *testing.T) {
	c := workingOnJob1()
	fx := visible(stop(c, `<spoken ref="job_000000000001" status="failed">The repo is gone.</spoken>`, T0))
	wantContains(t, fx, eCue(CueError))
	wantJobStatus(t, c, 1, JobFailed)
	wantContains(t, fx, eHold(false))
}

func TestCancelOfAnInFlightJobInterruptsAndQueuesTheCancelNote(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneWorking), T0)
	fx := visible(c.Step(CancelInput(job(1)), T0))
	wantEffects(t, fx, eInterrupt, eHold(false))
	wantJobStatus(t, c, 1, JobCancelled)
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("The user cancelled job_000000000001. Stop work on it and confirm.\n[ref job_000000000001]"))
}

func TestCancelOfAJobNeverSentJustDropsIt(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	fx := visible(c.Step(CancelInput(job(1)), T0))
	wantEffects(t, fx, eHold(false))
	fx = visible(c.Step(CoordinatorUpInput(PaneIdle), T0))
	wantNone(t, fx, "nothing to send")
}

func TestDelegationOffFailsOpenJobsWithTheAgentStopped(t *testing.T) {
	c := workingOnJob1()
	fx := visible(c.Step(CoordinatorFailedInput(HostError{Kind: HostGaveUp}), T0))
	if !effectsEqual(fx[:1], []Effect{eStatus("Coordinator: stopped after repeated crashes")}) {
		t.Fatalf("%s", fmtEffects(fx))
	}
	wantContains(t, fx, eNotify("The coordinator stopped before finishing: summarise the issues"))
	wantContains(t, fx, eHold(false))
	wantJobStatus(t, c, 1, JobFailed)
	wantDisabled(t, c, "Coordinator: stopped after repeated crashes")
	wantNone(t, visible(delegate(c, 2, "b")), "delegate is refused while off")
}

// ---------- replies by ref ----------

func TestSeveralBlocksReachTheirOwnJobs(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken><spoken ref="job_000000000001" status="working">A next.</spoken>`, T0)
	wantJobStatus(t, c, 2, JobDone)
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAnUnknownRefIsUnsolicitedNeverTheNewestJob(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := visible(stop(c, `<spoken ref="job_ffffffffffff" status="done">Something else.</spoken>`, T0))
	wantEffects(t, fx, eInject("Coordinator says: Something else."))
	wantJobStatus(t, c, 1, JobWorking, "job 1 untouched (#89)")
}

// Prefixed: routing_test.go has a test of the same Rust name.
func TestControllerUnsolicitedLinesAreOutboxedSilentlyWhenClosed(t *testing.T) {
	c := workingOnJob1()
	// The turn carrying job 1 ends first, so the next ref-less line belongs to no job.
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0)
	fx := stop(c, "<spoken>I tidied the notes.</spoken>", T0)
	wantNone(t, visible(fx))
	found := false
	for _, e := range fx {
		if e.Kind == EfAppend && e.File == OutboxFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("no outbox append: %s", fmtEffects(fx))
	}
}

func TestAReplyWithoutBlocksFallsBackToItsFirstSentences(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := stop(c, "I looked. Nothing is broken. More detail here.", T0)
	wantContains(t, fx, eLog("spoken_fallback"))
	// The only job in the prompt just answered: the fallback is its reply, and it closes.
	wantContains(t, fx, eInject("Coordinator reply: I looked. Nothing is broken."))
	wantJobStatus(t, c, 1, JobDone)
	wantContains(t, fx, eHold(false))
}

func TestARefLessBlockAnswersTheOnlyJobInFlight(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := visible(stop(c, "<spoken>Twelve issues, none urgent.</spoken>", T0))
	wantContains(t, fx, eInject("Coordinator reply: Twelve issues, none urgent."))
	wantJobStatus(t, c, 1, JobDone)
}

func TestAReplyWithoutRefsForTwoJobsInFlightIsUnsolicitedAndClosesBoth(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	openVoice(c)
	fx := visible(stop(c, "<spoken>Both handled.</spoken>", T0))
	wantContains(t, fx, eInject("Coordinator says: Both handled."))
	wantJobStatus(t, c, 1, JobDone)
	wantJobStatus(t, c, 2, JobDone)
	wantContains(t, fx, eInject("Coordinator reply: The coordinator finished: a; details are in the terminal."))
	wantContains(t, fx, eHold(false))
}

func TestAReplyWithoutBlocksAndNothingInFlightStaysUnsolicited(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0)
	openVoice(c)
	fx := visible(stop(c, "Still going. More here.", T0))
	wantContains(t, fx, eInject("Coordinator says: Still going. More here."))
	wantJobStatus(t, c, 1, JobWorking)
}

// ---------- delivery ----------

func TestClosedLinesReplayOnTheNextConversation(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001">Twelve issues.</spoken>`, T0)
	fx := visible(c.Step(voiceOpened(), T0+5))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: Twelve issues."))
	wantNone(t, visible(c.Step(voiceOpened(), T0+6)), "replayed once")
}

func TestAnotherPartsUndeliveredNoticeIsNotTheAgentsToDeliver(t *testing.T) {
	c := ctl()
	openVoice(c)
	fx := visible(c.Step(voiceClosed(Notice{Text: "Timer done: pasta (5 minutes)."}), T0))
	wantNone(t, fx)
}

func TestUndeliveredNoticesFromAClosedConversationAreNotified(t *testing.T) {
	c := ctl()
	openVoice(c)
	fx := visible(c.Step(voiceClosed(Notice{Text: "Coordinator reply: x"}), T0))
	wantEffects(t, fx, eChime, eNotify("x"))
	fx = visible(c.Step(InjectBouncedInput(Notice{Text: "Coordinator reply: y"}), T0))
	wantEffects(t, fx, eChime, eNotify("y"))
}

// ---------- permissions ----------

func askPush(c *AgentController, id RequestID, now Millis) []Effect {
	return c.Step(PermissionInput(id, Coordinator, "Bash", map[string]any{"command": "git push"}), now)
}

func TestAPermissionIsSpokenAsAQuestionWithAConfirmWord(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := visible(askPush(c, 7, T0))
	wantEffects(t, fx, eInject("Coordinator question: Run git push in coordinator? Say confirm hawk."))
	wantJobStatus(t, c, 1, JobQuestion)
}

func TestACleanConfirmAllowsAndResumesTheJob(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	askPush(c, 7, T0)
	fx := visible(c.Step(userSaid("Confirm hawk."), T0))
	wantEffects(t, fx, eDecide(7, Allow()), eInject("Coordinator: allowed"))
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAQuestionOrNegationNeverAllows(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	askPush(c, 7, T0)
	wantNone(t, visible(c.Step(userSaid("confirm hawk?"), T0)))
	fx := visible(c.Step(userSaid("no hawk"), T0))
	wantEffects(t, fx[:1], eDecide(7, Deny("The user said no.")))
}

func TestAnUnansweredPermissionIsDeniedAfterTheTimeout(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	if anyKind(c.Step(TickInput(), T0+119_999), EfDecide) {
		t.Fatal("decided early")
	}
	fx := visible(c.Step(TickInput(), T0+120_000))
	wantEffects(t, fx[:1], eDecide(7, Deny("No answer from the user.")))
	wantContains(t, fx, eNotify("Coordinator: denied after two minutes with no answer."))
}

func TestQuittingDeniesEveryPendingPermissionAndStopsTheCoordinator(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	askPush(c, 8, T0)
	fx := visible(c.Step(ShutdownInput(), T0))
	wantEffects(t, fx, eDecide(7, Deny("The app quit.")), eDecide(8, Deny("The app quit.")), eStopCoord)
}

func TestKeepPanesOnQuitLeavesTheCoordinator(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.KeepPanesOnQuit = true })
	wantNone(t, visible(c.Step(ShutdownInput(), T0)))
}

func TestCancellingAJobDeniesItsPendingPermission(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	fx := visible(c.Step(CancelInput(job(1)), T0))
	wantContains(t, fx, eDecide(7, Deny("The user cancelled the job.")))
}

// ---------- coordinator lifecycle ----------

func TestABlockedCoordinatorWithoutAPendingPermissionGetsTheTerminalHint(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneBlocked), T0)
	wantNone(t, visible(c.Step(TickInput(), T0+2_999)))
	fx := visible(c.Step(TickInput(), T0+3_000))
	wantEffects(t, fx, eStatus("Coordinator: waiting in terminal"), eChime, eNotify(BlockedHint))
	wantNone(t, visible(c.Step(TickInput(), T0+9_000)), "hinted once")
	fx = visible(c.Step(coordStatus(PaneWorking), T0+9_500))
	wantEffects(t, fx, eStatus(""))
}

func TestABlockedCoordinatorWithAPendingPermissionGetsNoHint(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	c.Step(coordStatus(PaneBlocked), T0)
	wantNotContains(t, c.Step(TickInput(), T0+5_000), eNotify(BlockedHint))
}

func TestAnExitedCoordinatorIsReCreatedAndGetsItsWorkAgain(t *testing.T) {
	c := workingOnJob1()
	fx := visible(c.Step(paneExited(Coordinator), T0))
	wantEffects(t, fx, eStart)
	fx = visible(c.Step(CoordinatorUpInput(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("New request from the user: summarise the issues\n[ref job_000000000001]"))
}

func TestAStartFailureFailsTheWaitingJobsButKeepsDelegationOn(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	fx := visible(c.Step(CoordinatorFailedInput(HostError{Kind: HostTimeout, Detail: "claude"}), T0))
	wantEffects(t, fx[:1], eStatus("Coordinator: claude did not start"))
	wantJobStatus(t, c, 1, JobFailed)
	wantDisabled(t, c, "")
	wantEffects(t, visible(delegate(c, 2, "b")), eHold(true), eStart)
}

func TestHerdrMissingOrMismatchedTurnsDelegationOff(t *testing.T) {
	c := ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostNotFound}), T0)
	wantDisabled(t, c, "Coordinator: herdr not found")
	c = ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostVersionMismatch, Client: 17, Server: 18}), T0)
	wantDisabled(t, c, "Coordinator: herdr version mismatch (update herdr)")
	// The host says what the user has and which versions are supported: the status line shows it.
	c = ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostVersionMismatch, Client: 20, Server: 20,
		Detail: "you have herdr 0.8.0 (protocol 20); DeskPixie supports Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)"}), T0)
	wantDisabled(t, c, "Coordinator: herdr version mismatch: you have herdr 0.8.0 (protocol 20); DeskPixie supports Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)")
}

// Another session host's name is the one the status line names (Herdr's lines are unchanged).
func TestTheStatusLineNamesTheActiveHost(t *testing.T) {
	c := ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostNotFound, Host: "tmux"}), T0)
	wantDisabled(t, c, "Coordinator: tmux not found")
	c = ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostVersionMismatch, Host: "tmux"}), T0)
	wantDisabled(t, c, "Coordinator: tmux version mismatch (update tmux)")
	c = ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostVersionMismatch, Host: "tmux", Detail: "you have tmux 3.1; DeskPixie supports tmux 3.2 or newer"}), T0)
	wantDisabled(t, c, "Coordinator: tmux version mismatch: you have tmux 3.1; DeskPixie supports tmux 3.2 or newer")
	c = ctl()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostNotFound, Host: "herdr"}), T0)
	wantDisabled(t, c, "Coordinator: herdr not found")
}

func TestRestartTurnsDelegationBackOn(t *testing.T) {
	// A runtime disable (repeated crashes) is what Restart undoes.
	c := workingOnJob1()
	c.Step(CoordinatorFailedInput(HostError{Kind: HostGaveUp}), T0)
	wantDisabled(t, c, "Coordinator: stopped after repeated crashes")
	fx := visible(c.Step(RestartInput(), T0))
	wantEffects(t, fx, eStatus(""), eResetHost)
	wantDisabled(t, c, "")
	c = workingOnJob1()
	fx = visible(c.Step(RestartInput(), T0))
	wantEffects(t, fx, eResetHost, eStopCoord)
}

func TestEnsureCoordinatorStartsItOnce(t *testing.T) {
	c := ctl()
	wantEffects(t, visible(c.Step(EnsureCoordinatorInput(), T0)), eStart)
	wantNone(t, visible(c.Step(EnsureCoordinatorInput(), T0)), "already starting")
}

// ---------- narration ----------

func TestNarrationIsSpokenOnceTheJobIsQuietAtMostEvery30Seconds(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	edit := func(file string, now Millis) []Effect {
		return visible(c.Step(PostToolInput(Coordinator, "Edit", map[string]any{"file_path": "/x/" + file}), now))
	}
	wantNone(t, edit("notes.md", T0), "held until quiet")
	wantNone(t, edit("plan.md", T0+5_000), "held until quiet")
	wantNone(t, visible(c.Step(TickInput(), T0+5_000+UpdateQuietMs-1)))
	wantEffects(t, visible(c.Step(TickInput(), T0+5_000+UpdateQuietMs)), eInject("Coordinator progress: editing plan.md"))
	// Quiet again 20 s later, but within 30 s of the last narration: it waits for the interval.
	at := T0 + 5_000 + UpdateQuietMs
	wantNone(t, edit("main.go", at+1))
	wantNone(t, visible(c.Step(TickInput(), at+UpdateQuietMs+1)))
	wantEffects(t, visible(c.Step(TickInput(), at+NarrateIntervalMs)), eInject("Coordinator progress: editing main.go"))
	if got := lastProgress(t, c); got != "editing main.go" {
		t.Fatalf("last_progress %q", got)
	}
}

func TestBusyNarrationIsStillSpokenAfterTheMaxHold(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	var fx []Effect
	for now := T0; now <= T0+UpdateMaxHoldMs; now += 5_000 {
		c.Step(PostToolInput(Coordinator, "Bash", map[string]any{"description": fmt.Sprintf("step %d", now)}), now)
		fx = append(fx, visible(c.Step(TickInput(), now))...)
	}
	wantEffects(t, fx, eInject(fmt.Sprintf("Coordinator progress: step %d", T0+UpdateMaxHoldMs)))
}

func TestNarrationIsSilentWhenClosedButStillRecorded(t *testing.T) {
	c := workingOnJob1()
	fx := c.Step(PostToolInput(Coordinator, "Bash", map[string]any{"description": "Run tests"}), T0)
	wantNone(t, visible(fx))
	if got := lastProgress(t, c); got != "run tests" {
		t.Fatalf("last_progress %q", got)
	}
}

// ---------- jobs cancel resolution, restore ----------

func TestCancelResolution(t *testing.T) {
	c := ctl()
	check := func(id string, wantID JobID, wantErr string) {
		t.Helper()
		got, err := c.ResolveCancel(id)
		if wantErr != "" {
			if err == nil || err.Error() != wantErr {
				t.Fatalf("resolve_cancel(%q) = %v, %v; want error %q", id, got, err, wantErr)
			}
			return
		}
		if err != nil || got != wantID {
			t.Fatalf("resolve_cancel(%q) = %v, %v; want %v", id, got, err, wantID)
		}
	}
	check("", "", "No job is open.")
	delegate(c, 1, "a")
	check("", job(1), "")
	delegate(c, 2, "b")
	check("", "", "Several jobs are open (job_000000000001, job_000000000002). Which one?")
	check("job_000000000002", job(2), "")
	check("job_00000000000f", "", "No open job job_00000000000f.")
}

func TestRestoreFailsOpenJobsAndOutboxesALine(t *testing.T) {
	first := ctl()
	var jobs []string
	for _, e := range delegate(first, 1, "summarise the issues") {
		if e.Kind == EfAppend && e.File == JobsFile {
			jobs = append(jobs, e.Line)
		}
	}
	c := ctl()
	fx := c.Step(RestoredInput(jobs, nil, false), T0+10)
	if fx[0].Kind != EfRewrite || fx[0].File != JobsFile || len(fx[0].Lines) != 1 {
		t.Fatalf("%s", fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobFailed)
	fx = visible(c.Step(voiceOpened(), T0+20))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator: a job stopped when the app restarted: summarise the issues"))
}

// ---------- hardening rulings ----------

func TestAnUnreadableCommandIsDeniedWithoutATokenOrAStatusChange(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fx := visible(c.Step(PermissionInput(9, Coordinator, "Bash", map[string]any{"command": "git push origin :main"}), T0))
	wantEffects(t, fx,
		eDecide(9, Deny("This can't be approved by voice. Ask the user to run it themselves, or to change it so it can be read aloud plainly.")),
		eInject("The coordinator asked to run something I can't read out safely, so I said no. Open the terminal to see it."),
	)
	wantJobStatus(t, c, 1, JobWorking)
	// No token was allocated: a readable request still gets the first word.
	fx = visible(askPush(c, 10, T0))
	wantEffects(t, fx, eInject("Coordinator question: Run git push in coordinator? Say confirm hawk."))
	wantJobStatus(t, c, 1, JobQuestion)
}

func TestOpeningTheConversationDropsTheCollapsedMoreUpdatesLine(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1 })
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	delegate(c, 3, "c")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	stop(c, `<spoken ref="job_000000000001" status="done">A done.</spoken>`, T0)
	stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0+1)
	stop(c, `<spoken ref="job_000000000003" status="done">C done.</spoken>`, T0+2)
	c.Step(voiceOpened(), T0+3)
	fx := visible(c.Step(TickInput(), T0+61_000))
	for _, e := range fx {
		if e.Kind == EfNotify && strings.Contains(e.Text, "more update") {
			t.Fatalf("%s", fmtEffects(fx))
		}
	}
}

func TestWhenAllConfirmWordsAreTakenTheRequestIsDenied(t *testing.T) {
	c := workingOnJob1()
	for id := range RequestID(12) {
		askPush(c, id, T0)
	}
	fx := visible(askPush(c, 99, T0))
	wantEffects(t, fx[:1], eDecide(99, Deny("Too many permission requests are waiting.")))
}

// ---------- review fixes ----------

func pendingAskNotice() Notice {
	return Notice{Text: "Coordinator question: Run git push in coordinator? Say confirm hawk."}
}

func TestRestartForgetsTheCoordinator(t *testing.T) {
	c := workingOnJob1()
	c.Step(RestartInput(), T0)
	wantEffects(t, visible(c.Step(EnsureCoordinatorInput(), T0)), eStart)
}

func TestAnUndeliveredQuestionKeepsItsKindAndBypassesTheCap(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1 })
	delegate(c, 1, "a")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	openVoice(c)
	fx := visible(askPush(c, 7, T0))
	wantEffects(t, fx, Effect{Kind: EfInject, Notice: pendingAskNotice()})
	c.Step(voiceClosed(), T0)
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0+1)
	fx = visible(c.Step(voiceClosed(pendingAskNotice()), T0+2))
	wantEffects(t, fx, eChime, eNotify("Run git push in coordinator? Say confirm hawk."))
}

func TestABouncedQuestionWhoseRequestExpiredIsDroppedFromReplay(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	askPush(c, 7, T0)
	fx := visible(c.Step(InjectBouncedInput(pendingAskNotice()), T0+1))
	wantEffects(t, fx, eChime, eNotify("Run git push in coordinator? Say confirm hawk."))
	c.Step(TickInput(), T0+120_000)
	fx = visible(c.Step(voiceOpened(), T0+120_001))
	if len(fx) == 0 || fx[0].Kind != EfInject {
		t.Fatalf("%s", fmtEffects(fx))
	}
	if strings.Contains(fx[0].Notice.Text, "confirm hawk") {
		t.Fatal(fx[0].Notice.Text)
	}
}

func TestBouncedNarrationIsDropped(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	c.Step(PostToolInput(Coordinator, "Edit", map[string]any{"file_path": "/x/notes.md"}), T0)
	fx := visible(c.Step(TickInput(), T0+UpdateQuietMs))
	if len(fx) == 0 || fx[0].Kind != EfInject {
		t.Fatalf("%s", fmtEffects(fx))
	}
	wantNone(t, visible(c.Step(InjectBouncedInput(fx[0].Notice), T0)))
}

func TestAnsweringTheOnlyWaitingJobForwardsTheAnswer(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	wantJobStatus(t, c, 1, JobQuestion)
	c.Step(coordStatus(PaneIdle), T0)
	fx := visible(c.Step(AnswerInput("", "the app repo"), T0+1))
	wantEffects(t, fx,
		Effect{Kind: EfAnswerResult, Job: job(1)},
		ePrompt("The user answered: the app repo\n[ref job_000000000001]"),
	)
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAnAmbiguousOrUnknownAnswerIsAnErrorAndChangesNothing(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	errOf := func(id JobID) string {
		t.Helper()
		fx := visible(c.Step(AnswerInput(id, "x"), T0))
		if len(fx) != 1 || fx[0].Kind != EfAnswerResult || fx[0].Err == "" {
			t.Fatalf("%s", fmtEffects(fx))
		}
		return fx[0].Err
	}
	eq := func(got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	eq(errOf(""), "No job is waiting for an answer.")
	eq(errOf(job(1)), "job_000000000001 is not waiting for an answer.")
	eq(errOf(job(9)), "No open job job_000000000009.")
	c.Step(StopInput(Coordinator, `<spoken ref="job_000000000001" status="question">A?</spoken><spoken ref="job_000000000002" status="question">B?</spoken>`), T0)
	if e := errOf(""); !strings.HasPrefix(e, "Several jobs are waiting for an answer") {
		t.Fatal(e)
	}
	wantJobStatus(t, c, 1, JobQuestion)
	wantJobStatus(t, c, 2, JobQuestion)
}

func TestCancellingOneBatchedJobSendsTheOtherAgain(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(coordStatus(PaneWorking), T0)
	fx := visible(c.Step(CancelInput(job(1)), T0))
	wantEffects(t, fx, eInterrupt)
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("The user cancelled job_000000000001. Stop work on it and confirm.\n[ref job_000000000001]\n\nNew request from the user: b\n[ref job_000000000002]"))
	wantJobStatus(t, c, 2, JobWorking)
}

func TestAJobWithTwoPendingPermissionsStaysQuestionUntilBothAreDecided(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	askPush(c, 8, T0)
	c.Step(userSaid("confirm hawk"), T0)
	wantJobStatus(t, c, 1, JobQuestion)
	c.Step(userSaid("confirm lark"), T0)
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAnExpiredPermissionReturnsItsJobToWorking(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	c.Step(TickInput(), T0+120_000)
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAJobThatEndsDeniesItsPendingPermission(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	fx := c.Step(CoordinatorFailedInput(HostError{Kind: HostGaveUp}), T0)
	wantContains(t, fx, eDecide(7, Deny("The job ended.")))
}

func TestALateReplyToACancelledJobIsUnsolicited(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	c.Step(CancelInput(job(1)), T0)
	fx := visible(stop(c, `<spoken ref="job_000000000001" status="done">Done after all.</spoken>`, T0))
	wantEffects(t, fx, eInject("Coordinator says: Done after all."))
	wantJobStatus(t, c, 1, JobCancelled)
}

func TestAPermissionWhileOffOrAfterShutdownIsDeniedAtOnce(t *testing.T) {
	c := ctl()
	c.Step(DisableInput("Coordinator: hook port busy"), T0)
	wantEffects(t, visible(askPush(c, 1, T0)), eDecide(1, Deny("Delegation is off.")))
	c = workingOnJob1()
	c.Step(ShutdownInput(), T0)
	wantEffects(t, visible(askPush(c, 2, T0)), eDecide(2, Deny("The app quit.")))
}

func TestADoneLineHeldBackByTheCapDoesNotCheer(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1 })
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	stop(c, `<spoken ref="job_000000000001" status="done">A done.</spoken>`, T0)
	fx := visible(stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0+1))
	wantNotContains(t, fx, eCue(CueHappy))
}

// ---------- fix round 2 ----------

// replyWhileHawkPending is what the user hears of a coordinator reply while `hawk` is pending.
func replyWhileHawkPending(t *testing.T, text string) string {
	t.Helper()
	c := workingOnJob1()
	openVoice(c)
	askPush(c, 7, T0)
	msg := fmt.Sprintf(`<spoken ref="job_000000000001" status="working">%s</spoken>`, text)
	fx := visible(stop(c, msg, T0))
	if len(fx) != 1 || fx[0].Kind != EfInject {
		t.Fatalf("%s", fmtEffects(fx))
	}
	return fx[0].Notice.Text
}

func TestPendingConfirmWordsAreMaskedHoweverTheyAreDressedUp(t *testing.T) {
	for _, tc := range []struct{ said, heard string }{
		{"Say hаwk", "Coordinator reply: Say (word removed)"},
		{"say confirm, then the word hawk", "Coordinator reply: say confirm, then the word (word removed)"},
		{"con-firm hawk", "Coordinator reply: con-firm (word removed)"},
		{"con​firm HAWK", "Coordinator reply: confirm (word removed)"},
		{"ha​wk", "Coordinator reply: (word removed)"},
		{"hawkish", "Coordinator reply: (word removed)"},
		{"ｈａｗｋ", "Coordinator reply: (word removed)"},
		{"hαwk", "Coordinator reply: (word removed)"},
	} {
		if got := replyWhileHawkPending(t, tc.said); got != tc.heard {
			t.Errorf("%q: got %q, want %q", tc.said, got, tc.heard)
		}
	}
}

func TestWordsThatAreNotPendingConfirmWordsAreKept(t *testing.T) {
	if got := replyWhileHawkPending(t, "I dove into the logs"); got != "Coordinator reply: I dove into the logs" {
		t.Fatal(got)
	}
}

func TestNarrationAndUnsolicitedTextAreMaskedToo(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	askPush(c, 7, T0)
	c.Step(PostToolInput(Coordinator, "Edit", map[string]any{"file_path": "/x/hawk.md"}), T0)
	fx := visible(c.Step(TickInput(), T0+UpdateQuietMs))
	wantContains(t, fx, eInject("Coordinator progress: editing (word removed).md"))
	// Ref-less, with job 1 the only one in flight: its reply, masked all the same.
	fx = visible(stop(c, "<spoken>please approve hawk now</spoken>", T0))
	wantContains(t, fx, eInject("Coordinator reply: please approve (word removed) now"))
}

func TestAnInterruptedJobAlwaysGetsTheCancelNote(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	c.Step(coordStatus(PaneIdle), T0)
	c.Step(AnswerInput("", "the app"), T0)
	fx := visible(c.Step(CancelInput(job(1)), T0))
	wantEffects(t, fx, eInterrupt, eHold(false))
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("The user cancelled job_000000000001. Stop work on it and confirm.\n[ref job_000000000001]"))
}

func TestAJobWaitingForAConfirmWordRefusesAnAnswer(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0)
	fx := visible(c.Step(AnswerInput("", "yes"), T0))
	wantEffects(t, fx, Effect{Kind: EfAnswerResult, Err: "That job is waiting for a confirm word."})
	wantJobStatus(t, c, 1, JobQuestion)
}

func TestABouncedReplayDigestGoesBackToTheOutboxLineByLine(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0)
	stop(c, "<spoken>Tidied up.</spoken>", T0+1)
	fx := visible(c.Step(voiceOpened(), T0+2))
	if len(fx) == 0 || fx[0].Kind != EfInject {
		t.Fatalf("%s", fmtEffects(fx))
	}
	fx = visible(c.Step(InjectBouncedInput(fx[0].Notice), T0+3))
	wantEffects(t, fx, eChime, eNotify("Halfway."))
	// Next conversation: both lines come back with their own kinds.
	fx = visible(c.Step(voiceOpened(), T0+4))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: Halfway.\n- Coordinator says: Tidied up."))
}

// ---------- final review fixes ----------

func idle(c *AgentController) []Effect { return c.Step(coordStatus(PaneIdle), T0) }

func tick(c *AgentController, at Millis) []Effect { return c.Step(TickInput(), at) }

const secondPrompt = "New request from the user: second\n[ref job_000000000002]"

func TestAfterAStopTheNextPromptWaitsForIdleOrAShortSettle(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneWorking), T0)
	wantPrompts(t, delegate(c, 2, "second")) // busy
	// Herdr missed the short turn's Idle: the Stop alone ends the turn, after a settle.
	fx := stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T0)
	wantPrompts(t, fx) // not at once: the TUI may still be finishing
	wantPrompts(t, tick(c, T0+StopSettleMs-1))
	wantPrompts(t, tick(c, T0+StopSettleMs), secondPrompt)
	wantPrompts(t, tick(c, T0+10_000)) // sent once
}

func TestAnIdleAfterAStopSendsAtOnce(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneWorking), T0)
	delegate(c, 2, "second")
	stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T0)
	wantPrompts(t, idle(c), secondPrompt)
	wantPrompts(t, tick(c, T0+StopSettleMs)) // the settle timer is spent
}

func TestANewTurnAfterAStopCancelsTheSettle(t *testing.T) {
	c := workingOnJob1()
	delegate(c, 2, "second")
	stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T0)
	c.Step(coordStatus(PaneWorking), T0+100)
	wantPrompts(t, tick(c, T0+StopSettleMs)) // the user typed into it: it is busy
}

func TestAFailedPromptIsRetriedOnATimerWithoutAnyStatusEvent(t *testing.T) {
	c := workingOnJob1()
	fx := visible(c.Step(PromptFailedInput(), T0))
	wantPrompts(t, fx)
	wantJobStatus(t, c, 1, JobWorking)
	delegate(c, 2, "second")
	wantPrompts(t, tick(c, T0+1_999))
	// The failed message first, in order.
	wantPrompts(t, tick(c, T0+2_000), "New request from the user: summarise the issues\n[ref job_000000000001]\n\n"+secondPrompt)
}

func TestThreeFailedPromptsInARowFailTheirJobsWithBackoffBetween(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptFailedInput(), T0)
	if n := len(prompts(tick(c, T0+2_000))); n != 1 {
		t.Fatalf("after 2 s: %d prompts", n)
	}
	c.Step(PromptFailedInput(), T0+2_000)
	wantPrompts(t, tick(c, T0+5_999))
	if n := len(prompts(tick(c, T0+6_000))); n != 1 {
		t.Fatalf("after 4 s more: %d prompts", n)
	}
	fx := visible(c.Step(PromptFailedInput(), T0+6_000))
	wantJobStatus(t, c, 1, JobFailed)
	wantContains(t, fx, eNotify("The coordinator stopped before finishing: summarise the issues"))
	wantContains(t, fx, eCue(CueError))
	wantPrompts(t, tick(c, T0+60_000)) // nothing left to send
}

func TestAFinishedTurnResetsThePromptFailureCount(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptFailedInput(), T0)
	tick(c, T0+2_000)
	c.Step(PromptFailedInput(), T0+2_000)
	tick(c, T0+6_000)
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0+6_000)
	delegate(c, 2, "b")
	if n := len(prompts(tick(c, T0+6_000+StopSettleMs))); n != 1 {
		t.Fatalf("%d prompts", n)
	}
	c.Step(PromptFailedInput(), T0+8_000)
	wantJobStatus(t, c, 2, JobWorking, "one failure since the last turn")
}

func TestATimedOutPromptWithNoSignOfLifeFailsItsJobsAfterAMinute(t *testing.T) {
	c := workingOnJob1()
	fx := visible(c.Step(PromptTimedOutInput(), T0))
	wantPrompts(t, fx) // never re-sent: it may have been typed
	wantNotContains(t, tick(c, T0+PromptWatchdogMs-1), eCue(CueError))
	fx = visible(tick(c, T0+PromptWatchdogMs))
	wantJobStatus(t, c, 1, JobFailed)
	wantContains(t, fx, eNotify("I couldn't reach the coordinator: summarise the issues"))
	wantContains(t, fx, eCue(CueError))
}

func TestASignOfLifeAfterATimedOutPromptStopsTheWatchdog(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptTimedOutInput(), T0)
	c.Step(coordStatus(PaneWorking), T0+1_000)
	tick(c, T0+PromptWatchdogMs+1)
	wantJobStatus(t, c, 1, JobWorking)
	c = workingOnJob1()
	c.Step(PromptTimedOutInput(), T0)
	stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0+1_000)
	tick(c, T0+PromptWatchdogMs+1)
	wantJobStatus(t, c, 1, JobWorking)
}

func TestARefLessBlockKeepsItsStatusForTheOnlyJob(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   JobStatus
	}{{"question", JobQuestion}, {"working", JobWorking}, {"failed", JobFailed}, {"done", JobDone}} {
		c := workingOnJob1()
		openVoice(c)
		stop(c, fmt.Sprintf(`<spoken status="%s">Over to you.</spoken>`, tc.status), T0)
		wantJobStatus(t, c, 1, tc.want, tc.status)
	}
}

func TestARefLessQuestionOrProgressForTwoJobsLeavesThemOpen(t *testing.T) {
	for _, status := range []string{"question", "working"} {
		c := ctl()
		delegate(c, 1, "a")
		delegate(c, 2, "b")
		c.Step(CoordinatorUpInput(PaneIdle), T0)
		openVoice(c)
		fx := visible(stop(c, fmt.Sprintf(`<spoken status="%s">Which one first?</spoken>`, status), T0))
		wantContains(t, fx, eInject("Coordinator says: Which one first?"), status)
		wantJobStatus(t, c, 1, JobWorking, status)
		wantJobStatus(t, c, 2, JobWorking, status)
	}
}

func TestASetupDisableSurvivesRestart(t *testing.T) {
	c := ctl()
	c.Step(DisableInput("Coordinator: hook port busy"), T0)
	c.Step(RestartInput(), T0)
	wantDisabled(t, c, "Coordinator: hook port busy")
	wantNone(t, visible(delegate(c, 1, "a")), "delegate still refused")
	wantNone(t, visible(c.Step(EnsureCoordinatorInput(), T0)))
}

func TestShutdownWithNoCoordinatorStopsNothing(t *testing.T) {
	c := ctl()
	wantNotContains(t, c.Step(ShutdownInput(), T0), eStopCoord)
}

func TestRestartWithNoCoordinatorStopsNothing(t *testing.T) {
	c := ctl()
	wantEffects(t, visible(c.Step(RestartInput(), T0)), eResetHost)
}

// ---------- fix round 3 ----------

func TestAnIdleBeforeTheStopDoesNotSendTheNextPrompt(t *testing.T) {
	c := workingOnJob1()
	delegate(c, 2, "second")
	// Herdr's Idle for job 1's turn beats our Stop hook.
	wantPrompts(t, idle(c)) // job 1's Stop has not arrived
	fx := stop(c, "<spoken>All done.</spoken>", T0+100)
	wantPrompts(t, fx)
	wantJobStatus(t, c, 1, JobDone, "the ref-less reply answers job 1 only")
	wantJobStatus(t, c, 2, JobDelegated, "job 2 was never in that prompt")
	wantPrompts(t, tick(c, T0+100+StopSettleMs), secondPrompt) // then the settle rule
}

func TestAnIdleWhoseStopIsLostSendsAfterTenSeconds(t *testing.T) {
	c := workingOnJob1()
	delegate(c, 2, "second")
	idle(c)
	wantPrompts(t, tick(c, T0+IdleWithoutStopMs-1))
	wantPrompts(t, tick(c, T0+IdleWithoutStopMs), secondPrompt)
}

func TestARefLessFailedBlockForTwoJobsFailsBoth(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	openVoice(c)
	fx := visible(stop(c, `<spoken status="failed">That broke.</spoken>`, T0))
	wantContains(t, fx, eInject("Coordinator says: That broke."))
	wantJobStatus(t, c, 1, JobFailed)
	wantJobStatus(t, c, 2, JobFailed)
	wantContains(t, fx, eHold(false), "Pixie stops holding")
}

func TestABusyStatusCancelsAPendingPromptRetry(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptFailedInput(), T0)
	c.Step(coordStatus(PaneWorking), T0+100)
	wantPrompts(t, tick(c, T0+2_000)) // busy: no retry into a working coordinator
}

func TestAnIdleAfterATimedOutPromptIsNotASignOfLife(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptTimedOutInput(), T0)
	idle(c)
	tick(c, T0+PromptWatchdogMs)
	wantJobStatus(t, c, 1, JobFailed, "Idle without Working: the prompt never landed")
}

func TestAnExitedCoordinatorResetsThePromptFailureCount(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptFailedInput(), T0)
	tick(c, T0+2_000)
	c.Step(PromptFailedInput(), T0+2_000)
	c.Step(paneExited(Coordinator), T0+3_000)
	c.Step(CoordinatorUpInput(PaneIdle), T0+4_000)
	c.Step(PromptFailedInput(), T0+4_000)
	wantJobStatus(t, c, 1, JobWorking, "a new coordinator starts the count again")
}

// ---------- fix round 4 ----------

func TestWorkingAfterAnIdleFlickerCancelsTheLostStopTimer(t *testing.T) {
	c := workingOnJob1()
	delegate(c, 2, "second")
	idle(c)
	c.Step(coordStatus(PaneWorking), T0+1_000)
	wantPrompts(t, tick(c, T0+IdleWithoutStopMs)) // still busy: nothing typed into it
}

func TestAnIdleDuringTheWatchdogIsLeftToTheWatchdog(t *testing.T) {
	c := workingOnJob1()
	c.Step(PromptTimedOutInput(), T0)
	delegate(c, 2, "second")
	idle(c)
	wantPrompts(t, tick(c, T0+IdleWithoutStopMs)) // the watchdog decides
	fx := tick(c, T0+PromptWatchdogMs)
	wantJobStatus(t, c, 1, JobFailed)
	wantPrompts(t, fx, secondPrompt) // then the rest goes out
}

func TestANoticeShowsWhenTheStatusIsOtherwiseEmptyAndDisablesNothing(t *testing.T) {
	c := workingOnJob1()
	fx := visible(c.Step(NoticeInput("Coordinator: workers off: projectsRoot not usable"), T0))
	wantEffects(t, fx, eStatus("Coordinator: workers off: projectsRoot not usable"))
	c.Step(coordStatus(PaneBlocked), T0)
	fx = visible(c.Step(TickInput(), T0+3_000))
	wantContains(t, fx, eStatus("Coordinator: waiting in terminal"), "a live status wins")
	fx = visible(c.Step(coordStatus(PaneWorking), T0+4_000))
	wantEffects(t, fx, eStatus("Coordinator: workers off: projectsRoot not usable")) // then the notice is back
	fx = visible(askPush(c, 1, T0+5_000))
	if anyKind(fx, EfDecide) {
		t.Fatalf("delegation still works: %s", fmtEffects(fx))
	}
}

func effectsOfKind(fx []Effect, kind EffectKind) []Effect {
	var out []Effect
	for _, e := range fx {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// saidManualNotice: does any effect carry Pixie's Manual-mode line (spoken, notified or kept)?
func saidManualNotice(fx []Effect) bool {
	for _, e := range fx {
		if strings.Contains(e.Text, ManualNotice) || strings.Contains(e.Notice.Text, ManualNotice) || strings.Contains(e.Line, ManualNotice) {
			return true
		}
	}
	return false
}

func TestAnAutoLaunchedClaudeAgentReportingDefaultIsInManual(t *testing.T) {
	c := NewAgentController(DefaultSettings())
	fx := c.Step(ModeInput(Coordinator, "default"), 1000)
	m := effectsOfKind(fx, EfManual)
	if len(m) != 1 || m[0].Agent != Coordinator || !m[0].Manual {
		t.Fatalf("%s", fmtEffects(fx))
	}
	if !saidManualNotice(fx) {
		t.Fatalf("no notice: %s", fmtEffects(fx))
	}
	if fx := c.Step(ModeInput(Coordinator, "default"), 2000); len(effectsOfKind(fx, EfManual)) != 0 || saidManualNotice(fx) {
		t.Fatal("repeated")
	}
	fx = c.Step(ModeInput(Coordinator, "auto"), 3000)
	if m := effectsOfKind(fx, EfManual); len(m) != 1 || m[0].Manual {
		t.Fatalf("not cleared: %s", fmtEffects(fx))
	}
	// Back in Manual later: flagged again, but Pixie says it only once per app run.
	fx = c.Step(ModeInput(Coordinator, "default"), 4000)
	if len(effectsOfKind(fx, EfManual)) != 1 || saidManualNotice(fx) {
		t.Fatalf("%s", fmtEffects(fx))
	}
}

func TestAFirstAutoReportIsNotAChange(t *testing.T) {
	c := NewAgentController(DefaultSettings())
	if fx := c.Step(ModeInput(Coordinator, "auto"), 1000); len(effectsOfKind(fx, EfManual)) != 0 {
		t.Fatalf("%s", fmtEffects(fx))
	}
	if fx := c.Step(ModeInput(Coordinator, "plan"), 2000); len(effectsOfKind(fx, EfManual)) != 0 {
		t.Fatalf("an unknown mode flagged: %s", fmtEffects(fx))
	}
}

func TestACodexAgentIsNeverFlaggedManual(t *testing.T) {
	s := DefaultSettings()
	s.CoordinatorKind = Codex
	c := NewAgentController(s)
	for _, m := range []string{"default", "", "on-request", "never"} {
		if fx := c.Step(ModeInput(Coordinator, m), 1000); len(effectsOfKind(fx, EfManual)) != 0 {
			t.Fatalf("%q flagged a Codex coordinator", m)
		}
	}
}

func TestOnlyClaudeWorkersAreFlaggedManual(t *testing.T) {
	cc := wctl()
	cl := admitOK(t, cc, Claude)
	if fx := cc.Step(ModeInput(cl.Name, "default"), 1000); len(effectsOfKind(fx, EfManual)) != 1 {
		t.Fatalf("claude worker not flagged: %s", fmtEffects(fx))
	}
	cx := wctl()
	cp := admitOK(t, cx, Codex)
	if fx := cx.Step(ModeInput(cp.Name, "default"), 1000); len(effectsOfKind(fx, EfManual)) != 0 {
		t.Fatalf("codex worker flagged: %s", fmtEffects(fx))
	}
	if fx := cx.Step(ModeInput("nobody", "default"), 1000); len(effectsOfKind(fx, EfManual)) != 0 {
		t.Fatal("unknown agent flagged")
	}
}
