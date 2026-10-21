package pix

// pix's own log (#225): logs/pix.log in DeskPixie's data folder, next to the app's, kept to the
// same Log size setting and exported with the others. Every pix running appends to it, each
// line naming its process and tool. It never holds what you type or what's sent to the agent.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sihekuang/deskpixie/core/logfile"
)

// AppData is DeskPixie's data folder: DESKPIXIE_SUPPORT_DIR when set (an isolated app), else
// ~/Library/Application Support/com.sihekuang.deskpixie.
func AppData(home string, env func(string) string) string {
	if d := env("DESKPIXIE_SUPPORT_DIR"); d != "" {
		return d
	}
	return filepath.Join(home, "Library", "Application Support", "com.sihekuang.deskpixie")
}

// LogPath is pix's log in DeskPixie's data folder.
func LogPath(home string, env func(string) string) string {
	return LogFile(AppData(home, env))
}

// LogFile is pix's log under the data folder appData.
func LogFile(appData string) string { return filepath.Join(appData, "logs", "pix.log") }

// logMax is the Log size setting from settings.json, read once when pix starts; the default
// when it can't be read.
func logMax(appData string) int64 {
	var cfg struct {
		LogMaxMB uint32 `json:"logMaxMB"`
	}
	b, err := os.ReadFile(filepath.Join(appData, "settings.json"))
	if err != nil || json.Unmarshal(b, &cfg) != nil || cfg.LogMaxMB == 0 {
		return logfile.DefaultMax
	}
	return int64(cfg.LogMaxMB) << 20
}

// Log is one pix's log. A nil Log is off.
type Log struct {
	w      *logfile.Writer
	prefix string
}

func openLog(path string, max int64, tool string) *Log {
	return &Log{w: logfile.New(path, func() int64 { return max }),
		prefix: fmt.Sprintf("pix %d %s: ", os.Getpid(), tool)}
}

func (l *Log) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	line := time.Now().Format("2006/01/02 15:04:05 ") + l.prefix + fmt.Sprintf(format, args...)
	_, _ = l.w.Write([]byte(line + "\n"))
}
