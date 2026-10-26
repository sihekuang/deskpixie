package agent

import (
	"fmt"
	"regexp"
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
	// Session and Question (#105): a prompt of one of your pix sessions (Agent is "" then);
	// Label names the session. Options: its choices, picked with Choose; a menu's (Menu) never
	// by a confirm word or Allow, a permission dialog's still (meaning its first, Yes).
	// NoVoice: only a tap answers it (you couldn't hear what it does).
	Session  string
	Question string
	Label    string
	Options  []string
	Menu     bool
	NoVoice  bool
	// Details and Typed: a menu's option descriptions, and that it takes a typed answer (#268).
	Details []string
	Typed   bool
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

// RequestSession registers a prompt of session's (#105): it waits until answered or gone (no
// deadline: it is still in the terminal). id is a request id the caller keeps unique.
func (c *Confirmations) RequestSession(p Pending) (string, bool) {
	token, ok := c.Request(p.ID, "", "", 0)
	if !ok {
		return "", false
	}
	p.Token, p.Deadline = token, ^Millis(0)
	c.pending[len(c.pending)-1] = p
	return token, true
}

// TakeSession removes session's prompt with this id ("" any of its prompts); true if there was one.
func (c *Confirmations) TakeSession(session, id string) bool {
	before := len(c.pending)
	c.pending = slices.DeleteFunc(c.pending, func(p Pending) bool {
		return p.Session == session && session != "" && (id == "" || p.Question == id)
	})
	return len(c.pending) != before
}

// Choose takes the menu with this token if option (from 1) is one of its choices.
func (c *Confirmations) Choose(token string, option int) (Pending, bool) {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token })
	if at < 0 || option < 1 || option > len(c.pending[at].Options) {
		return Pending{}, false
	}
	return c.take(token)
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
		if slices.Contains(words, t) && c.voiceToken(t) {
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

// Detach makes request id belong to no job (#249): it outlives the job that asked it. The job
// it belonged to ("" = none, or no such request).
func (c *Confirmations) Detach(id RequestID) JobID {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.ID == id })
	if at < 0 {
		return ""
	}
	job := c.pending[at].Job
	c.pending[at].Job = ""
	return job
}

// DecideToken settles the request with token from a tap on Pixie's bubble (#97): the same
// outcome as the confirm word (allow) or a refusal (deny). Unknown or used tokens do nothing.
func (c *Confirmations) DecideToken(token string, allow bool) (Decided, bool) {
	if at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token }); at >= 0 && c.pending[at].Menu {
		return Decided{}, false // a menu is picked from, not allowed
	}
	p, ok := c.take(token)
	if !ok {
		return Decided{}, false
	}
	return Decided{p, allow}, true
}

// voiceToken: token is pending and a confirm word may answer it (not a menu, not tap-only).
func (c *Confirmations) voiceToken(token string) bool {
	return slices.ContainsFunc(c.pending, func(p Pending) bool { return p.Token == token && !p.Menu && !p.NoVoice })
}

// OptionsOf is the menu choices of the prompt with this token (nil: not a menu).
// TakesTyped: the request with token is a session menu that takes a typed answer (#268).
func (c *Confirmations) TakesTyped(token string) bool {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token })
	return at >= 0 && c.pending[at].Typed
}

// PendingOf is the request still waiting with token.
func (c *Confirmations) PendingOf(token string) (Pending, bool) {
	at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token })
	if at < 0 {
		return Pending{}, false
	}
	return c.pending[at], true
}

func (c *Confirmations) OptionsOf(token string) []string {
	if at := slices.IndexFunc(c.pending, func(p Pending) bool { return p.Token == token }); at >= 0 {
		return c.pending[at].Options
	}
	return nil
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
	return VoiceConfirmableWith(toolName, toolInput, SpokenPaths{})
}

// SpokenPaths are the folders a spoken command names short (#97): the projects root as
// "projects/" and the home folder as "home/". Empty fields shorten nothing.
type SpokenPaths struct{ ProjectsRoot, Home string }

