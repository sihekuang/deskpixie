package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Shared helpers for controller_test.go and controller_worker_test.go (the Rust test modules'
// helpers; renamed where a name is taken elsewhere in the package).

// visible drops the persistence and log noise (and tab labels, the updates bubble's list and the
// reply cards, tested on their own).
func visible(fx []Effect) []Effect {
	var out []Effect
	for _, e := range fx {
		switch e.Kind {
		case EfAppend, EfRewrite, EfLog, EfLabel, EfUpdates, EfReply, EfLine, EfDelivered:
		default:
			out = append(out, e)
		}
	}
	return out
}

func fmtEffects(fx []Effect) string {
	parts := make([]string, len(fx))
	for k, e := range fx {
		parts[k] = fmtEffect(e)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func fmtEffect(e Effect) string {
	switch e.Kind {
	case EfStartCoordinator:
		return "StartCoordinator"
	case EfPrompt:
		return fmt.Sprintf("Prompt(%q)", e.Text)
	case EfInterrupt:
		return "Interrupt"
	case EfInject:
		return fmt.Sprintf("Inject(%q)", e.Notice.Text)
	case EfHoldOpen:
		return fmt.Sprintf("HoldOpen(%v)", e.Hold)
	case EfChime:
		return "Chime"
	case EfReply:
		return fmt.Sprintf("Reply(%d, %q)", e.Reply.ID, e.Reply.Text)
	case EfNotify:
		return fmt.Sprintf("Notify(%q)", e.Text)
	case EfCue:
		return fmt.Sprintf("Cue(%d)", e.Cue)
	case EfDecide:
		return fmt.Sprintf("Decide(%d, %+v)", e.ID, e.Decision)
	case EfAppend:
		return fmt.Sprintf("Append(%s, %s)", e.File, e.Line)
	case EfRewrite:
		return fmt.Sprintf("Rewrite(%s, %d lines)", e.File, len(e.Lines))
	case EfStatus:
		return fmt.Sprintf("Status(%q)", e.Text)
	case EfStopCoordinator:
		return "StopCoordinator"
	case EfResetHost:
		return "ResetHost"
	case EfLog:
		return fmt.Sprintf("Log(%q)", e.Text)
	case EfAnswerResult:
		return fmt.Sprintf("AnswerResult(%s, err %q)", e.Job, e.Err)
	case EfAdmitted:
		return fmt.Sprintf("Admitted(%+v, err %q)", e.Plan, e.Err)
	case EfToolResult:
		return fmt.Sprintf("ToolResult(%v, err %q)", e.Value, e.Err)
	case EfPromptWorker:
		return fmt.Sprintf("PromptWorker(%s, %q)", e.Name, e.Text)
	case EfSubmitWorker:
		return fmt.Sprintf("SubmitWorker(%s)", e.Name)
	case EfStopWorker:
		return fmt.Sprintf("StopWorker(%s)", e.Name)
	case EfReadWorker:
		return fmt.Sprintf("ReadWorker(%s)", e.Name)
	case EfForgetWorker:
		return fmt.Sprintf("ForgetWorker(%s)", e.Name)
	case EfLabel:
		return fmt.Sprintf("Label(%q)", e.Tab.Label)
	case EfOpenConversation:
		return "OpenConversation"
	case EfUpdates:
		return fmt.Sprintf("Updates(%+v)", e.Value)
	}
	return fmt.Sprintf("Effect(%d)", e.Kind)
}

// Effect builders for assertions.
func eInject(text string) Effect              { return Effect{Kind: EfInject, Notice: Notice{Text: text}} }
func eNotify(text string) Effect              { return Effect{Kind: EfNotify, Text: text} }
func eShowPane(j JobID, name string) Effect   { return Effect{Kind: EfShowPane, Job: j, Name: name} }
func ePrompt(text string) Effect              { return Effect{Kind: EfPrompt, Text: text} }
func eHold(on bool) Effect                    { return Effect{Kind: EfHoldOpen, Hold: on} }
func eStatus(line string) Effect              { return Effect{Kind: EfStatus, Text: line} }
func eCue(c Cue) Effect                       { return Effect{Kind: EfCue, Cue: c} }
func eDecide(id RequestID, d Decision) Effect { return Effect{Kind: EfDecide, ID: id, Decision: d} }
func eLog(text string) Effect                 { return Effect{Kind: EfLog, Text: text} }
func eStopWorker(name string) Effect          { return Effect{Kind: EfStopWorker, Name: name} }
func eForgetWorker(name string) Effect        { return Effect{Kind: EfForgetWorker, Name: name} }
func eReadWorker(name string) Effect          { return Effect{Kind: EfReadWorker, Name: name} }
func eSubmitWorker(name string) Effect        { return Effect{Kind: EfSubmitWorker, Name: name} }
func ePromptWorker(name, text string) Effect {
	return Effect{Kind: EfPromptWorker, Name: name, Text: text}
}

var (
	eStart     = Effect{Kind: EfStartCoordinator}
	eChime     = Effect{Kind: EfChime}
	eInterrupt = Effect{Kind: EfInterrupt}
	eStopCoord = Effect{Kind: EfStopCoordinator}
	eResetHost = Effect{Kind: EfResetHost}
	eOpen      = Effect{Kind: EfOpenConversation}
)

// plain drops a notification's bubble line (#199): tests that care check it on its own.
func plain(fx []Effect) []Effect {
	out := make([]Effect, len(fx))
	for i, e := range fx {
		e.Update = nil
		e.Notice.Sessions = nil // checked on their own (#261)
		e.Chat.Sessions = nil
		out[i] = e
	}
	return out
}

func effectsEqual(a, b []Effect) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(plain(a), plain(b))
}

func wantEffects(t *testing.T, got []Effect, want ...Effect) {
	t.Helper()
	if !effectsEqual(got, want) {
		t.Fatalf("effects\n got %s\nwant %s", fmtEffects(got), fmtEffects(want))
	}
}

func wantNone(t *testing.T, got []Effect, why ...string) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("want no effects %v, got %s", why, fmtEffects(got))
	}
}

