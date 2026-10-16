package voice

// Client delegation (#70, experimental): a provider that hands a turn to the app without a
// typed tool call (GPT-Live's `delegation: {type: client}`) reports it as an EvToolCall for
// ClientDelegationTool. The conversation fills in the task from the transcript (the user's last
// words, with the earlier turns as context, as the #71 hand-off does), and the runtime's
// clientDelegation tool sends it to the coordinator: answer_job when the user is answering a
// "Coordinator question:", otherwise delegate.

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	// ClientDelegationTool is the pseudo tool an adapter names in EvToolCall for a client
	// delegation. Its CallID is the provider's delegation id; the conversation sets Args.
	ClientDelegationTool = "client_delegation"
	// AnswerJobTool is the coordinator's answer_job tool.
	AnswerJobTool = "answer_job"
	// CheckPrefix marks a delegated fact check (the delegation persona's "Check: ").
	CheckPrefix = "Check: "
	// CoordinatorQuestionPrefix leads a notice that asks the user something for a job.
	CoordinatorQuestionPrefix = "Coordinator question: "
)

// isCoordinatorQuestion: n asks the user something for a job (the older "Agent question:"
// lead-in too).
func isCoordinatorQuestion(text string) bool {
	return strings.HasPrefix(text, CoordinatorQuestionPrefix) || strings.HasPrefix(text, "Agent question: ")
}

// asksQuestion: the notice relays a "Coordinator question:" that is not a confirm prompt, on
// its own or as a line of the "while you were away" replay.
func asksQuestion(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
		if isCoordinatorQuestion(line) && !strings.Contains(line, "Say confirm") {
			return true
		}
	}
	return false
}

// ClientDelegationArgs are the arguments the conversation gives ClientDelegationTool.
type ClientDelegationArgs struct {
	Task    string `json:"task"`
	Context string `json:"context,omitempty"`
	// Answer: the user is answering a "Coordinator question:" relayed just before this turn.
	Answer bool `json:"answer,omitempty"`
}

// ClientDelegationResult is ClientDelegationTool's output, which the adapter turns into what
// the voice model is told.
type ClientDelegationResult struct {
	// Routed: "delegate", "answer_job" or "none" (nothing was sent to the coordinator).
	Routed string `json:"routed,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	// Check: a fact check; its answer comes back as a "Coordinator reply:" in this conversation.
	Check bool   `json:"check,omitempty"`
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

// DelegationOffNoAgent is the error when no coordinator is built (Settings ▸ Agent ▸ Delegation off).
const DelegationOffNoAgent = "Delegation is off (the coordinator is turned off in Settings)."

// clientDelegation is the runtime's ClientDelegationTool: it routes the task to the registry's
// answer_job or delegate tool.
type clientDelegation struct{ reg *ToolRegistry }

func (clientDelegation) Name() string        { return ClientDelegationTool }
func (clientDelegation) Description() string { return "Routes a client delegation to the coordinator." }
func (clientDelegation) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func resultOutcome(r ClientDelegationResult) ToolOutcome {
	b, _ := json.Marshal(r)
	return ToolOutcome{Output: b}
}

// jobResult reads a delegate/answer_job output: its job id or its error.
func jobResult(out json.RawMessage) (jobID, errText string) {
	var v struct {
		JobID string `json:"job_id"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &v)
	return v.JobID, v.Error
}

func (t clientDelegation) Invoke(ctx context.Context, raw json.RawMessage) ToolOutcome {
	var a ClientDelegationArgs
	_ = json.Unmarshal(raw, &a)
	words := strings.TrimSpace(strings.TrimPrefix(a.Task, CheckPrefix))
	if IsConfirmWords(words) {
		return resultOutcome(ClientDelegationResult{Routed: "none", Note: "a confirm word: the app handles it"})
	}
	del, ok := t.reg.Get(DelegateTool)
	if !ok {
		return resultOutcome(ClientDelegationResult{Error: DelegationOffNoAgent})
	}
	if a.Answer && words != "" {
		if aj, ok := t.reg.Get(AnswerJobTool); ok {
			args, _ := json.Marshal(map[string]string{"answer": words})
			id, e := jobResult(aj.Invoke(ctx, args).Output)
			switch {
			case id != "":
				return resultOutcome(ClientDelegationResult{Routed: "answer_job", JobID: id})
			case strings.Contains(e, "confirm word"):
				return resultOutcome(ClientDelegationResult{Routed: "none", Note: "the coordinator is waiting for a confirm word: the app handles it"})
			}
			// No job waiting after all: it's a new request.
		}
	}
	args := map[string]string{"task": a.Task}
	if a.Context != "" {
		args["context"] = a.Context
	}
	b, _ := json.Marshal(args)
	out := del.Invoke(ctx, b)
	id, e := jobResult(out.Output)
	if id == "" {
		if e == "" {
			e = "the coordinator did not take it"
		}
		return resultOutcome(ClientDelegationResult{Error: e})
	}
	o := resultOutcome(ClientDelegationResult{Routed: "delegate", JobID: id, Check: strings.HasPrefix(a.Task, CheckPrefix)})
	o.EndAfterReply = out.EndAfterReply
	return o
}

