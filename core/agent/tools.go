package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

// The voice tools (spec §5.1): delegate, jobs, answer_job, focus_session. Registered in the voice loop's
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

func (Delegate) Name() string { return voice.DelegateTool }

func (Delegate) Description() string {
	return "Hand a task to the coordinator, the capable agent working in the background. This is the default for every " +
		"request: use it for anything beyond small talk, simple stable facts you are certain of, what the " +
		"user just told you, and your own tools. That includes research, facts to check, live information " +
		"(news, prices, scores), the user's projects, files, code, repositories, apps and settings, writing, " +
		"explanations, advice, summaries, charts, diagrams or pages. When unsure, call it; don't try it " +
		"yourself first or ask whether to. Not for the time, the weather, arithmetic, timers or music " +
		"controls: get_time, get_weather, calculate, timer and media answer those at once. The coordinator does " +
		"not hear the conversation, so the task and context must stand on their own. Call this BEFORE " +
		"saying you will hand something off. Returns at once with a job id; then say one short " +
		"acknowledgement; the result arrives later as an \"Coordinator reply:\" line."
}

func (Delegate) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"task":{"type":"string","description":"What the user said, word for word, as they said it, with every request, step, correction and change of mind in it. Don't split, reorder or reword it: the coordinator works out what they meant."},` +
		`"context":{"type":"string","description":"What the user's words point back to from earlier in the conversation, since the coordinator cannot hear it: what pronouns refer to, names, numbers, choices, earlier Coordinator replies."}},` +
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
	// Work: Pixie says it handed it off, then goes idle; the result comes back on its own. A fact
	// check stays open so Pixie can answer it aloud.
	out.EndAfterReply = !strings.HasPrefix(task, "Check:")
	return out
}

// Jobs is the `jobs` tool: what the agent's jobs are doing, or cancel one. One tool for both
// keeps the voice model's tool list short.
type Jobs struct{ H *AgentHandle }

func (Jobs) Name() string { return "jobs" }

func (Jobs) Description() string {
	return "The coordinator's jobs. action \"status\": what each job is doing (status, age in seconds, latest " +
		"progress), to answer \"what's the coordinator doing?\". Not the user's sessions (\"how many sessions are open?\"): " +
		"delegate those. action \"cancel\": stop a job; without job_id it " +
		"cancels the only open job, and if several are open it returns an error asking which."
}

func (Jobs) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"action":{"type":"string","enum":["status","cancel"]},` +
		`"job_id":{"type":"string","description":"For cancel: the job's id from jobs status, only if several jobs are open."}},` +
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
		id, to, err := j.H.CancelAsked(textArg(args, "job_id"))
		if err != nil {
			return toolError(err)
		}
		if to != "" {
			return toolReply(map[string]any{"job_id": id.String(), "status": "answered", "to": to})
		}
		return toolReply(map[string]any{"job_id": id.String(), "status": "cancelled"})
	}
	return toolReply(map[string]any{"error": "action must be status or cancel"})
}

// AnswerJob is the `answer_job` tool.
type AnswerJob struct{ H *AgentHandle }

func (AnswerJob) Name() string { return "answer_job" }

func (AnswerJob) Description() string {
	return "Give the user's spoken answer to the question you relayed last, a job's or a session's: the coordinator's " +
		"\"Coordinator question:\", or a \"Session update:\" that says a session is asking. Never use it for a confirm word: the app handles those " +
		"itself. Leave out job_id: the app knows who asked, and the coordinator sorts out which of several questions it answers."
}

func (AnswerJob) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"answer":{"type":"string","description":"The user's answer, in their words."},` +
		`"job_id":{"type":"string","description":"Leave it out. Only a job_id exactly as a line gave it."}},` +
		`"required":["answer"],"additionalProperties":false}`)
}

func (a AnswerJob) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	args := decodeArgs(raw)
	answer := textArg(args, "answer")
	if answer == "" {
		return toolReply(map[string]any{"error": "answer is required"})
	}
	id, to, err := a.H.AnswerAsked(textArg(args, "job_id"), answer)
	if err != nil {
		return toolError(err)
	}
	if to != "" {
		return toolReply(map[string]any{"to": to, "status": "answered"})
	}
	return toolReply(map[string]any{"job_id": id.String(), "status": "answered"})
}

// FocusSessionTool is the `focus_session` tool (#170): bring one of the user's own sessions to
// the front, as a click on its roster row does.
type FocusSessionTool struct{ H *AgentHandle }

func (FocusSessionTool) Name() string { return "focus_session" }

func (FocusSessionTool) Description() string {
	return "Bring one of the user's own sessions (listed under \"The user's own sessions\") to the front of the screen, " +
		"when they ask to see, show, pull up, switch to or focus it. Only for showing it: anything else about a session " +
		"is delegated."
}

func (FocusSessionTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"name":{"type":"string","description":"The session's name exactly as listed, e.g. the part before the colon."}},` +
		`"required":["name"],"additionalProperties":false}`)
}

func (f FocusSessionTool) Invoke(ctx context.Context, raw json.RawMessage) voice.ToolOutcome {
	name := textArg(decodeArgs(raw), "name")
	var err error = LaunchError("name is required")
	if name != "" {
		if err = f.H.FocusSession(ctx, name); err == nil {
			return toolReply(map[string]any{"name": name, "status": "shown"})
		}
	}
	// The sessions as they are now, so Pixie can ask which one.
	return toolReply(map[string]any{"error": err.Error(), "your_sessions": f.H.YourSessionLines()})
}

var (
	_ voice.Tool = Delegate{}
	_ voice.Tool = Jobs{}
	_ voice.Tool = AnswerJob{}
	_ voice.Tool = FocusSessionTool{}
)
