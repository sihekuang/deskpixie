package agent

import "testing"

// #177: when the coordinator's message reaches one of your sessions, Pixie says so at once,
// not after the coordinator has written its reply (3 to 20 s later). That reply, when it only
// repeats the hand-off, is recorded but not said again.

const landing = "deskpixie-landing: continue search-console SEO branch"

var handedOff = eInject("Coordinator reply: Passed on to your session on " + landing + ".")

func TestAHandOffIsSaidAtOnceAndAReplyThatOnlyRepeatsItIsNot(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	wantEffects(t, visible(c.Step(SessionSentInput("repo-claude", landing), T0)), handedOff)
	wantJobStatus(t, c, 1, JobWorking)
	fx := visible(stop(c, `<spoken ref="job_000000000001" status="done">I sent your words to the landing page session.</spoken>`, T0+5_000))
	wantContains(t, fx, eHold(false))
	for _, e := range fx {
		if e.Kind == EfInject || e.Kind == EfNotify {
			t.Fatalf("said again: %+v", fx)
		}
	}
	wantJobStatus(t, c, 1, JobDone)
}

// A reply after more work (a command run after the hand-off), a question or a failure is
// news: it is said as always.
func TestAReplyAfterAHandOffIsSaidWhenItIsNews(t *testing.T) {
	news := map[string]func(c *AgentController) string{
		"more work": func(c *AgentController) string {
			c.Step(PostToolInput(Coordinator, "Bash", map[string]any{"description": "Check the site"}), T0+1_000)
			return `<spoken ref="job_000000000001" status="done">Sent, and the site is up.</spoken>`
		},
		"a question": func(*AgentController) string {
			return `<spoken ref="job_000000000001" status="question">Sent. Should it push too?</spoken>`
		},
		"a failure": func(*AgentController) string {
			return `<spoken ref="job_000000000001" status="failed">It stopped responding.</spoken>`
		},
	}
	for name, then := range news {
		c := workingOnJob1()
		openVoice(c)
		c.Step(SessionSentInput("repo-claude", landing), T0)
		reply := then(c)
		fx := visible(stop(c, reply, T0+5_000))
		said := false
		for _, e := range fx {
			said = said || e.Kind == EfInject
		}
		if !said {
			t.Fatalf("%s: not said: %+v", name, fx)
		}
	}
}

// With two requests in flight, the hand-off belongs to neither for sure: nothing is said early.
func TestAHandOffWithTwoRequestsInFlightWaitsForTheReply(t *testing.T) {
	c := ctl()
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	openVoice(c)
	wantNone(t, visible(c.Step(SessionSentInput("repo-claude", landing), T0)))
}
