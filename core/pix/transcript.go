package pix

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/sihekuang/deskpixie/core/pix/describe"
)

// transcriptTail is how much of a transcript's end ResumedDescription reads.
const transcriptTail = 8 << 20

// ResumedDescription describes a resumed Claude conversation from its transcript (the
// SessionStart hook's transcript_path): the last prompt you typed in it, else the title
// Claude gave it, else "". The transcript format is undocumented, so anything unexpected
// just gives "".
func ResumedDescription(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > transcriptTail {
		f.Seek(-transcriptTail, io.SeekEnd) // the first, partial line is skipped as junk
	}
	var prompt, title string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		var e struct {
			Type    string `json:"type"`
			AITitle string `json:"aiTitle"`
			IsMeta  bool   `json:"isMeta"`
			Summary bool   `json:"isCompactSummary"`
			Hidden  bool   `json:"isVisibleInTranscriptOnly"`
			Origin  *struct {
				Kind string `json:"kind"`
			} `json:"origin"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) == nil {
			switch {
			case e.Type == "ai-title" && e.AITitle != "":
				title = e.AITitle
			case e.Type == "user" && !e.IsMeta && !e.Summary && !e.Hidden && (e.Origin == nil || e.Origin.Kind == "human"):
				// Typed by you: not a peer's message, a notice or a compaction summary. Older
				// Claude Code versions record no origin.
				if d := describe.FromPrompt(promptText(e.Message.Content)); d != "" {
					prompt = d
				}
			}
		}
		if err != nil {
			break
		}
	}
	if prompt != "" {
		return prompt
	}
	return describe.Clean(title)
}

// promptText is a user message's text: the string itself, or its text parts (tool results
// have none).
func promptText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			return p.Text
		}
	}
	return ""
}
