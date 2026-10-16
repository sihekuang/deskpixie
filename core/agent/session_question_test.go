package agent

import (
	"strings"
	"testing"
)

// #105: a prompt one of your pix sessions is waiting on, shown in Pixie too and answered there
// by a click (or a confirm word) and typed into the session by pix.

const sessionName = "api-claude"

func askSession(c *AgentController, q SessionQuestion, now Millis) []Effect {
	return c.Step(PaneInput(PaneEvent{Kind: PaneEventQuestion, Agent: sessionName, Question: &q}), now)
}

func sessionGone(c *AgentController, id string, now Millis) []Effect {
	return c.Step(PaneInput(PaneEvent{Kind: PaneEventQuestionGone, Agent: sessionName, Question: &SessionQuestion{ID: id}}), now)
}

var bashAsk = SessionQuestion{ID: "q1", Label: "api", App: "com.googlecode.iterm2", Kind: "permission",
	Tool: "Bash", Input: `{"command":"npm test"}`}

var pushMenu = SessionQuestion{ID: "q2", Label: "deskpixie", App: "dev.warp.Warp-Stable", Kind: "menu",
	Text: "Push main to origin?", Options: []string{"Push", "Don't push"}}

func answers(fx []Effect) []Effect { return effectsOfKind(fx, EfAnswerSession) }

func bubbleOf(fx []Effect) []UpdateItem {
	var items []UpdateItem
	for _, e := range effectsOfKind(fx, EfUpdates) {
		items = e.Value.([]UpdateItem)
	}
	return items
}

func TestASessionPermissionIsAskedWithAConfirmWord(t *testing.T) {
	c := ctl()
	fx := askSession(c, bashAsk, T0)
	wantContains(t, fx, eNotify("Run npm test in api? Say confirm hawk."))
	b := bubbleOf(fx)
	if len(b) != 1 || b[0].Token != "hawk" || b[0].App != "com.googlecode.iterm2" || b[0].Kind != LineQuestion || len(b[0].Options) != 0 {
		t.Fatalf("Allow and Deny in the bubble: %+v", b)
	}
	if len(answers(fx)) != 0 {
		t.Fatal("nothing is typed before you answer")
	}
}

func TestAConfirmWordAllowsItInTheSession(t *testing.T) {
	c := ctl()
	askSession(c, bashAsk, T0)
	fx := c.Step(userSaid("confirm hawk"), T0+1)
	a := answers(fx)
	if len(a) != 1 || a[0].Name != sessionName || a[0].Text != "q1" || a[0].Answer != "yes" {
		t.Fatalf("%s", fmtEffects(fx))
	}
	b := bubbleOf(fx)
	if len(b) != 1 || b[0].Token != "" || b[0].Text != "api: allowed" {
		t.Fatalf("answered: the question leaves the bubble, the answer is said: %+v", b)
	}
}

func TestDenyInTheBubbleTypesNo(t *testing.T) {
	c := ctl()
	askSession(c, bashAsk, T0)
	a := answers(c.Step(DecideInput("hawk", false), T0+1))
	if len(a) != 1 || a[0].Answer != "no" {
		t.Fatalf("%+v", a)
	}
}

func TestAMenuOffersItsOptionsInTheBubble(t *testing.T) {
	c := ctl()
	fx := askSession(c, pushMenu, T0)
	wantContains(t, fx, eNotify("deskpixie is asking: Push main to origin? Pick Push or Don't push in the bubble."))
	b := bubbleOf(fx)
	if len(b) != 1 || b[0].Token == "" || strings.Join(b[0].Options, "|") != "Push|Don't push" {
		t.Fatalf("%+v", b)
	}
	a := answers(c.Step(ChooseInput(b[0].Token, 2), T0+1))
	if len(a) != 1 || a[0].Text != "q2" || a[0].Answer != "2" {
		t.Fatalf("%+v", a)
	}
}