// ShortenPaths replaces "<root>/" with "projects/" and "<home>/" with "home/" where the path
// starts a word (start of the command or after a space, quote or '='), root first. Only what is
// read and measured changes; the command that runs is untouched.
func ShortenPaths(cmd string, p SpokenPaths) string {
	for _, a := range [][2]string{{p.ProjectsRoot, "projects/"}, {p.Home, "home/"}} {
		root := strings.TrimRight(a[0], "/")
		if root == "" {
			continue
		}
		prefix := root + "/"
		var b strings.Builder
		for i := 0; i < len(cmd); {
			starts := i == 0 || strings.ContainsRune(" \t'\"=", rune(cmd[i-1]))
			if starts && strings.HasPrefix(cmd[i:], prefix) {
				b.WriteString(a[1])
				i += len(prefix)
				continue
			}
			b.WriteByte(cmd[i])
			i++
		}
		cmd = b.String()
	}
	return cmd
}

// DescribeWith is Describe with paths read short.
func DescribeWith(toolName string, toolInput any, agent string, p SpokenPaths) string {
	if toolName == "Bash" {
		toolInput = map[string]any{"command": ShortenPaths(stringField(toolInput, "command"), p)}
	}
	return Describe(toolName, toolInput, agent)
}

// VoiceConfirmableWith is VoiceConfirmable with paths read short.
func VoiceConfirmableWith(toolName string, toolInput any, p SpokenPaths) bool {
	switch toolName {
	case "Bash":
		raw := ShortenPaths(stringField(toolInput, "command"), p)
		return isSpeakableText(raw) && utf8.RuneCountInString(SpeakableCommand(raw)) <= MaxSpokenCommand
	case "Edit", "Write", "Read", "NotebookEdit":
		name, ok := pathFileName(filePathOf(toolInput))
		return ok && isSpeakableText(name)
	case CloseSessionTool, MessageSessionTool:
		return isSpeakableText(stringField(toolInput, "name"))
	}
	return false
}

// CloseSessionTool is close_session as a permission request (#89): the app asks before a
// session you started is closed, whichever agent the coordinator is.
const CloseSessionTool = "mcp__pixie__close_session"

// MessageSessionTool is message_session as a permission request (#141): asked about only when
// the message has the session do something hard to undo (GatedActions).
const MessageSessionTool = "mcp__pixie__message_session"

// gatedForms are the hard-to-undo steps a message can hand a session, by each form to its base
// verb: the ones the coordinator's own settings ask about (git push, publish, deploy), plus merging.
var gatedForms = map[string]string{
	"push": "push", "pushes": "push", "pushing": "push", "pushed": "push",
	"merge": "merge", "merges": "merge", "merging": "merge",
	"publish": "publish", "publishes": "publish", "publishing": "publish",
	"deploy": "deploy", "deploys": "deploy", "deploying": "deploy",
}

// pushAside are the directions that make a described push move something rather than send
// code (#255): "pushes the older one up".
var pushAside = map[string]bool{"up": true, "down": true, "aside": true, "away": true, "back": true, "over": true, "around": true, "forward": true, "along": true}

// pushNouns make "push" a kind of thing, not a step: "push notifications", "push to talk".
var pushNouns = map[string]bool{"notification": true, "notifications": true, "button": true, "buttons": true}

// codeWords make a push in their clause one of code.
var codeWords = map[string]bool{"git": true, "branch": true, "branches": true, "remote": true, "origin": true, "upstream": true,
	"github": true, "pr": true, "commit": true, "commits": true, "changes": true, "change": true, "code": true, "main": true,
	"repo": true, "tag": true, "tags": true, "fix": true, "fixes": true}

// pushObjects after a push are what gets sent: "pushes it up" is still git's idiom.
var pushObjects = map[string]bool{"it": true, "them": true, "this": true, "that": true, "these": true, "those": true,
	"everything": true, "all": true}

// pushIsntCode: the push at words[i] names a kind of thing, or describes something moving on
// screen ("it pushes up", "pushing the others down") with no code in its clause (#255). An
// instruction ("push your work up", "push it back") always asks: asking once too often is the
// safe side.
func pushIsntCode(words []string, i int) bool {
	rest := words[i+1:]
	if len(rest) > 0 && pushNouns[rest[0]] || len(rest) > 1 && rest[0] == "to" && rest[1] == "talk" {
		return true
	}
	if w := words[i]; w != "pushes" && w != "pushing" || len(rest) > 0 && pushObjects[rest[0]] {
		return false
	}
	for _, w := range words {
		if codeWords[w] {
			return false
		}
	}
	for _, w := range rest[:min(len(rest), 4)] {
		if pushAside[w] {
			return true
		}
	}
	return false
}

