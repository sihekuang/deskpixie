package app

// The app's logs (#225): the core's own log in logs/deskpixie.log, pix's in logs/pix.log (the engine tees the standard
// logger there), kept like the voice log and the metrics log to the Log size setting, and
// Settings ▸ Advanced ▸ Export Logs… (`logs.export`).

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sihekuang/deskpixie/core/buildinfo"
	"github.com/sihekuang/deskpixie/core/logfile"
	"github.com/sihekuang/deskpixie/core/pix"
	"github.com/sihekuang/deskpixie/core/voice"
)

// AppLogPath is the core's log under the app's data folder.
func AppLogPath(appSupport string) string {
	return filepath.Join(appSupport, "logs", "deskpixie.log")
}

// LogMaxBytes is the Log size setting in bytes, read on every write.
func (a *App) LogMaxBytes() int64 {
	mb := a.store.Current().LogMaxMB
	if mb == 0 {
		return logfile.DefaultMax
	}
	return int64(mb) << 20
}

// logFiles are the logs an export holds.
func (a *App) logFiles() []string {
	files := []string{AppLogPath(a.paths.AppSupport), voice.VoiceLogPath(a.paths.AppSupport),
		pix.LogFile(a.paths.AppSupport)}
	if a.deps.MetricsLogPath != "" {
		files = append(files, a.deps.MetricsLogPath)
	}
	return files
}

func (a *App) registerLogs(r Registrar) {
	// The shell picks where (a save panel) and says what only it knows: the app and OS versions.
	r.Register("logs.export", func(raw json.RawMessage) (any, error) {
		var in struct {
			Path string `json:"path"`
			App  string `json:"app"`
			OS   string `json:"os"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Path == "" {
			return nil, errors.New("Choose where to save the logs.")
		}
		about := fmt.Sprintf("DeskPixie %s\nCore %s\n%s\nExported %s\nLog size: %d MB\n",
			in.App, buildinfo.String(), in.OS, time.Now().Format(time.RFC3339), a.LogMaxBytes()>>20)
		if err := logfile.Export(in.Path, a.logFiles(), about); err != nil {
			return nil, fmt.Errorf("Couldn't save the logs: %v", err)
		}
		return map[string]any{"path": in.Path}, nil
	})
}