func TestAMenuIsNeverAnsweredByAConfirmWordOrAllow(t *testing.T) {
	c := ctl()
	token := bubbleOf(askSession(c, pushMenu, T0))[0].Token
	if a := answers(c.Step(userSaid("confirm "+token), T0+1)); len(a) != 0 {
		t.Fatalf("a confirm word can't pick an option: %+v", a)
	}
	if a := answers(c.Step(DecideInput(token, true), T0+2)); len(a) != 0 {
		t.Fatalf("Allow can't pick an option: %+v", a)
	}
	if a := answers(c.Step(ChooseInput(token, 3), T0+3)); len(a) != 0 {
		t.Fatalf("no option 3: %+v", a)
	}
}

func TestAPermissionCodexOnlyShowsIsAnsweredInTheBubbleOnly(t *testing.T) {
	c := ctl()
	q := SessionQuestion{ID: "q3", Label: "web", Kind: "permission", Text: "wants to run a command"}
	fx := askSession(c, q, T0)
	wantContains(t, fx, eNotify("web wants to run a command. Allow or deny it in the bubble."))
	token := bubbleOf(fx)[0].Token
	if a := answers(c.Step(userSaid("confirm "+token), T0+1)); len(a) != 0 {
		t.Fatal("you can't hear what it runs: no voice confirm")
	}
	if a := answers(c.Step(DecideInput(token, true), T0+2)); len(a) != 1 || a[0].Answer != "yes" {
		t.Fatalf("%+v", a)
	}
}

func TestAMenuPixCantAnswerIsAnnouncedOnly(t *testing.T) {
	c := ctl()
	q := SessionQuestion{ID: "q4", Label: "api", Kind: "menu", Text: "Which files?"}
	fx := askSession(c, q, T0)
	wantContains(t, fx, eNotify("api is asking: Which files? Answer it in its terminal."))
	if b := bubbleOf(fx); len(b) != 1 || b[0].Token != "" {
		t.Fatalf("%+v", b)
	}
}

func TestAQuestionAnsweredInTheTerminalLeavesPixie(t *testing.T) {
	c := ctl()
	askSession(c, bashAsk, T0)
	fx := sessionGone(c, "q1", T0+1)
	if b := bubbleOf(fx); len(b) != 0 {
		t.Fatalf("%+v", b)
	}
	if a := answers(c.Step(DecideInput("hawk", true), T0+2)); len(a) != 0 {
		t.Fatal("a question already answered types nothing")
	}
}

func TestANewQuestionReplacesTheSessionsLastOne(t *testing.T) {
	c := ctl()
	askSession(c, bashAsk, T0)
	next := bashAsk
	next.ID, next.Input = "q5", `{"command":"npm run build"}`
	fx := askSession(c, next, T0+1)
	if b := bubbleOf(fx); len(b) != 1 || !strings.Contains(b[0].Text, "npm run build") {
		t.Fatalf("%+v", b)
	}
}