// negators turn the rest of their clause into what not to do ("don't push", "without merging");
// so do "instead of" and any "n't" word.
var negators = map[string]bool{"not": true, "no": true, "never": true, "dont": true, "cannot": true, "without": true, "nor": true, "avoid": true}

// affirmers after a negator undo it: "don't forget to push" asks for the push.
var affirmers = map[string]bool{"forget": true, "hesitate": true, "fail": true, "neglect": true}

// firstPerson words are the sender as the subject. Only a step already done ("I pushed", "we've
// pushed") or under way ("I'm merging") is the sender's own report (#246); a plan, an opinion
// or an approval ("we should merge", "I'd push", "I say push it") still asks.
var firstPerson = map[string]bool{"i": true, "we": true, "i've": true, "we've": true, "i'm": true, "we're": true}

// underWay are the first-person words an -ing form after them is the sender's: "I'm merging".
var underWay = map[string]bool{"i'm": true, "we're": true}

// fillers may sit between the sender and their step: "we just pushed".
var fillers = map[string]bool{"just": true, "already": true, "also": true, "now": true}

// youWords make a clause about the session: "I want you to merge" still asks.
var youWords = map[string]bool{"you": true, "you're": true, "you've": true, "you'll": true, "you'd": true}

// leadIns start a step named by its -ing form: "before merging I merged main" is the sender's.
var leadIns = map[string]bool{"before": true, "after": true, "while": true}

// negationEnds start a new instruction inside a clause: "don't wait, just push".
var negationEnds = map[string]bool{"but": true, "then": true, "just": true, "instead": true}

// stateQuestions lead a question about what happened ("has it been pushed?", "did you
// deploy?"), not a request; "can you push?" still asks for the push.
var stateQuestions = map[string]bool{"has": true, "have": true, "had": true, "is": true, "are": true, "was": true, "were": true, "did": true, "does": true}

// whWords lead a question about where things stand when a state word follows ("where are we on
// the merge?", "how did the deploy go?", "what's left?"); "how about you merge it?" and "when can
// you push?" still ask for the step (#165).
var whWords = map[string]bool{"where": true, "what": true, "how": true, "when": true, "which": true, "who": true}

// asksWhereThingsStand: words start a question about what happened, not a request.
func asksWhereThingsStand(words []string) bool {
	if len(words) == 0 {
		return false
	}
	if stateQuestions[words[0]] {
		return true
	}
	if wh, ok := strings.CutSuffix(words[0], "'s"); ok && whWords[wh] {
		return true
	}
	return whWords[words[0]] && len(words) > 1 && stateQuestions[words[1]]
}

// reviewVerbs, reviewNouns and goAheadObjects name the sender's own go-ahead, only in the
// fixed phrases waitsOnSender reads: a sender or a review elsewhere in the sentence ("I've
// reviewed it, rebase before you push") isn't what the step waits for.
var reviewVerbs = map[string]bool{"review": true, "approve": true, "check": true, "look": true, "see": true, "sign": true}

// reviewedVerbs are a review done, only after "I've": "after I approved it" reports one ("nothing
// changed after I approved it"), so it waits for nothing.
var reviewedVerbs = map[string]bool{"reviewed": true, "approved": true, "checked": true, "looked": true, "seen": true, "signed": true}

var reviewNouns = map[string]bool{"ok": true, "okay": true, "review": true, "approval": true, "ahead": true, "off": true}

// goAheadObjects may follow the review before "before": "have me review the changes first".
var goAheadObjects = map[string]bool{"it": true, "that": true, "this": true, "them": true, "these": true, "those": true,
	"the": true, "changes": true, "change": true, "diff": true, "work": true, "everything": true, "first": true, "at": true, "over": true}

// waitsFor words lead the sender's go-ahead after the step: "push once I've reviewed it". Not
// "until", which says how long to keep at it ("keep deploying until I say so").
var waitsFor = map[string]bool{"after": true, "once": true, "when": true}

