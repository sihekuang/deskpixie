package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Prompts your pix sessions wait on (#105): shown in Pixie too, with a confirm word (or Allow
// and Deny, or a menu's options) in the bubble; your answer is typed into the session by pix,
// which first checks the prompt is still on screen. Only you answer: a click, or a confirm word
// for a permission you can hear in full; never the coordinator.

// sessionRequestBase starts the request ids of session prompts, apart from hook request ids.
const sessionRequestBase RequestID = 1 << 62

// onSessionQuestion asks you session's prompt q; a prompt it asked before is replaced. Asked
// again while it waits with its buttons (pix's hook let go of it, #347), it changes in place:
// it is not told twice.
func (c *AgentController) onSessionQuestion(session string, q SessionQuestion, now Millis, fx *[]Effect) {
	if c.shutDown {
		return
	}
	if len(q.Options) > 0 && c.confirms.RefreshSession(session, q.ID, askedAs(q), q.Typed) {
		return
	}
	c.confirms.TakeSession(session, "")
	delete(c.announced, session)
	tokens := c.pendingTokens()
	label := maskPendingTokens(q.Label, tokens)
	line := Line{Kind: LineQuestion, App: q.App, Session: session, Title: label}
	pending := Pending{Session: session, Question: q.ID, Label: label, Asked: askedAs(q)}
	var input any
	if q.Input != "" {
		json.Unmarshal([]byte(q.Input), &input)
	}
	// Everything the session supplies is masked: its options and command too.
	mask := func(s string) string { return maskPendingTokens(s, tokens) }
	options := make([]string, len(q.Options))
	for i, o := range q.Options {
		options[i] = mask(o)
	}
	q.Options = options
	what := func() string { return mask(DescribeWith(q.Tool, input, label, c.settings.SpokenPaths)) }
	// describe is the question as said, and its brief for the bubble (#197): without its label
	// (the title) and without how to answer it, since the bubble's buttons are right there.
	describe := func(token string) (string, string) {
		switch {
		case q.Kind == "menu" && len(q.Options) > 0:
			return fmt.Sprintf("%s is asking: %s Choose %s.", label, mask(q.Text), orList(q.Options)),
				upperFirst(mask(q.Text))
		case q.Kind == "menu":
			return fmt.Sprintf("%s is asking: %s Answer it in its terminal.", label, mask(q.Text)),
				upperFirst(mask(q.Text)) + " Answer it in its terminal."
		case q.Kind == "notice" && q.Tool != "":
			return what() + " Answer it in its terminal.", ""
		case q.Kind == "notice":
			return fmt.Sprintf("%s %s. Answer it in its terminal.", label, mask(q.Text)),
				upperFirst(mask(q.Text)) + ". Answer it in its terminal."
		case len(q.Options) > 0 && q.Prompt != "":
			// Its dialog as shown: Claude's summary of a command, Codex's reason, else its question.
			body := mask(q.Prompt)
			switch {
			case q.Tool == "Bash" && q.Text != "":
				body = fmt.Sprintf("%s. %s", strings.TrimSuffix(mask(q.Text), "."), mask(q.Prompt))
			case q.Tool == "" && q.Text != "": // Codex: its reason asks it in full
				body = mask(q.Text)
			}
			said := label + ": " + body
			if q.Tool != "" && VoiceConfirmableWith(q.Tool, input, c.settings.SpokenPaths) {
				return fmt.Sprintf("%s Say confirm %s, or choose in the bubble.", said, token), upperFirst(body)
			}
			return said + " Choose in the bubble.", upperFirst(body)
		case q.Tool != "" && VoiceConfirmableWith(q.Tool, input, c.settings.SpokenPaths):
			return ConfirmQuestionLine(what(), token), what()
		case q.Tool != "":
			return what() + " Allow or deny it in the bubble.", what()
		}
		return fmt.Sprintf("%s %s. Allow or deny it in the bubble.", label, mask(q.Text)), upperFirst(mask(q.Text)) + "."
	}
	// Announced only: a menu pix can't answer, or a dialog it mustn't (a notice).
	if q.Kind == "notice" || (q.Kind == "menu" && len(q.Options) == 0) {
		line.Text, line.Brief = describe("")
		c.deliver(line, now, fx)
		if id, ok := c.asking[session]; ok {
			c.announced[session] = announcedQuestion{question: q.ID, line: id}
		}
		return
	}
	c.sessionSeq++
	pending.ID = sessionRequestBase + RequestID(c.sessionSeq)
	pending.Options = q.Options
	if q.Kind == "menu" {
		pending.Menu = true
		pending.Typed, pending.Multi = q.Typed, q.Multi
		for _, d := range q.Details {
			pending.Details = append(pending.Details, mask(d))
		}
	} else {
		pending.NoVoice = q.Tool == "" || !VoiceConfirmableWith(q.Tool, input, c.settings.SpokenPaths)
	}
	token, ok := c.confirms.RequestSession(pending)
	if !ok {
		line.Text = fmt.Sprintf("%s is waiting on you in its terminal.", label)
		line.Brief = "Waiting on you in its terminal."
		c.deliver(line, now, fx)
		return
	}
	line.Token = &token
	line.Text, line.Brief = describe(token)
	if q.Kind == "permission" && len(q.Options) > 0 {
		line.Note = mask(q.Body)
	}
	if pending.Menu {
		c.askedBy(asker{menu: pending.ID, at: now})
		c.reask = reask{at: now, line: line}
	}
	before := c.outbox.Len()
	c.deliverOpening(line, now, fx)
	// Said in a conversation, it never reached the outbox: keep it there as heard, so the bubble
	// lists it with its buttons while it waits (as permissions do, #97).
	if !c.outbox.HasTokenSince(before, token) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: c.outbox.PushHeard(line, now)})
	}
}

