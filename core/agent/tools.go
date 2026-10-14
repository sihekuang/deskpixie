package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

// The voice tools (spec §5.1): delegate, jobs, answer_job. Registered in the voice loop's
// ToolRegistry by the composition root, with DelegationPersona appended to the persona. Each
// returns at once and never ends the conversation.
// Port of ai-mascot crates/agent-core/src/tools.rs.

func toolReply(output map[string]any) voice.ToolOutcome {
	b, err := json.Marshal(output)
	if err != nil {
		b = []byte(`{"error":"internal error"}`)
	}
	return voice.ToolOutcome{Output: b}
}

func toolError(err error) voice.ToolOutcome { return toolReply(map[string]any{"error": err.Error()}) }

// textArg is args[key] trimmed; "" when missing, not a string, or blank.
func textArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func decodeArgs(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// Delegate is the `delegate` tool.
type Delegate struct{ H *AgentHandle }

func (Delegate) Name() string { return "delegate" }

func (Delegate) Description() string {
	return "Hand a task to the capable agent working in the background. This is the default for every " +
		"request: use it for anything beyond small talk, simple stable facts you are certain of, what the " +
		"user just told you, and your own tools. That includes research, facts to check, live information " +
		"(news, prices, scores), the user's projects, files, code, repositories, apps and settings, writing, " +
		"explanations, advice, summaries, charts, diagrams or pages. When unsure, call it; don't try it " +
		"yourself first or ask whether to. Not for the time, the weather, arithmetic, timers or music " +
		"controls: get_time, get_weather, calculate, timer and media answer those at once. The agent does " +
		"not hear the conversation, so the task and context must stand on their own. Call this BEFORE " +
		"saying you will hand something off. Returns at once with a job id; then say one short " +
		"acknowledgement; the result arrives later as an \"Agent reply:\" line."
}

func (Delegate) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"task":{"type":"string","description":"What the user wants done, in their words."},` +
		`"context":{"type":"string","description":"Everything from the conversation the agent needs, since it cannot hear it: what pronouns refer to, names, numbers, choices, earlier Agent replies."}},` +
		`"required":["task"],"additionalProperties":false}`)
}

func (d Delegate) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	args := decodeArgs(raw)
	task := textArg(args, "task")
	if task == "" {
		return toolReply(map[string]any{"error": "task is required"})
	}
	id, err := d.H.Delegate(task, textArg(args, "context"))
	if err != nil {
		return toolError(err)
	}
	out := toolReply(map[string]any{"job_id": id.String(), "status": "delegated"})
	// Work: Bit says it handed it off, then goes idle; the result comes back on its own. A fact
	// check stays open so Bit can answer it aloud.
	out.EndAfterReply = !strings.HasPrefix(task, "Check:")
	return out
}

// Jobs is the `jobs` tool: what the agent's jobs are doing, or cancel one. One tool for both
// keeps the voice model's tool list short.
type Jobs struct{ H *AgentHandle }

func (Jobs) Name() string { return "jobs" }

func (Jobs) Description() string {
	return "The agent's jobs. action \"status\": what each job is doing (status, age in seconds, latest " +
		"progress), to answer \"what's the agent doing?\". action \"cancel\": stop a job; without job_id it " +
		"cancels the only open job, and if several are open it returns an error asking which."
}

func (Jobs) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"action":{"type":"string","enum":["status","cancel"]},` +
		`"job_id":{"type":"string","description":"For cancel: e.g. job_0123456789ab; only if several jobs are open."}},` +
		`"required":["action"],"additionalProperties":false}`)
}

func (j Jobs) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	args := decodeArgs(raw)
	action, _ := args["action"].(string)
	switch action {
	case "status":
		views := j.H.JobStatus()
		if views == nil {
			views = []JobView{}
		}
		return toolReply(map[string]any{"jobs": views})
	case "cancel":
		id, err := j.H.Cancel(textArg(args, "job_id"))
		if err != nil {
			return toolError(err)
		}
		return toolReply(map[string]any{"job_id": id.String(), "status": "cancelled"})
	}
	return toolReply(map[string]any{"error": "action must be status or cancel"})
}

// AnswerJob is the `answer_job` tool.
type AnswerJob struct{ H *AgentHandle }

func (AnswerJob) Name() string { return "answer_job" }

func (AnswerJob) Description() string {
	return "Give the agent the user's spoken answer to an \"Agent question:\" you relayed. Never use it for a " +
		"confirm word: the app handles those itself. Without job_id it answers the only job waiting for an " +
		"answer; if several are waiting it returns an error asking which."
}

func (AnswerJob) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"answer":{"type":"string","description":"The user's answer, in their words."},` +
		`"job_id":{"type":"string","description":"e.g. job_0123456789ab; only if several jobs are waiting."}},` +
		`"required":["answer"],"additionalProperties":false}`)
}

func (a AnswerJob) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	args := decodeArgs(raw)
	answer := textArg(args, "answer")
	if answer == "" {
		return toolReply(map[string]any{"error": "answer is required"})
	}
	id, err := a.H.Answer(textArg(args, "job_id"), answer)
	if err != nil {
		return toolError(err)
	}
	return toolReply(map[string]any{"job_id": id.String(), "status": "answered"})
}

var (
	_ voice.Tool = Delegate{}
	_ voice.Tool = Jobs{}
	_ voice.Tool = AnswerJob{}
)
