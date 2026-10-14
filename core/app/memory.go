package app

// Bit's short-term memory (settings voice.shortTermMemory): what is recorded and how it joins
// the session. The store itself is voice.Memory; this is the composition: transcripts from the
// runtime's UI events, delegations from the agent's voice tools, coordinator lines from the
// notices the runtime actually gives the model, and the memory as one more SessionFacts.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"

	"github.com/sihekuang/deskpixie/core/voice"
)

// memoryRecorder records into a memory. A nil recorder (the setting is off) records nothing.
type memoryRecorder struct {
	mem *voice.Memory
	// muted: the user asked Bit to forget; the rest of this conversation isn't recorded.
	muted atomic.Bool
}

func (r *memoryRecorder) add(role voice.MemoryRole, text string) {
	if r == nil || r.muted.Load() {
		return
	}
	r.mem.Add(role, text)
}

// forgetTool is the forget_recent voice tool: clears the memory and mutes the rest of this
// conversation, so the request to forget isn't itself remembered.
func (r *memoryRecorder) forgetTool() voice.Tool {
	return voice.ForgetTool{Memory: r.mem, OnForget: func() { r.muted.Store(true) }}
}

// transcript records a runtime "transcript" event.
func (r *memoryRecorder) transcript(role, text string) {
	switch role {
	case "user":
		r.add(voice.MemUser, text)
	case "assistant":
		r.add(voice.MemBit, text)
	}
}

// memoryNoticePrefixes are the injected lines worth remembering: the coordinator's replies,
// questions and notes (progress narration and timer notices are not).
var memoryNoticePrefixes = []string{"Agent reply: ", "Agent question: ", "Agent says: ", "Agent replies while you were away:"}

// notice records a notice the model was just given.
func (r *memoryRecorder) notice(text string) {
	for _, p := range memoryNoticePrefixes {
		if strings.HasPrefix(text, p) {
			r.add(voice.MemCoordinator, strings.TrimSpace(strings.TrimPrefix(text, "Agent ")))
			return
		}
	}
}

// wrap records the delegations made through t (delegate, answer_job); other tools are as is.
func (r *memoryRecorder) wrap(t voice.Tool) voice.Tool {
	if r == nil {
		return t
	}
	switch t.Name() {
	case "delegate", "answer_job":
		return recordedTool{Tool: t, rec: r}
	}
	return t
}

type recordedTool struct {
	voice.Tool
	rec *memoryRecorder
}

func (t recordedTool) Invoke(ctx context.Context, args json.RawMessage) voice.ToolOutcome {
	out := t.Tool.Invoke(ctx, args)
	var in map[string]any
	_ = json.Unmarshal(args, &in)
	var res struct {
		JobID string `json:"job_id"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out.Output, &res)
	str := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
	var text string
	switch t.Name() {
	case "delegate":
		text = str("task")
		if c := str("context"); c != "" {
			text += " (context: " + c + ")"
		}
	case "answer_job":
		text = "answer to the agent's question: " + str("answer")
	}
	switch {
	case res.Error != "":
		text += " — failed: " + res.Error
	case res.JobID != "":
		text += " [ref " + res.JobID + "]"
	}
	t.rec.add(voice.MemDelegated, text)
	return out
}

// sessionFacts joins several SessionFacts with blank lines, skipping blank ones.
type sessionFacts []voice.SessionFacts

func (s sessionFacts) Facts() string {
	var parts []string
	for _, f := range s {
		if t := strings.TrimSpace(f.Facts()); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ForgetMemory clears Bit's short-term memory (also the `memory:forget` action and the
// forget_recent voice tool). A no-op when the memory is off.
func (a *App) ForgetMemory() error {
	if a.memory == nil {
		return nil
	}
	return a.memory.mem.Clear()
}
