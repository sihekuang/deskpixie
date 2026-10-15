package voice

// The claim check (#72): when Pixie's words in a turn claim an action ("I've handed that off",
// "I've set a timer", "I've paused the music") and the tool that does it wasn't called in that
// turn, the conversation corrects it. Like the hand-off (#71) these matchers are English only
// and deliberately conservative: only first-person statements count, never questions, offers,
// conditionals or negations, so a missed claim leaves today's behaviour.

import "strings"

// The tools the claim check covers besides delegate (core/tools' timer and media).
const (
	TimerToolName = "timer"
	MediaToolName = "media"
)

// ClaimTools are the tools whose claims are checked; the runtime keeps those it has.
var ClaimTools = []string{DelegateTool, TimerToolName, MediaToolName}

// Claim is an action Pixie said it did that no tool call backs.
type Claim struct {
	Tool string // the tool that would do it
	// Check: Pixie promised to check a fact ("Let me check that."): the delegated task gets the
	// "Check: " prefix the persona uses for facts.
	Check bool
}

// CorrectionPrefix leads the notice Pixie gets when a claim wasn't backed.
const CorrectionPrefix = "Coordinator: correction: "

// ClaimNotice is what Pixie is told about a claim it can't back: what didn't happen and what to
// do. A delegate claim gets none: the app delegates instead.
func ClaimNotice(c Claim) Notice {
	switch c.Tool {
	case TimerToolName:
		return Notice{Text: CorrectionPrefix + "no timer was set or changed. Call timer now, or ask the user how long."}
	case MediaToolName:
		return Notice{Text: CorrectionPrefix + "the music wasn't changed. Call media now, or tell the user it didn't happen."}
	}
	return Notice{}
}

// claimGroup: one of verbs, after a first-person subject, with one of objects in the next
// claimWindow words (objects nil: none needed; next, if set, limits the word right after it).
type claimGroup struct {
	verbs, objects, next []string
}

type claimSpec struct {
	tool   string
	backed []string // a call to any of these backs the claim; nil = any tool
	check  bool
	groups []claimGroup
	// phrases claim it when a sentence opens with one, after any lead-ins.
	phrases []string
}

const claimWindow = 5

var claimSpecs = []claimSpec{
	{tool: DelegateTool, backed: []string{DelegateTool, "recall", "answer_job", "jobs"}, groups: []claimGroup{
		{verbs: words("hand handed handing pass passed passing ask asked asking"),
			objects: words("coordinator agent agents claude brain")},
		{verbs: words("hand handed handing"), objects: words("off")},
		{verbs: words("start started starting kick kicked kicking open opened opening create created creating queue queued queuing"),
			objects: words("job")},
		{verbs: words("delegate delegated delegating")},
	}},
	// The acknowledgement Pixie says after delegating ("On it, I'll let you know when it's
	// done", #95). A timer or any other tool called in the turn backs it.
	{tool: DelegateTool, phrases: []string{
		"on it", "im on it", "im on that", "ill get on it", "ill get on that", "ill get right on it",
		"ill get right on that", "ill get back to you", "ill let you know when its done",
		"ill let you know once its done", "ill let you know when its ready", "ill let you know once its ready",
		"ill let you know when its finished", "ill let you know when thats done",
	}},
	{tool: DelegateTool, check: true, groups: []claimGroup{
		{verbs: words("check checking look looking find"), next: words("that it this now for real quickly into up out")},
	}},
	{tool: TimerToolName, backed: []string{TimerToolName, DelegateTool}, groups: []claimGroup{
		{verbs: words("set setting start started starting cancel cancelled canceled cancelling canceling stop stopped stopping add added adding create created put putting"),
			objects: words("timer timers countdown reminder")},
		{verbs: words("remind"), objects: words("you")},
	}},
	{tool: MediaToolName, backed: []string{MediaToolName, DelegateTool}, groups: []claimGroup{
		{verbs: words("pause paused pausing resume resumed resuming skip skipped skipping stop stopped stopping play playing " +
			"mute muted muting unmute unmuted turn turned turning lower lowered lowering raise raised raising"),
			objects: words("music song songs track tune tunes spotify playback volume playlist")},
	}},
}

// The "let me check" words never cover these: Pixie checking its own tools or the agent's jobs.
var checkNotAbout = words("timer timers music song coordinator agent agents job jobs weather time clock")

func words(s string) []string { return strings.Fields(s) }

func inSet(w string, set []string) bool {
	for _, s := range set {
		if s == w {
			return true
		}
	}
	return false
}

