package tools

import (
	"os"
	"strings"
)

// systemZoneName finds the IANA name of the system zone: $TZ, else the /etc/localtime symlink
// target (".../zoneinfo/Europe/London"). Empty means unknown (callers fall back to UTC).
func systemZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return ""
}
