package voice

// The voice log (#71): one timestamped line per conversation event (opened, phase changes,
// what the user and Pixie said, tool calls and results, hand-offs, notices, why it closed), so
// a conversation that went wrong can be read back afterwards. Never keys or audio.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// VoiceLogMaxBytes: past this the log moves to voice.log.1 (one old file is kept).
const VoiceLogMaxBytes = 1 << 20

// voiceLogClip bounds one value in a line (tool arguments and results can be long).
const voiceLogClip = 400

// VoiceLogPath is the log's file under the app's data folder ("" when that is unknown: no log).
func VoiceLogPath(appSupport string) string {
	if appSupport == "" {
		return ""
	}
	return filepath.Join(appSupport, "logs", "voice.log")
}

// VoiceLog appends lines to a file, rotating it at VoiceLogMaxBytes. A nil *VoiceLog logs
// nothing. Safe for concurrent use; a write that fails is dropped (the log never blocks the
// conversation).
type VoiceLog struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewVoiceLog logs to path ("" = nil: no log).
func NewVoiceLog(path string) *VoiceLog {
	if path == "" {
		return nil
	}
	return &VoiceLog{path: path, now: time.Now}
}

// Printf writes one line: the time, then the formatted text with newlines flattened.
func (l *VoiceLog) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	text := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
	line := l.now().Format("2006-01-02T15:04:05.000-07:00") + " " + text + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return
	}
	if fi, err := os.Stat(l.path); err == nil && fi.Size()+int64(len(line)) > VoiceLogMaxBytes {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
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
