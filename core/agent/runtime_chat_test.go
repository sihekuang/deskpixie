package agent_test

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// recChat records the text box's lines.
type recChat struct {
	mu    sync.Mutex
	lines []ChatLine
}

func (r *recChat) Chat(l ChatLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, l)
}

func (r *recChat) Lines() []ChatLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

func TestChatSendDelegatesATypedJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		id, err := r.h.ChatSend("  how are my workers doing?  ")
		if err != nil || id != JobID("job_000000000001") {
			t.Fatal(id, err)
		}
		settle()
		prompts := r.host.Prompts()
		if len(prompts) != 1 || !strings.Contains(prompts[0].Text, "how are my workers doing?") ||
			!strings.Contains(prompts[0].Text, "typed this in DeskPixie's text box") {
			t.Fatal(prompts)
		}
		stopHook(r, `<spoken ref="job_000000000001" status="done">Two are running.</spoken>`)
		settle()
		if got := r.chat.Lines(); len(got) != 1 || !reflect.DeepEqual(got[0], ChatLine{Job: JobID("job_000000000001"), Status: "done", Text: "Two are running."}) {
			t.Fatal(got)
		}
		if hasAlert(r.alerts.Alerts(), agenttest.NotifyAlert("Pixie", "Two are running.")) {
			t.Fatal("a typed answer is not notified", r.alerts.Alerts())
		}
	})
}

func TestChatSendRejectsEmptyText(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("   "); err == nil || err.Error() != "Type a message first." {
			t.Fatal(err)
		}
		settle()
		if len(r.h.JobStatus()) != 0 {
			t.Fatal("a job for nothing")
		}
	})
}

func TestChatSendAnswersAWaitingTypedQuestion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("fix the flaky test"); err != nil {
			t.Fatal(err)
		}
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		id, err := r.h.ChatSend("deskpixie")
		if err != nil || id != JobID("job_000000000001") {
			t.Fatal("the answer goes to the waiting job", id, err)
		}
		settle()
		if n := len(r.h.JobStatus()); n != 1 {
			t.Fatal("no new job for an answer", n)
		}
		prompts := r.host.Prompts()
		if !strings.Contains(prompts[len(prompts)-1].Text, "The user answered: deskpixie") {
			t.Fatal(prompts)
		}
	})
}

func TestChatSendSaysWhyWhenDelegationIsOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Input(CoordinatorFailedInput(HostError{Kind: HostGaveUp}))
		settle()
		if _, err := r.h.ChatSend("hello"); err == nil || !strings.HasPrefix(err.Error(), "Delegation is off") {
			t.Fatal(err)
		}
	})
}

func TestChatStopCancelsTheTypedJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("a slow one"); err != nil {
			t.Fatal(err)
		}
		settle()
		r.h.ChatStop()
		settle()
		keys := r.host.Keys()
		if len(keys) != 1 || !slices.Equal(keys[0].Keys, []string{"Escape"}) {
			t.Fatal("the coordinator is interrupted", keys)
		}
		if r.h.JobStatus()[0].Status != JobCancelled {
			t.Fatal(r.h.JobStatus())
		}
		r.h.ChatStop() // nothing open: no-op
		settle()
		if len(r.host.Keys()) != 1 {
			t.Fatal(r.host.Keys())
		}
	})
}

// #302: the thread records the answer on the question it answered.
func TestChatMessageSaysItAnsweredTheWaitingQuestion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		sent, err := r.h.ChatMessage("fix the flaky test")
		if err != nil || sent != (ChatSent{Job: JobID("job_000000000001")}) {
			t.Fatal("a new job answers nothing", sent, err)
		}
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		sent, err = r.h.ChatMessage("deskpixie")
		if err != nil || sent != (ChatSent{Job: JobID("job_000000000001"), Answered: true}) {
			t.Fatal(sent, err)
		}
	})
}

// #302: a reply to a session is never taken for the answer to a question waiting in the box.
func TestChatDelegateIsANewJobWhileAQuestionWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("fix the flaky test"); err != nil {
			t.Fatal(err)
		}
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		id, err := r.h.ChatDelegate("Tell the docs session: ship it")
		if err != nil || id != JobID("job_000000000002") {
			t.Fatal("a new job", id, err)
		}
		settle()
		prompts := r.host.Prompts()
		if last := prompts[len(prompts)-1].Text; strings.Contains(last, "The user answered") || !strings.Contains(last, "Tell the docs session: ship it") {
			t.Fatal(prompts)
		}
		if _, err := r.h.ChatDelegate("  "); err == nil {
			t.Fatal("an empty message went")
		}
	})
}