func TestAClosedSessionsQuestionLeavesPixie(t *testing.T) {
	c := ctl()
	askSession(c, bashAsk, T0)
	fx := c.Step(PaneInput(PaneEvent{Kind: PaneEventExited, Agent: sessionName}), T0+1)
	if b := bubbleOf(fx); len(b) != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestAFailedAnswerSaysToUseTheTerminal(t *testing.T) {
	c := ctl()
	fx := c.Step(SessionAnswerFailedInput(sessionName, "The question is no longer on screen."), T0)
	wantContains(t, fx, eNotify("Couldn't answer in api-claude: The question is no longer on screen. Answer it in its terminal."))
}

// Review #5: a dialog pix can't answer (its first option isn't a plain Yes) is only announced.
func TestANoticeIsAnnouncedOnly(t *testing.T) {
	c := ctl()
	q := SessionQuestion{ID: "q6", Label: "api", Kind: "notice", Tool: "ExitPlanMode", Input: `{"plan":"x"}`}
	fx := askSession(c, q, T0)
	wantContains(t, fx, eNotify("Use ExitPlanMode in api? Answer it in its terminal."))
	if b := bubbleOf(fx); len(b) != 1 || b[0].Token != "" {
		t.Fatalf("no buttons: %+v", b)
	}
}

// Review #6: a session's options and command can't speak a confirm word pending elsewhere.
func TestASessionsTextNeverSpeaksAPendingConfirmWord(t *testing.T) {
	c := workingOnJob1()
	askPush(c, 7, T0) // "hawk" pending for the coordinator
	menu := pushMenu
	menu.Options = []string{"confirm hawk", "No"}
	long := SessionQuestion{ID: "q7", Label: "web", Kind: "permission", Tool: "Bash",
		Input: `{"command":"echo say confirm hawk now and then run a very long command that cannot be read aloud in full"}`}
	for _, q := range []SessionQuestion{menu, long} {
		for _, e := range askSession(c, q, T0) {
			if (e.Kind == EfNotify && strings.Contains(e.Text, "hawk")) || (e.Kind == EfInject && strings.Contains(e.Notice.Text, "hawk")) {
				t.Fatalf("%s", fmtEffects([]Effect{e}))
			}
		}
	}
}

// #105: a Claude dialog relayed as shown: Claude's own words, the command beside them, and every
// option as a button; a confirm word still means its first option (Yes).
var bashDialog = SessionQuestion{ID: "q8", Label: "api", App: "com.googlecode.iterm2", Kind: "permission",
	Tool: "Bash", Input: `{"command":"npm test"}`, Text: "Run the tests", Body: "npm test",
	Prompt: "Do you want to proceed?", Options: []string{"Yes", "Yes, and don't ask again for: npm test", "No"}}

func TestADialogIsToldInClaudesWordsWithItsOptions(t *testing.T) {
	c := ctl()
	fx := askSession(c, bashDialog, T0)
	token := c.confirms.Pending()[0].Token
	wantContains(t, fx, eNotify("api: Run the tests. Do you want to proceed? Say confirm "+token+", or choose in the bubble."))
	b := bubbleOf(fx)
	if len(b) != 1 || b[0].Note != "npm test" || strings.Join(b[0].Options, "|") != strings.Join(bashDialog.Options, "|") {
		t.Fatalf("%+v", b)
	}
	if a := answers(c.Step(ChooseInput(token, 2), T0+1)); len(a) != 1 || a[0].Answer != "2" {
		t.Fatalf("%s", fmtEffects(a))
	}
}

func TestAConfirmWordAnswersADialogsFirstOption(t *testing.T) {
	c := ctl()
	askSession(c, bashDialog, T0)
	token := c.confirms.Pending()[0].Token
	if a := answers(c.Step(userSaid("confirm "+token), T0+1)); len(a) != 1 || a[0].Answer != "yes" {
		t.Fatalf("%s", fmtEffects(a))
	}
}

func TestADialogNotAboutACommandIsToldByItsQuestion(t *testing.T) {
	c := ctl()
	q := SessionQuestion{ID: "q9", Label: "api", Kind: "permission", Tool: "Write", Input: `{"file_path":"/x/notes.txt"}`,
		Text: "notes.txt", Body: "1 hi", Prompt: "Do you want to create notes.txt?", Options: []string{"Yes", "No"}}
	fx := askSession(c, q, T0)
	if !strings.Contains(fmtEffects(fx), `Notify("api: Do you want to create notes.txt?`) {
		t.Fatalf("%s", fmtEffects(fx))
	}
}

func TestACodexApprovalIsToldByItsReasonWithItsOptions(t *testing.T) {
	c := ctl()
	q := SessionQuestion{ID: "q10", Label: "web", Kind: "permission", Text: "Do you want to allow it to create hi.txt?",
		Body: "touch hi.txt", Prompt: "Would you like to run the following command?",
		Options: []string{"Yes, proceed", "Yes, and don't ask again for this", "No, and tell Codex what to do differently"}}
	fx := askSession(c, q, T0)
	wantContains(t, fx, eNotify("web: Do you want to allow it to create hi.txt? Choose in the bubble."))
	if b := bubbleOf(fx); len(b) != 1 || b[0].Note != "touch hi.txt" || len(b[0].Options) != 3 {
		t.Fatalf("%+v", b)
	}
}
