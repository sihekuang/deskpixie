package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Spoken confirm words for agent permission requests (port of confirm.rs).
// Matching runs only on the user's transcripts, never on a model tool call.

// Tokens are the confirm words: short, distinct, unlikely in normal speech.
var Tokens = [12]string{"hawk", "lark", "wren", "dove", "crow", "kite", "rook", "teal", "swan", "ibis", "finch", "heron"}

// RequestID identifies one PermissionRequest hook call (assigned by the runtime).
type RequestID = uint64

var (
	refusals  = []string{"no", "dont", "deny", "stop", "cancel", "not", "never", "nope", "reject", "refuse"}
	questions = []string{"if", "what", "should", "can", "could", "would", "whether", "maybe", "why", "how", "when", "will", "might", "shall"}
	verbs     = []string{"confirm", "allow", "approve"}
	leading   = []string{"yes", "yeah", "yep", "ok", "okay", "sure", "go", "ahead", "alright", "right", "so", "um", "uh"}
	trailing  = []string{"please", "thanks", "thank", "you", "now", "ok", "okay", "go"}
)

// questionMarks: `?` in any script.
const questionMarks = "?？؟¿⁇‽﹖❓❔⁈⁉⸮"

// Pending is one permission request waiting for its confirm word.
type Pending struct {
	ID    RequestID
	Token string
	Agent string
	// Job is the job the request belongs to, when it can be told (the coordinator's single
	// in-flight job); "" when it can't.
	Job      JobID
	Deadline Millis
}

// Decided is one request settled by a transcript.
type Decided struct {
	Pending Pending
	Allow   bool
}

// Confirmations holds the pending requests and hands out confirm words.
type Confirmations struct {
	pending []Pending
	timeout Millis
	// next rotates through Tokens, so a just-used word is not handed out again at once.
	next int
}

func NewConfirmations(timeout Millis) *Confirmations {
	return &Confirmations{timeout: timeout}
}

// Request registers a request and returns its token, or false if all 12 words are in use.
func (c *Confirmations) Request(id RequestID, agent string, job JobID, now Millis) (string, bool) {
	for k := range Tokens {
		at := (c.next + k) % len(Tokens)
		t := Tokens[at]
		if c.IsPendingToken(t) {
			continue
		}
		c.next = (at + 1) % len(Tokens)
		deadline := now + c.timeout
		if deadline < now {
			deadline = ^Millis(0) // saturating_add
		}
		c.pending = append(c.pending, Pending{ID: id, Token: t, Agent: agent, Job: job, Deadline: deadline})
		return t, true
	}
	return "", false
}

// Pending returns the requests still waiting (do not modify).
func (c *Confirmations) Pending() []Pending { return c.pending }

func (c *Confirmations) HasPendingFor(agent string) bool {
	return slices.ContainsFunc(c.pending, func(p Pending) bool { return p.Agent == agent })
}

func (c *Confirmations) IsPendingToken(token string) bool {
	return slices.ContainsFunc(c.pending, func(p Pending) bool { return p.Token == token })
}

