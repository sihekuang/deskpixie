package agent

import "strings"

// Attachments are the files waiting for the user's next request (#240): the next request or
// answer takes them, as `Attached: <path>` lines, and a worker's prompt gets them copied into
// its folder. The store in core/attach is the real one.
type Attachments interface {
	Len() int
	// Take hands the waiting attachments over and returns their paths (nil when none).
	Take() []string
	// Restore puts paths Take returned back to waiting (what took them couldn't go).
	Restore(paths []string)
	// ForWorker copies the attachments text names into the worker folder dir and returns the
	// text naming the copies.
	ForWorker(text, dir string) string
}

// attachedOnly is the request when the user sent attachments with no words.
const attachedOnly = "See the attached files."

// attached takes the waiting attachments as the lines that end a request or an answer.
func (h *AgentHandle) attached() string {
	return attachedLines(h.take())
}

func (h *AgentHandle) take() []string {
	if h.deps.Attachments == nil {
		return nil
	}
	return h.deps.Attachments.Take()
}

// giveBack returns taken attachments to waiting.
func (h *AgentHandle) giveBack(paths []string) {
	if h.deps.Attachments != nil && len(paths) > 0 {
		h.deps.Attachments.Restore(paths)
	}
}

func (h *AgentHandle) hasAttachments() bool {
	return h.deps.Attachments != nil && h.deps.Attachments.Len() > 0
}

// attachedLines is one "\nAttached: <path>" per path ("" for none).
func attachedLines(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString("\nAttached: " + p)
	}
	return b.String()
}