// askedAs is what tells prompt q from another the session asks under the same id: who asks,
// what, and its options, as the session sent them (what answers it, Typed, may change).
func askedAs(q SessionQuestion) string {
	return strings.Join(append([]string{q.Kind, q.Label, q.Tool, q.Input, q.Text, q.Prompt}, q.Options...), "\x00")
}

// announcedQuestion is a session's prompt asked without buttons: pix's id for it, and the
// outbox line that asks it.
type announcedQuestion struct {
	question string
	line     uint64
}

// dropAnnounced: pix says session's prompt id is gone (answered in its terminal, or the
// session moved on): its asking line goes with it, if it is still the session's open question.
func (c *AgentController) dropAnnounced(session, id string, fx *[]Effect) {
	a, ok := c.announced[session]
	if !ok || a.question != id {
		return
	}
	delete(c.announced, session)
	if c.asking[session] == a.line {
		c.dropAsking(session, fx)
	}
}

// onChoose: a tap on a session menu's option.
func (c *AgentController) onChoose(token string, option int, now Millis, fx *[]Effect) {
	p, ok := c.confirms.Choose(token, option)
	if !ok {
		c.tapNotWaiting(token, now, fx)
		return
	}
	c.noteTap(token, now)
	c.answerSession(p, strconv.Itoa(option), now, fx)
}

// onPicks: the options picked of a session menu question that takes several (#364).
func (c *AgentController) onPicks(token string, options []int, now Millis, fx *[]Effect) {
	p, ok := c.confirms.Picks(token, options)
	if !ok {
		c.tapNotWaiting(token, now, fx)
		return
	}
	c.noteTap(token, now)
	sorted := slices.Clone(options)
	slices.Sort(sorted)
	picks := make([]string, len(sorted))
	for i, o := range sorted {
		picks[i] = strconv.Itoa(o)
	}
	c.answerSession(p, "picks:"+strings.Join(picks, ","), now, fx)
}

// onTypedAnswer: an answer typed in the bubble for a session menu that takes one (#268).
func (c *AgentController) onTypedAnswer(token, text string, now Millis, fx *[]Effect) {
	text = strings.TrimSpace(text)
	if text == "" || !c.confirms.TakesTyped(token) {
		return
	}
	p, ok := c.confirms.take(token)
	if !ok {
		c.tapNotWaiting(token, now, fx)
		return
	}
	c.noteTap(token, now)
	c.answerSession(p, "text:"+text, now, fx)
}

// onChatAbout: "Chat about this" on a session menu that takes one (#268).
func (c *AgentController) onChatAbout(token string, now Millis, fx *[]Effect) {
	if !c.confirms.TakesTyped(token) {
		c.tapNotWaiting(token, now, fx)
		return
	}
	p, _ := c.confirms.take(token)
	c.noteTap(token, now)
	c.answerSession(p, "chat", now, fx)
}

