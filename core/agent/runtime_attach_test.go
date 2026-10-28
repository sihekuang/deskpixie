package agent_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
)

// fakeAttach is the waiting attachments (#240): paths handed over on Take, and the worker
// prompts it was asked to rewrite.
type fakeAttach struct {
	mu      sync.Mutex
	paths   []string
	takes   int
	workers [][2]string
}

func (f *fakeAttach) set(paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = paths
}

func (f *fakeAttach) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func (f *fakeAttach) Take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes++
	p := f.paths
	f.paths = nil
	return p
}

func (f *fakeAttach) ForWorker(text, dir string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workers = append(f.workers, [2]string{text, dir})
	return strings.ReplaceAll(text, "/a/", dir+"/.deskpixie/attachments/")
}

func (f *fakeAttach) Restore(paths []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, paths...)
}

func (f *fakeAttach) Takes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.takes
}

func lastPrompt(t *testing.T, r *rtRig) string {
	t.Helper()
	p := r.host.Prompts()
	if len(p) == 0 {
		t.Fatal("no prompt")
	}
	return p[len(p)-1].Text
}

func TestATypedRequestCarriesItsAttachments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.attach.set("/a/1.png", "/a/2.pdf")
		sent, err := r.h.ChatMessage("what's wrong?")
		if err != nil {
			t.Fatal(err)
		}
		// #394: the message says what it took, for the thread to show.
		if !slices.Equal(sent.Attached, []string{"/a/1.png", "/a/2.pdf"}) {
			t.Fatal(sent.Attached)
		}
		settle()
		if got := lastPrompt(t, r); !strings.Contains(got, "what's wrong?\nAttached: /a/1.png\nAttached: /a/2.pdf") {
			t.Fatal(got)
		}
		if r.attach.Len() != 0 {
			t.Fatal("not taken")
		}
	})
}

func TestAVoiceRequestCarriesThemToo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.attach.set("/a/1.png")
		if _, err := r.h.Delegate("fix it", ""); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := lastPrompt(t, r); !strings.Contains(got, "fix it\nAttached: /a/1.png") {
			t.Fatal(got)
		}
	})
}

func TestAnAnswerCarriesThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if _, err := r.h.ChatSend("fix the flaky test"); err != nil {
			t.Fatal(err)
		}
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Which screen?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		r.attach.set("/a/1.png")
		sent, err := r.h.ChatMessage("this one")
		if err != nil || !sent.Answered || !slices.Equal(sent.Attached, []string{"/a/1.png"}) {
			t.Fatal(sent, err)
		}
		settle()
		if got := lastPrompt(t, r); !strings.Contains(got, "this one\nAttached: /a/1.png") {
			t.Fatal(got)
		}
	})
}

func TestAttachmentsAloneAreARequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.attach.set("/a/1.png")
		if _, err := r.h.ChatSend("  "); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := lastPrompt(t, r); !strings.Contains(got, "See the attached files.\nAttached: /a/1.png") {
			t.Fatal(got)
		}
	})
}

func TestAttachmentsWaitWhenDelegationIsOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Input(CoordinatorFailedInput(HostError{Kind: HostGaveUp}))
		settle()
		r.attach.set("/a/1.png")
		if sent, err := r.h.ChatMessage("hello"); err == nil || sent.Attached != nil {
			t.Fatal("sent", sent)
		}
		if sent, err := r.h.ChatDelegate("Tell the docs session: hello"); err == nil || sent.Attached != nil {
			t.Fatal("sent", sent)
		}
		if r.attach.Takes() != 0 || r.attach.Len() != 1 {
			t.Fatal("taken while delegation is off")
		}
	})
}

// #240: a worker's prompt names its own copies of the job's attachments, in its folder.
func TestAWorkerPromptGetsLocalPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		c := startCall("focus")
		c.Worker.Task = "Fix the upload test.\nAttached: /a/1.png"
		okValue(t, call(r, Coordinator, c))
		settle()
		var brief string
		for _, p := range r.host.Prompts() {
			if p.Agent == wName {
				brief = p.Text
			}
		}
		const wt = "/projects/focus/.worktrees/upload"
		if !strings.Contains(brief, "Attached: "+wt+"/.deskpixie/attachments/1.png") {
			t.Fatal(brief)
		}
		r.attach.mu.Lock()
		defer r.attach.mu.Unlock()
		if len(r.attach.workers) == 0 || r.attach.workers[0][1] != wt {
			t.Fatal(r.attach.workers)
		}
	})
}

// A stop said to the job that asked isn't a message: the waiting attachments stay (#240 review).
func TestAStopLeavesTheAttachmentsWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "how is the suite going?")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Keep waiting?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		r.attach.set("/a/1.png")
		invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`)
		settle()
		if r.attach.Len() != 1 {
			t.Fatal("the stop took them")
		}
		if got := lastPrompt(t, r); strings.Contains(got, "Attached:") {
			t.Fatal(got)
		}
	})
}

// An answer with no question waiting becomes a request; with delegation off it can't go, and
// its attachments go back to waiting.
func TestAnAnswerThatCantGoGivesThemBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Input(CoordinatorFailedInput(HostError{Kind: HostGaveUp}))
		settle()
		r.attach.set("/a/1.png")
		if _, _, err := r.h.AnswerAsked("", "this one"); err == nil {
			t.Fatal("went")
		}
		if r.attach.Len() != 1 {
			t.Fatal("lost")
		}
	})
}

// #394: a reply to one session carries the attachments and says which it took.
func TestAReplyToASessionCarriesThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.attach.set("/a/1.png")
		sent, err := r.h.ChatDelegate("Tell the docs session: look")
		if err != nil || !slices.Equal(sent.Attached, []string{"/a/1.png"}) {
			t.Fatal(sent, err)
		}
		settle()
		if got := lastPrompt(t, r); !strings.Contains(got, "Tell the docs session: look\nAttached: /a/1.png") {
			t.Fatal(got)
		}
		if sent, err := r.h.ChatDelegate("Tell the docs session: again"); err != nil || sent.Attached != nil {
			t.Fatal("nothing was waiting", sent, err)
		}
	})
}