// waitsOnSender: the step at words[i] waits for the sender's own go-ahead, so they're asked
// again when they give it (#329). Before the step: "have me review it", "let me see", "ask
// me", "check with me", "wait for my OK", "I'll review it". After it: "once I've reviewed",
// "after I approve", "after my approval". A step before the review ("push it, then let me
// review"), one the wait is further on from ("push and merge once I've approved"), one kept at
// ("keep pushing"), and one in a condition ("if you're unsure, ask me before you push") still
// ask.
func waitsOnSender(words []string, i int) bool {
	// A condition anywhere in the clause ("ask me before you push unless it's a typo fix")
	// lets the step go ahead unasked when it doesn't hold.
	clause := " " + strings.Join(words, " ") + " "
	conditional := strings.Contains(clause, " if ") || strings.Contains(clause, " unless ") ||
		strings.Contains(clause, " in case ") || strings.Contains(clause, " in doubt ")
	if conditional || i > 0 && words[i-1] == "keep" {
		return false
	}
	j := i - 1
	if j >= 0 && (youWords[words[j]] || words[j] == "we") {
		j--
	}
	// The step ends the clause, or a "then" follows it: "ask me before you push to main, other
	// branches are fine" only waits for part of it.
	rest := words[i+1:]
	if at := slices.Index(rest, "then"); at >= 0 {
		rest = rest[:at]
	}
	ends := len(rest) <= 2 && !slices.ContainsFunc(rest, func(w string) bool { return !goAheadObjects[w] })
	if j >= 0 && words[j] == "before" && ends {
		t := words[:j]
		for n := 0; n < 4 && len(t) > 0 && goAheadObjects[t[len(t)-1]]; n++ {
			t = t[:len(t)-1]
		}
		ends := func(ok ...func(string) bool) bool {
			if len(t) < len(ok) {
				return false
			}
			for k, f := range ok {
				if !f(t[len(t)-len(ok)+k]) {
					return false
				}
			}
			return true
		}
		is := func(set ...string) func(string) bool {
			return func(w string) bool { return slices.Contains(set, w) }
		}
		me, verb, noun := is("me", "us"), func(w string) bool { return reviewVerbs[w] }, func(w string) bool { return reviewNouns[w] }
		if ends(is("have", "let"), me, verb) || ends(is("ask"), me) || ends(is("check"), is("with"), me) ||
			ends(is("wait"), is("for"), is("my", "our"), noun) || ends(is("wait"), is("for"), is("my", "our"), is("go", "sign"), noun) ||
			ends(is("i'll", "we'll"), verb) {
			return true
		}
	}
	// The wait comes right after the step and what it sends: "push it once", "merge the
	// changes after"; "push it, it works when I check" waits for nothing.
	k := i + 1
	for n := 0; n < 2 && k < len(words) && goAheadObjects[words[k]]; n++ {
		k++
	}
	if k < len(words) && waitsFor[words[k]] {
		r := slices.Concat(words[k+1:min(len(words), k+5)], []string{"", "", ""})
		if r[0] == "i" && r[1] == "have" {
			r = append([]string{"i've"}, r[2:]...)
		}
		// The phrase ends there: "my review" is a go-ahead, "my review comments" a thing.
		ends := func(w string) bool { return w == "" || w == "and" || negationEnds[w] }
		switch {
		case r[0] == "i":
			return reviewVerbs[r[1]] && r[2] != "in" || r[1] == "say" && (r[2] == "so" || r[2] == "go")
		case r[0] == "i've":
			return reviewedVerbs[r[1]] && r[2] != "in"
		case r[0] == "my":
			return reviewNouns[r[1]] && ends(r[2]) || (r[1] == "go" || r[1] == "sign") && reviewNouns[r[2]] && ends(r[3])
		}
		return false
	}
	return false
}