// routedHandOff is the hand-off (#71/#72) through the client delegation router: its result
// reads like the delegate tool's ({"job_id"} or {"error"}), for HandOffNotice.
type routedHandOff struct{ router Tool }

func (h routedHandOff) Invoke(ctx context.Context, raw json.RawMessage) ToolOutcome {
	var r ClientDelegationResult
	routed := h.router.Invoke(ctx, raw)
	_ = json.Unmarshal(routed.Output, &r)
	out := map[string]string{}
	switch {
	case r.JobID != "":
		out["job_id"] = r.JobID
	case r.Error != "":
		out["error"] = r.Error
	}
	b, _ := json.Marshal(out)
	return ToolOutcome{Output: b, EndAfterReply: routed.EndAfterReply}
}

// goodbyes end a conversation when they are the whole utterance or its end (normalized).
var goodbyes = []string{
	"bye", "goodbye", "bye bye", "good bye", "see you", "see ya", "see you later", "talk later",
	"talk to you later", "thats all", "thats it", "thats all bye", "im done", "were done",
	"good night", "later", "catch you later", "have a good one", "take care",
	"thats all for now", "thats it for now", "were done here", "nothing else", "never mind",
	"nevermind",
}

// courtesy words make up a closing like "okay, thank you" or "nope, I'm good, thanks": an
// utterance of only these, with a thanks or an "I'm good" in it, is a goodbye (#93). A thanks
// next to anything else ("set a timer, thanks") is not.
var courtesy = map[string]bool{
	"ok": true, "okay": true, "alright": true, "all": true, "right": true, "perfect": true,
	"great": true, "cool": true, "awesome": true, "nice": true, "wonderful": true, "got": true,
	"it": true, "sounds": true, "good": true, "no": true, "nope": true, "yeah": true, "yes": true,
	"yep": true, "well": true, "oh": true, "so": true, "um": true, "uh": true, "thanks": true,
	"thank": true, "you": true, "much": true, "a": true, "lot": true, "very": true, "again": true,
	"im": true, "thats": true, "for": true, "now": true, "set": true, "fine": true, "hey": true, "pixie": true, "bit": true,
}

// closers: the courtesy words that end a conversation by themselves.
var closers = []string{"thanks", "thank you", "im good", "im all good", "all good", "im fine", "im all set", "all set"}

// assistantNames trail a goodbye ("bye Pixie") without changing it.
var assistantNames = map[string]bool{"pixie": true, "bit": true}

// IsGoodbye reports whether the user's words end the conversation: a goodbye at the end, with
// at most a few polite words before it ("thanks, that's all, bye"), or a closing made only of
// courtesy words ("okay, thank you"), and no request.
func IsGoodbye(said string) bool {
	words := strings.Fields(normalize(said))
	for len(words) > 1 && assistantNames[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	if len(words) == 0 || len(words) > 8 {
		return false
	}
	text := strings.Join(words, " ")
	if hasAnyPhrase(text, requestAnywhere) || strings.HasSuffix(strings.TrimSpace(said), "?") {
		return false
	}
	for _, g := range goodbyes {
		if text == g || strings.HasSuffix(text, " "+g) {
			return true
		}
	}
	for _, w := range words {
		if !courtesy[w] {
			return false
		}
	}
	return hasAnyPhrase(text, closers)
}

// IsQuestion reports whether the user's words ask a question (not a request to do something).
func IsQuestion(said string) bool {
	words := strings.Fields(normalize(said))
	for len(words) > 0 && leadIns[words[0]] {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	text := strings.Join(words, " ")
	if hasPrefixPhrase(text, requestStarts) {
		return false
	}
	// "can you check …" / "could you …" ask for work, not a fact.
	for _, p := range []string{"can you", "could you", "would you", "will you", "can u"} {
		if strings.HasPrefix(text, p+" ") {
			return false
		}
	}
	return strings.HasSuffix(strings.TrimSpace(said), "?") || hasPrefixPhrase(text, questionStarts)
}

// confirmVerbs open a confirm-word answer ("confirm hawk"); the coordinator hears those itself.
var confirmVerbs = map[string]bool{"confirm": true, "allow": true, "approve": true}

// IsConfirmWords reports whether the words are a confirm-word answer: a confirm verb and a
// word, with nothing else but a lead-in.
func IsConfirmWords(said string) bool {
	words := strings.Fields(normalize(said))
	for len(words) > 0 && leadIns[words[0]] {
		words = words[1:]
	}
	return len(words) >= 2 && len(words) <= 3 && confirmVerbs[words[0]]
}
