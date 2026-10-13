package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Validation messages.
const (
	NotOnPlatform = "Not available on this platform"
	NotAChoice    = "Not one of the choices"
	NoFile        = "No file at that path"
	// BadProjectsRoot is shown for any bad projects folder: never the OS error or a path.
	BadProjectsRoot = "Choose an existing folder inside your home folder"
	BadHotkey       = "Not a key combination (e.g. ctrl+alt+Space)"
	BadSession      = "Use a-z, 0-9 and -, up to 48 characters, not starting with - and not \"default\""
	NoWorkerKind    = "Pick at least one worker kind to turn workers on"
)

// FieldError is one problem, shown under the field at Path.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Env is what the rules need besides the config: the home folder, the platform, and the
// runtime choices.
type Env struct {
	Home     string
	Platform Platform
	Sources  OptionSources
}

// RangeMessage is the message for a whole number outside min..=max.
func RangeMessage(min, max int64) string {
	return fmt.Sprintf("Use a whole number from %d to %d", min, max)
}

// Validate is every save-time rule c breaks; empty means it may be saved. Loading stays lenient.
func Validate(c Config, env Env) []FieldError {
	t := tree(c)
	var errs []FieldError
	add := func(path, msg string) { errs = append(errs, FieldError{path, msg}) }
	for _, f := range Fields {
		value, ok := lookup(t, f.Path)
		if !ok {
			continue
		}
		switch f.Kind.Type {
		case KindInt:
			n, isNum := value.(json.Number)
			x, err := n.Float64()
			if !isNum || err != nil || x != math.Trunc(x) || x < float64(f.Kind.Min) || x > float64(f.Kind.Max) {
				add(f.Path, RangeMessage(f.Kind.Min, f.Kind.Max))
			}
		case KindChoice:
			// A device UID may name a device that isn't connected now (a hand edit, or one
			// saved on another day): the audio unit falls back to the default for it.
			// An open choice's options are only suggestions.
			if f.Kind.Choices.Nullable() || f.Kind.Choices.Open {
				continue
			}
			s, _ := value.(string)
			if p := choiceProblem(f.Kind.Choices, s, env); p != "" {
				add(f.Path, p)
			}
		case KindMultiChoice:
			values, _ := value.([]any)
			for _, v := range values {
				s, _ := v.(string)
				if p := choiceProblem(f.Kind.Choices, s, env); p != "" {
					add(f.Path, p)
					break
				}
			}
		}
	}
	if CheckHotkey(c.Hotkey) != nil {
		add("hotkey", BadHotkey)
	}
	if _, err := ProjectsRoot(c.Agent.ProjectsRoot, env.Home); err != nil {
		add("agent.projectsRoot", BadProjectsRoot)
	}
	if CheckSession(c.Agent.Session) != nil {
		add("agent.session", BadSession)
	}
	if c.Voice.PersonaPath != nil && !isFile(*c.Voice.PersonaPath) {
		add("voice.personaPath", NoFile)
	}
	if c.Agent.HerdrPath != nil && !isFile(*c.Agent.HerdrPath) {
		add("agent.herdrPath", NoFile)
	}
	if w := c.Agent.Workers; w.Enabled {
		any := false
		for _, k := range w.Kinds {
			any = any || ParseAgentKind(k) != ""
		}
		if !any {
			add("agent.workers.kinds", NoWorkerKind)
		}
	}
	return errs
}

// choiceProblem is why value can't be chosen here, or "".
func choiceProblem(c Choices, value string, env Env) string {
	for _, o := range Options(c, env.Sources) {
		if o.Value != value {
			continue
		}
		for _, p := range o.Platforms {
			if p == env.Platform {
				return ""
			}
		}
		return NotOnPlatform
	}
	return NotAChoice
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ParseAgentKind is "claude" or "codex" for those names (any case, trimmed), else "".
// (Rust agent_core::AgentKind::parse; P7 owns the type.)
func ParseAgentKind(s string) string {
	switch k := strings.ToLower(strings.TrimSpace(s)); k {
	case "claude", "codex":
		return k
	}
	return ""
}

// CheckSession is the herdr session name rule: [a-z0-9][a-z0-9-]{0,47}, never "default".
// (Rust session_herdr::check_session; P12 owns the supervisor.)
func CheckSession(session string) error {
	ok := len(session) >= 1 && len(session) <= 48 && session != "default"
	for i, c := range session {
		lowerOrDigit := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !(lowerOrDigit || (i > 0 && c == '-')) {
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("herdr session must be a dedicated name (a-z, 0-9, -; not \"default\"), not %q", session)
	}
	return nil
}

// ProjectsRoot resolves the projects folder: ~ and ~/… expand to home, a relative path is
// below home; the result must be an existing folder strictly below home after resolving
// symlinks. (Rust agent::workers::projects_root.)
func ProjectsRoot(raw, home string) (string, error) {
	raw = strings.TrimSpace(raw)
	var joined string
	switch {
	case raw == "":
		return "", fmt.Errorf("empty")
	case raw == "~":
		joined = home
	case strings.HasPrefix(raw, "~/"):
		joined = filepath.Join(home, raw[2:])
	case strings.HasPrefix(raw, "~"):
		return "", fmt.Errorf("~user is not supported")
	case filepath.IsAbs(raw):
		joined = raw
	default:
		joined = filepath.Join(home, raw)
	}
	root, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if root, err = filepath.Abs(root); err != nil {
		return "", err
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", root)
	}
	h, err := filepath.EvalSymlinks(home)
	if err != nil {
		h = home
	}
	if h, err = filepath.Abs(h); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(h, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not a folder below your home folder", root)
	}
	return root, nil
}