var (
	claimSubjects = words("i ive ill im lemme")
	// claimFillers may sit between the subject and the verb: "I've just gone ahead and set …".
	claimFillers = words("have has will am just now also quickly go gone went ahead and then gonna going to")
	// claimLeadIns may open the sentence before a bare "Handing that off now."
	claimLeadIns   = words("sure ok okay alright all right done got it great yes yeah yep no problem of course absolutely perfect cool so")
	claimNegations = words("not dont didnt doesnt havent hasnt hadnt cant cannot couldnt wont wouldnt wasnt isnt arent never unable shouldnt")
	// claimConditionals make the sentence an offer or a condition, not a done action.
	claimConditionals = words("if once when whenever unless want wanna like should shall could can would might maybe perhaps until")
	// claimEarlier: the sentence is about an earlier turn ("I already handed that off"), which
	// this turn's tool calls can't back.
	claimEarlier = words("already earlier before yesterday ago previously last")
	// claimTails are fine endings that contain a conditional word ("I'll let you know when it's done").
	claimTails = []string{
		"let you know when", "let you know once", "let you know if", "tell you when", "ping you when",
		"when its done", "when it is done", "when its ready", "when it finishes", "when theyre done",
		"once its done", "once its ready", "once it finishes", "when it goes off", "when its up",
		"when times up", "when the time is up", "if anything", "when i hear back", "once i hear back",
	}
)

// FindClaim returns the first action Pixie's words claim that no call in called backs, among the
// covered tools in available.
func FindClaim(said string, called map[string]bool, available []string) (Claim, bool) {
	anyCall := len(called) > 0
	for _, sentence := range claimSentences(said) {
		toks := strings.Fields(sentence)
		for _, spec := range claimSpecs {
			if !inSet(spec.tool, available) || backed(spec, called, anyCall) {
				continue
			}
			if spec.matches(toks) {
				return Claim{Tool: spec.tool, Check: spec.check}, true
			}
		}
	}
	return Claim{}, false
}

func backed(s claimSpec, called map[string]bool, anyCall bool) bool {
	if s.backed == nil {
		return anyCall
	}
	for _, t := range s.backed {
		if called[t] {
			return true
		}
	}
	return false
}

// claimSentences are the statements in said, normalized: questions and sentences with a
// negation or a condition are left out.
func claimSentences(said string) []string {
	var out []string
	start := 0
	flush := func(end int, question bool) {
		s := normalize(said[start:end])
		start = end + 1
		if s == "" || question {
			return
		}
		probe := " " + s + " "
		for _, tail := range claimTails {
			probe = strings.ReplaceAll(probe, " "+tail+" ", " ")
		}
		for _, w := range strings.Fields(probe) {
			if inSet(w, claimNegations) || inSet(w, claimConditionals) || inSet(w, claimEarlier) {
				return
			}
		}
		if containsPhrase(s, "how long") {
			return
		}
		out = append(out, s)
	}
	for i, r := range said {
		switch r {
		case '.', '!', ';':
			flush(i, false)
		case '?':
			flush(i, true)
		}
	}
	flush(len(said), false)
	return out
}

func (s claimSpec) matches(toks []string) bool {
	if len(s.phrases) > 0 {
		k := 0
		for k < len(toks) && inSet(toks[k], claimLeadIns) {
			k++
		}
		return hasPrefixPhrase(strings.Join(toks[k:], " "), s.phrases)
	}
	for i := range toks {
		if !verbPosition(toks, i) {
			continue
		}
		for _, g := range s.groups {
			if inSet(toks[i], g.verbs) && g.follows(toks[i+1:], s.check) {
				return true
			}
		}
	}
	return false
}

func (g claimGroup) follows(rest []string, check bool) bool {
	if g.next != nil {
		if len(rest) > 0 && !inSet(rest[0], g.next) {
			return false
		}
		if check {
			for _, w := range rest {
				if inSet(w, checkNotAbout) {
					return false
				}
			}
		}
	}
	if g.objects == nil {
		return true
	}
	for j := 0; j < len(rest) && j < claimWindow; j++ {
		if inSet(rest[j], g.objects) {
			return true
		}
	}
	return false
}

// verbPosition: toks[i] is where a first-person action's verb stands: after "I", "I've",
// "I'll", "I'm" or "let me" (fillers allowed), after "and"/"then" in a sentence that has such a
// subject, or opening the sentence as an -ing or -ed form ("Handing that off now.").
func verbPosition(toks []string, i int) bool {
	j := i - 1
	for j >= 0 && inSet(toks[j], claimFillers) {
		j--
	}
	if j >= 0 && (inSet(toks[j], claimSubjects) || (toks[j] == "me" && j > 0 && toks[j-1] == "let")) {
		return true
	}
	if i > 0 && (toks[i-1] == "and" || toks[i-1] == "then") {
		for _, w := range toks[:i-1] {
			if inSet(w, claimSubjects) {
				return true
			}
		}
	}
	k := 0
	for k < i && inSet(toks[k], claimLeadIns) {
		k++
	}
	if k == i {
		w := toks[i]
		return strings.HasSuffix(w, "ing") || strings.HasSuffix(w, "ed")
	}
	return false
}