// answerMenu: what you said back to the session menu with token, asked last (#330): the
// option it names (MenuChoice), else your words typed in when the menu takes them. An option or
// words that push, merge, publish or deploy are still picked in the bubble, as is any answer
// to a menu that takes no words and names none of its options; the menu then still waits.
func (c *AgentController) answerMenu(p Pending, text string, now Millis, fx *[]Effect) {
	text = strings.TrimSpace(text)
	answer, said := "", text
	if p.Multi {
		*fx = append(*fx, Effect{Kind: EfAnswerResult, Err: "That one takes several picks: pick them in the bubble."})
		return
	}
	if n, ok := MenuChoice(p.Options, text); ok {
		answer, said = strconv.Itoa(n), p.Options[n-1]
	} else if p.Typed && text != "" {
		answer = "text:" + text
	}
	switch actions := GatedActions(said); {
	case answer == "":
		*fx = append(*fx, Effect{Kind: EfAnswerResult,
			Err: fmt.Sprintf("That isn't one of its options: say %s, or pick it in the bubble.", orList(p.Options))})
		return
	case len(actions) > 0:
		*fx = append(*fx, Effect{Kind: EfAnswerResult,
			Err: fmt.Sprintf("That one asks it to %s: pick it in the bubble.", spokenList(actions))})
		return
	}
	c.confirms.take(p.Token)
	c.asked = asker{}
	c.answerSession(p, answer, now, fx)
	*fx = append(*fx, Effect{Kind: EfAnswerResult, Name: p.Session})
}

// menuAsked: the session menu Pixie relayed last, still waiting (nil none): by its request id,
// which no other request gets.
func (c *AgentController) menuAsked() *Pending {
	at := slices.IndexFunc(c.confirms.Pending(), func(p Pending) bool { return p.ID == c.asked.menu && p.Menu })
	if at < 0 {
		return nil
	}
	p := c.confirms.Pending()[at]
	return &p
}

// menuFillers lead or end an answer without naming anything: "let's go with Postgres, please".
var menuFillers = map[string]bool{"yes": true, "yeah": true, "ok": true, "okay": true, "um": true, "uh": true, "so": true,
	"lets": true, "go": true, "with": true, "pick": true, "choose": true, "the": true, "option": true, "number": true,
	"please": true, "thanks": true, "i": true, "want": true, "id": true, "like": true}

// numberWords are read as their digits, in what's said and in the options alike: "two" is "2".
// Not "one", as often a pronoun ("any one is fine") as a number: number reads it where it is one.
var numberWords = map[string]string{"two": "2", "three": "3", "four": "4", "five": "5", "six": "6",
	"seven": "7", "eight": "8", "nine": "9", "ten": "10"}

