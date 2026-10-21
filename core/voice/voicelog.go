package voice

// The voice log (#71): one timestamped line per conversation event (opened, phase changes,
// what the user and Pixie said, tool calls and results, hand-offs, notices, why it closed), so
// a conversation that went wrong can be read back afterwards. Never keys or audio.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/logfile"
)

// voiceLogClip bounds one value in a line (tool arguments and results can be long).
const voiceLogClip = 400

// VoiceLogPath is the log's file under the app's data folder ("" when that is unknown: no log).
func VoiceLogPath(appSupport string) string {
	if appSupport == "" {
		return ""
	}
	return filepath.Join(appSupport, "logs", "voice.log")
}

// VoiceLog appends lines to a file kept to the Log size setting (logfile, #225). A nil
// *VoiceLog logs nothing. Safe for concurrent use; a write that fails is dropped (the log never
// blocks the conversation).
type VoiceLog struct {
	w   *logfile.Writer
	now func() time.Time
}

// NewVoiceLog logs to path ("" = nil: no log), keeping at most max() bytes on disk (nil =
// logfile.DefaultMax).
func NewVoiceLog(path string, max func() int64) *VoiceLog {
	if path == "" {
		return nil
	}
	if max == nil {
		max = func() int64 { return logfile.DefaultMax }
	}
	return &VoiceLog{w: logfile.New(path, max), now: time.Now}
}

// Printf writes one line: the time, then the formatted text with newlines flattened.
func (l *VoiceLog) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	text := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
	line := l.now().Format("2006-01-02T15:04:05.000-07:00") + " " + text + "\n"
	_, _ = l.w.Write([]byte(line))
}

// clipLog shortens s for a log line.
func clipLog(s string) string {
	if len(s) <= voiceLogClip {
		return s
	}
	cut := voiceLogClip
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