// GatedActions are the hard-to-undo steps text asks for, by their base verb, in the order first
// named ("push", "merge"): none for an ordinary message (#152), including one that only forbids
// a step ("don't push anything") or asks whether it happened ("has anything been pushed?").
func GatedActions(text string) []string {
	var out []string
	for _, c := range clauses(strings.ToLower(strings.ReplaceAll(text, "’", "'"))) {
		words := strings.FieldsFunc(c.text, func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' })
		if c.question && asksWhereThingsStand(words) {
			continue
		}
		mine, yours := false, false
		for _, w := range words {
			mine, yours = mine || firstPerson[w] || underWay[w], yours || youWords[w]
		}
		negated := false
		for i, w := range words {
			prev, prev2, next := "", "", ""
			if i > 0 {
				prev = words[i-1]
			}
			if i > 1 {
				prev2 = words[i-2]
			}
			if i+1 < len(words) {
				next = words[i+1]
			}
			switch {
			case (negators[w] || strings.HasSuffix(w, "n't")) && prev != "if" && prev != "or":
				// "if not, push" and "whether or not" don't forbid anything.
				negated = !affirmers[next]
			case w == "instead" && next == "of":
				negated = true
			case negationEnds[w]:
				negated = false
			}
			// The sender's own step (#246): "I pushed", "we just pushed", "I'm merging",
			// "before merging I …".
			subject := prev
			if fillers[prev] {
				subject = prev2
			}
			reported := strings.HasSuffix(w, "ed") && firstPerson[subject] ||
				strings.HasSuffix(w, "ing") && underWay[subject] ||
				leadIns[prev] && strings.HasSuffix(w, "ing") && mine && !yours
			if base, ok := gatedForms[w]; ok && !negated && !reported && !slices.Contains(out, base) &&
				!(base == "push" && pushIsntCode(words, i)) && !waitsOnSender(words, i) {
				out = append(out, base)
			}
		}
	}
	return out
}

// clause is a stretch of text up to a full stop, semicolon, colon, ! or ?: how far a "don't"
// reaches, and whether it was asked.
type clause struct {
	text     string
	question bool
}

func clauses(text string) []clause {
	var out []clause
	start := 0
	for i, r := range text {
		if strings.ContainsRune(".;:!?", r) {
			out = append(out, clause{text[start:i], r == '?'})
			start = i + 1
		}
	}
	return append(out, clause{text: text[start:]})
}

// spokenList joins words as said aloud: "a", "a and b", "a, b and c".
func spokenList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

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
		what := spokenDescription(toolInput)
		if !isSpeakableText(what) {
			what = stringField(toolInput, "name")
		}
		return fmt.Sprintf("Close your session %s?", what)
	}
	if toolName == MessageSessionTool {
		what := spokenDescription(toolInput)
		if !isSpeakableText(what) {
			what = stringField(toolInput, "name")
		}
		actions, _ := toolInput.(map[string]any)["actions"].([]string)
		return fmt.Sprintf("Message your session %s, asking it to %s?", what, spokenList(actions))
	}
	return fmt.Sprintf("Use %s in %s?", Clean(toolName), agent)
}

// spokenDescription is a session's description as said aloud: a pix description's "project:
// task" colon reads as a comma (#165).
func spokenDescription(toolInput any) string {
	return strings.ReplaceAll(stringField(toolInput, "description"), ":", ",")
}

// ConfirmQuestionLine is the spoken question: the description plus `Say confirm <token>.`
func ConfirmQuestionLine(description, token string) string {
	return fmt.Sprintf("%s Say confirm %s.", description, token)
}

// inventedPrompt is a confirm phrase ("say confirm hawk", "approve lark"): a confirm verb and
// one of Tokens, optionally led by "say".
var inventedPrompt = regexp.MustCompile(`(?i)\b(say\s+)?(` + strings.Join(verbs, "|") + `)\s+(` + strings.Join(Tokens[:], "|") + `)\b`)

// defuseInventedPrompts (#141): agent text that copies the app's confirm prompt for a word no
// request is waiting on would have the user say a confirm word nothing listens for. "Say
// confirm hawk" becomes "Say yes", a plain question the user's answer reaches the job through;
// "approve hawk" keeps only its verb. A pending word is left for maskPendingTokens to remove.
func defuseInventedPrompts(text string, pending []string) string {
	return inventedPrompt.ReplaceAllStringFunc(text, func(m string) string {
		g := inventedPrompt.FindStringSubmatch(m)
		if slices.Contains(pending, strings.ToLower(g[3])) {
			return m
		}
		if g[1] != "" {
			return g[1][:len("say")] + " yes"
		}
		return g[2]
	})
}