// ordinals are an option's place: "the second one".
var ordinals = map[string]int{"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5}

// menuNegators make an answer say what not to pick: "not Postgres", "anything but SQLite",
// "other than Postgres".
var menuNegators = map[string]bool{"not": true, "no": true, "dont": true, "never": true, "without": true, "nor": true,
	"isnt": true, "doesnt": true, "wont": true, "cant": true, "shouldnt": true, "wouldnt": true, "didnt": true,
	"but": true, "avoid": true, "other": true, "than": true, "skip": true, "except": true, "instead": true,
	"rather": true, "besides": true, "exclude": true, "excluding": true, "apart": true}

// menuWords: text folded for matching: lower case, no apostrophes, only letters and digits.
func menuWords(text string) []string {
	text = strings.NewReplacer("'", "", "’", "").Replace(strings.ToLower(text))
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for k, w := range words {
		if d, ok := numberWords[w]; ok {
			words[k] = d
		}
	}
	return words
}

// isNumber: w is all digits.
func isNumber(w string) bool {
	return w != "" && strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

// number is w as a number said: its digits, or "1" for "one"; "" when it isn't one.
func number(w string) string {
	if w == "one" {
		return "1"
	}
	if isNumber(w) {
		return w
	}
	return ""
}

// MenuChoice is the option (from 1) an answer picks: one said as it reads ("SQLite"), by its
// place ("the second one", "option 3", "the last one"), or the only one named in it, with no
// "not" besides ("let's go with Postgres"). false when it names none, several or one it rules out.
func MenuChoice(options []string, answer string) (int, bool) {
	raw := menuWords(answer)
	// An option named word for word first: "Option 2" is the option of that name, wherever it is.
	for k, o := range options {
		if strings.Join(menuWords(o), " ") == strings.Join(raw, " ") {
			return k + 1, true
		}
	}
	// "option 2", "number one" and nothing else: a place, whatever numbers the options have.
	if place := trimLeadFillers(raw); len(place) == 2 && (place[0] == "option" || place[0] == "number" || place[0] == "choice") {
		if n, err := strconv.Atoi(number(place[1])); err == nil {
			return n, n >= 1 && n <= len(options)
		}
	}
	words := stripFillers(raw)
	// Word for word first, then with fillers left out on both sides: "yes please" is the
	// option "Yes please" before it is "Yes".
	for _, form := range []func([]string) []string{func(w []string) []string { return w }, stripFillers} {
		said := strings.Join(form(raw), " ")
		for k, o := range options {
			if strings.Join(form(menuWords(o)), " ") == said {
				return k + 1, true
			}
		}
	}
	if len(words) == 1 {
		w := words[0]
		if n := ordinals[w]; n >= 1 && n <= len(options) {
			return n, true
		}
		if w == "last" && len(options) > 0 {
			return len(options), true
		}
		// A number is a place ("option 2"), unless the options have numbers of their own:
		// then "two" names "2 workers", never the second option.
		numbered := slices.ContainsFunc(options, func(o string) bool { return slices.ContainsFunc(menuWords(o), isNumber) })
		if d := number(w); d != "" && !numbered {
			if n, err := strconv.Atoi(d); err == nil && n >= 1 && n <= len(options) {
				return n, true
			}
			return 0, false
		}
		if d := number(w); d != "" {
			found := 0
			for k, o := range options {
				if slices.Contains(menuWords(o), d) {
					if found != 0 {
						return 0, false
					}
					found = k + 1
				}
			}
			return found, found != 0
		}
	}
	found := 0
	for k, o := range options {
		if ow := strings.Join(menuWords(o), " "); ow != "" && strings.Contains(" "+strings.Join(raw, " ")+" ", " "+ow+" ") {
			if found != 0 {
				return 0, false
			}
			found = k + 1
		}
	}
	if found == 0 {
		return 0, false
	}
	own := menuWords(options[found-1])
	for _, w := range raw {
		if menuNegators[w] && !slices.Contains(own, w) {
			return 0, false
		}
	}
	return found, true
}

// trimLeadFillers drops the fillers at the start and "please" or "thanks" at the end, but
// never "option" or "number" before a place: "let's go with option 2, please" is "option 2".
func trimLeadFillers(words []string) []string {
	for len(words) > 0 && (words[len(words)-1] == "please" || words[len(words)-1] == "thanks") {
		words = words[:len(words)-1]
	}
	for len(words) > 0 && menuFillers[words[0]] && words[0] != "option" && words[0] != "number" {
		words = words[1:]
	}
	return words
}

// stripFillers drops the fillers at the end, then at the start, keeping one word at least:
// "yes please" is "yes", "option A" is "a", "the second one" is "second".
func stripFillers(words []string) []string {
	for n := len(words); n > 1; n = len(words) {
		last, prev := words[n-1], words[n-2]
		if !menuFillers[last] && !(last == "one" && (ordinals[prev] > 0 || prev == "last" || prev == "new" || prev == "other")) {
			break
		}
		words = words[:n-1]
	}
	for len(words) > 1 && menuFillers[words[0]] {
		words = words[1:]
	}
	return words
}

// RecentTapMs: a tap this soon after one settled its token is the same click, doubled.
const RecentTapMs Millis = 5000

func (c *AgentController) noteTap(token string, now Millis) {
	if c.tapped == nil {
		c.tapped = map[string]Millis{}
	}
	c.tapped[token] = now
}

// tapNotWaiting: a tap on token settled nothing. One no longer waiting (answered, gone, or from
// before a restart) is said and logged (#268); a waiting one tapped wrong (Allow on a menu, an
// option it hasn't) the bubble never offers, and a double click's second tap, stay quiet.
func (c *AgentController) tapNotWaiting(token string, now Millis, fx *[]Effect) {
	if c.confirms.IsPendingToken(token) {
		return
	}
	if at, ok := c.tapped[token]; ok && now < at+RecentTapMs {
		return // the second tap of a double click
	}
	*fx = append(*fx, logEffect(fmt.Sprintf("agent: a tap on %q: that question isn't waiting any more", token)))
	c.deliver(NewLine(LineInfo, "That question isn't waiting any more."), now, fx)
}

// answerSession types answer into p's session; what was answered is said once the session took
// it (InSessionAnswered, #268).
func (c *AgentController) answerSession(p Pending, answer string, now Millis, fx *[]Effect) {
	said := map[string]string{"yes": "allowed", "no": "denied"}[answer]
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(p.Options) {
		said = "picked " + p.Options[n-1]
	}
	if list, ok := strings.CutPrefix(answer, "picks:"); ok {
		var names []string
		for _, f := range strings.Split(list, ",") {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(p.Options) {
				names = append(names, p.Options[n-1])
			}
		}
		said = "picked " + andList(names)
	}
	if typed, ok := strings.CutPrefix(answer, "text:"); ok {
		said = fmt.Sprintf("answered %q", typed)
	} else if answer == "chat" {
		said = "you'll talk it over first"
	}
	*fx = append(*fx, Effect{Kind: EfAnswerSession, Name: p.Session, Text: p.Question, Answer: answer,
		Line: fmt.Sprintf("%s: %s", p.Label, said)})
}

// andList is "A", "A and B", "A, B and C".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// orList is "A", "A or B", "A, B or C".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