// OnTranscript decides pending requests from one user utterance.
// Deny rules run first: a refusal word plus a pending token denies that request.
// A confirm needs the utterance to be exactly `[fillers] (confirm|allow|approve) <token>
// [fillers]`, with no question word and no question mark (the verb is required, so a bare token
// never confirms). Over-denial is deliberate and fail-safe: "no problem, confirm hawk" denies, as
// any refusal word does. A decided request is removed (its token is used up).
func (c *Confirmations) OnTranscript(text string) []Decided {
	hasQuestionMark := strings.ContainsAny(text, questionMarks)
	lowered := strings.NewReplacer("’", "", "'", "").Replace(strings.ToLower(text))
	words := strings.FieldsFunc(lowered, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsNumber(r)) })
	var mentioned []string
	for _, t := range Tokens {
		if slices.Contains(words, t) && c.IsPendingToken(t) {
			mentioned = append(mentioned, t)
		}
	}
	if len(mentioned) == 0 {
		return nil
	}
	if slices.ContainsFunc(words, func(w string) bool { return slices.Contains(refusals, w) }) {
		var out []Decided
		for _, t := range mentioned {
			if p, ok := c.take(t); ok {
				out = append(out, Decided{p, false})
			}
		}
		return out
	}
	if hasQuestionMark || slices.ContainsFunc(words, func(w string) bool { return slices.Contains(questions, w) }) {
		return nil
	}
	rest := words
	for len(rest) > 0 && slices.Contains(leading, rest[0]) {
		rest = rest[1:]
	}
	// Requiring the verb reduces, but does not prevent, echo confirms (the spoken prompt itself
	// says "confirm <token>"): the caller must gate transcripts captured during or just after playback.
	if len(rest) == 0 || !slices.Contains(verbs, rest[0]) {
		return nil
	}
	rest = rest[1:]
	if len(rest) == 0 {
		return nil
	}
	token, tail := rest[0], rest[1:]
	for _, w := range tail {
		if !slices.Contains(trailing, w) {
			return nil
		}
	}
	if !slices.Contains(mentioned, token) {
		return nil
	}
	if p, ok := c.take(token); ok {
		return []Decided{{p, true}}
	}
	return nil
}

// Expire removes and returns requests past their deadline (deny them: "No answer from the user").
func (c *Confirmations) Expire(now Millis) []Pending {
	var expired, kept []Pending
	for _, p := range c.pending {
		if now >= p.Deadline {
			expired = append(expired, p)
		} else {
			kept = append(kept, p)
		}
	}
	c.pending = kept
	return expired
}

// Drain removes and returns everything still pending (app quit).
func (c *Confirmations) Drain() []Pending {
	out := c.pending
	c.pending = nil
	return out
}

// Remove takes one request out (e.g. its hook connection went away).
func (c *Confirmations) Remove(id RequestID) (Pending, bool) {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.ID == id })
	if at < 0 {
		return Pending{}, false
	}
	p := c.pending[at]
	c.pending = slices.Delete(c.pending, at, at+1)
	return p, true
}

func (c *Confirmations) take(token string) (Pending, bool) {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token })
	if at < 0 {
		return Pending{}, false
	}
	p := c.pending[at]
	c.pending = slices.Delete(c.pending, at, at+1)
	return p, true
}

// MaxSpokenCommand: a Bash command longer than this (in speakable form) cannot be approved by voice.
const MaxSpokenCommand = 80