func contains(fx []Effect, e Effect) bool {
	for _, f := range plain(fx) {
		if reflect.DeepEqual(f, plain([]Effect{e})[0]) {
			return true
		}
	}
	return false
}

func wantContains(t *testing.T, fx []Effect, e Effect, why ...string) {
	t.Helper()
	if !contains(fx, e) {
		t.Fatalf("want %s in %s %v", fmtEffect(e), fmtEffects(fx), why)
	}
}

func wantNotContains(t *testing.T, fx []Effect, e Effect, why ...string) {
	t.Helper()
	if contains(fx, e) {
		t.Fatalf("want no %s in %s %v", fmtEffect(e), fmtEffects(fx), why)
	}
}

func anyKind(fx []Effect, kinds ...EffectKind) bool {
	for _, f := range fx {
		for _, k := range kinds {
			if f.Kind == k {
				return true
			}
		}
	}
	return false
}

func prompts(fx []Effect) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfPrompt {
			out = append(out, e.Text)
		}
	}
	return out
}

func wantPrompts(t *testing.T, fx []Effect, want ...string) {
	t.Helper()
	got := prompts(fx)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prompts\n got %q\nwant %q", got, want)
	}
}

func jobStatusOf(t *testing.T, c *AgentController, n uint64) JobStatus {
	t.Helper()
	for _, v := range c.Snapshot(T0) {
		if v.JobID == job(n).String() {
			return v.Status
		}
	}
	t.Fatalf("no job %d in the snapshot", n)
	return 0
}

func wantJobStatus(t *testing.T, c *AgentController, n uint64, want JobStatus, why ...string) {
	t.Helper()
	if got := jobStatusOf(t, c, n); got != want {
		t.Fatalf("job %d: status %v, want %v %v", n, got, want, why)
	}
}

func jsonEq(t *testing.T, got, want any, why ...string) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var gv, wv any
	_ = json.Unmarshal(g, &gv)
	_ = json.Unmarshal(w, &wv)
	if !reflect.DeepEqual(gv, wv) {
		t.Fatalf("json\n got %s\nwant %s %v", g, w, why)
	}
}

func coordStatus(status PaneStatus) Input {
	return PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: Coordinator, Status: status})
}

func paneStatus(agent string, status PaneStatus) Input {
	return PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: agent, Status: status})
}

func paneExited(agent string) Input {
	return PaneInput(PaneEvent{Kind: PaneEventExited, Agent: agent})
}

func voiceOpened() Input { return VoiceInput(VoiceSignal{Kind: voice.SigOpened}) }

func voiceClosed(undelivered ...Notice) Input {
	return VoiceInput(VoiceSignal{Kind: voice.SigClosed, Undelivered: undelivered})
}

func userSaid(text string) Input { return VoiceInput(VoiceSignal{Kind: voice.SigUserSaid, Text: text}) }
