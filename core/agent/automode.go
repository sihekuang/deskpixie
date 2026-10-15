package agent

// Why a Claude agent launched in auto mode runs in Manual (#97), most likely first. Claude Code
// starts in Manual without a word when auto mode isn't available
// (https://code.claude.com/docs/en/permission-modes).

import (
	"regexp"
	"strconv"
)

type ManualCause int

const (
	CauseOldModel ManualCause = iota
	CauseOldClaude
	CauseOrgOff
	CauseUnavailable
)

// Reason is the tray's wording after "… is in Manual mode: ".
func (c ManualCause) Reason() string {
	switch c {
	case CauseOldModel:
		return "its model is too old for auto mode"
	case CauseOldClaude:
		return "Claude Code is too old for auto mode"
	case CauseOrgOff:
		return "your organization turned auto mode off"
	}
	return "auto mode isn't available for your account right now"
}

func ManualCauseOf(model string, claudeListsAuto, managedOff bool) ManualCause {
	switch {
	case !ModelSupportsAuto(model):
		return CauseOldModel
	case !claudeListsAuto:
		return CauseOldClaude
	case managedOff:
		return CauseOrgOff
	}
	return CauseUnavailable
}

var (
	claudeModelID = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d+))?`)
	claudeOldID   = regexp.MustCompile(`^claude-\d`)
)

// ModelSupportsAuto: the documented minimum on the API (Opus/Sonnet 4.6+, Haiku 5.5, Fable).
// Aliases, empty (Claude Code's default) and ids it doesn't recognise count as supported, so a
// model it doesn't know is never blamed.
func ModelSupportsAuto(model string) bool {
	if claudeOldID.MatchString(model) {
		return false // claude-3-x style ids predate auto mode
	}
	m := claudeModelID.FindStringSubmatch(model)
	if m == nil {
		return true
	}
	major, _ := strconv.Atoi(m[2])
	minor := 0
	if m[3] != "" && len(m[3]) <= 2 { // a date suffix (8 digits) is not a minor version
		minor, _ = strconv.Atoi(m[3])
	}
	switch m[1] {
	case "fable":
		return true
	case "haiku":
		return major > 5 || major == 5 && minor >= 5
	}
	return major > 4 || major == 4 && minor >= 6
}