// SpeakableCommand is the command as it should be read aloud, hiding nothing: invisible
// characters are dropped and shell metacharacters become words, so the user hears every part
// that will run.
func SpeakableCommand(cmd string) string {
	chars := []rune(cmd)
	var out strings.Builder
	for i := 0; i < len(chars); {
		c := chars[i]
		var next rune = -1
		if i+1 < len(chars) {
			next = chars[i+1]
		}
		text, used := "", 1
		switch {
		case c == '&' && next == '&':
			text, used = " and then ", 2
		case c == '|' && next == '|':
			text, used = " or else ", 2
		case c == '$' && next == '(':
			text, used = " subshell ", 2
		case c == '|':
			text = " pipe "
		case c == ';' || c == '\n' || c == '\t':
			text = " then "
		case c == '>':
			text = " into "
		case c == '<':
			text = " from "
		case c == '#':
			text = " hash "
		case c == '~':
			text = " home "
		case c == '`':
			text = " subshell "
		case c == '&':
			text = " in background "
		}
		if text != "" {
			out.WriteString(text)
		} else if !(isControl(c) || isFormatChar(c)) {
			out.WriteRune(c)
		}
		i += used
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

// stringField is Rust's `tool_input.get(k).and_then(Value::as_str).unwrap_or_default()`.
func stringField(input any, k string) string {
	if m, ok := input.(map[string]any); ok {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

// pathFileName is Rust's Path::file_name: the last normal component (trailing "/" and "."
// ignored); false for "", "/", "." or a path ending in "..".
func pathFileName(p string) (string, bool) {
	parts := strings.Split(p, "/")
	for len(parts) > 0 {
		last := parts[len(parts)-1]
		if last == "" || last == "." && len(parts) > 1 {
			parts = parts[:len(parts)-1]
			continue
		}
		break
	}
	if len(parts) == 0 {
		return "", false
	}
	last := parts[len(parts)-1]
	if last == "" || last == "." || last == ".." {
		return "", false
	}
	return last, true
}

func filePathOf(input any) string {
	if p := stringField(input, "file_path"); p != "" {
		return p
	}
	return stringField(input, "notebook_path")
}

// VoiceConfirmable reports whether a request can be approved by voice: false for a Bash
// command that is too long to read out (speakable form over MaxSpokenCommand) or that spans
// lines. Callers deny the rest with "Too long to approve by voice; run it yourself or shorten it."
func VoiceConfirmable(toolName string, toolInput any) bool {
	switch toolName {
	case "Bash":
		raw := stringField(toolInput, "command")
		return isSpeakableText(raw) && utf8.RuneCountInString(SpeakableCommand(raw)) <= MaxSpokenCommand
	case "Edit", "Write", "Read", "NotebookEdit":
		name, ok := pathFileName(filePathOf(toolInput))
		return ok && isSpeakableText(name)
	case CloseSessionTool:
		return isSpeakableText(stringField(toolInput, "name"))
	}
	return false
}

// CloseSessionTool is close_session as a permission request (#89): the app asks before a
// session you started is closed, whichever agent the coordinator is.
const CloseSessionTool = "mcp__pixie__close_session"

// isSpeakableText is an allowlist: only characters whose meaning survives being read aloud (or
// that SpeakableCommand speaks as words). Punctuation a voice may drop (`:` `+` `$` quotes ...) is out.
func isSpeakableText(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c < utf8.RuneSelf && (unicode.IsLetter(c) || unicode.IsDigit(c)) || strings.ContainsRune(" -_./,@%&|;><#~", c)) {
			return false
		}
	}
	return true
}

// Describe words a permission question: `Run <command ≤ 80 chars> in <agent>?` for Bash,
// `<Tool> the file <name> in <agent>?` for file tools, `Use <tool> in <agent>?` otherwise.
func Describe(toolName string, toolInput any, agent string) string {
	switch toolName {
	case "Bash":
		command := SpeakableCommand(stringField(toolInput, "command"))
		if utf8.RuneCountInString(command) > MaxSpokenCommand {
			// Logging only: callers deny these (see VoiceConfirmable).
			return fmt.Sprintf("Run a long command starting with %s… in %s?", string([]rune(command)[:60]), agent)
		}
		return fmt.Sprintf("Run %s in %s?", command, agent)
	case "Edit", "Write", "MultiEdit", "Read", "NotebookEdit":
		name, ok := pathFileName(filePathOf(toolInput))
		if !ok {
			name = "a file"
		}
		return fmt.Sprintf("%s the file %s in %s?", toolName, name, agent)
	}
	if toolName == CloseSessionTool {
		// Its description when that reads aloud (Pixie says descriptions, not names).
		what := stringField(toolInput, "description")
		if !isSpeakableText(what) {
			what = stringField(toolInput, "name")
		}
		return fmt.Sprintf("Close your session %s?", what)
	}
	return fmt.Sprintf("Use %s in %s?", Clean(toolName), agent)
}

// ConfirmQuestionLine is the spoken question: the description plus `Say confirm <token>.`
func ConfirmQuestionLine(description, token string) string {
	return fmt.Sprintf("%s Say confirm %s.", description, token)
}
