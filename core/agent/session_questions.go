package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Prompts your pix sessions wait on (#105): shown in Pixie too, with a confirm word (or Allow
// and Deny, or a menu's options) in the bubble; your answer is typed into the session by pix,
// which first checks the prompt is still on screen. Only you answer: a click, or a confirm word
// for a permission you can hear in full; never the coordinator.

// sessionRequestBase starts the request ids of session prompts, apart from hook request ids.
const sessionRequestBase RequestID = 1 << 62

// onSessionQuestion asks you session's prompt q; a prompt it asked before is replaced.
func (c *AgentController) onSessionQuestion(session string, q SessionQuestion, now Millis, fx *[]Effect) {
	if c.shutDown {
		return
	}
	c.confirms.TakeSession(session, "")
	tokens := c.pendingTokens()
	label := maskPendingTokens(q.Label, tokens)
	line := Line{Kind: LineQuestion, App: q.App, Session: session, Title: label}
	pending := Pending{Session: session, Question: q.ID, Label: label}
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
			return fmt.Sprintf("%s is asking: %s Pick %s in the bubble.", label, mask(q.Text), orList(q.Options)),
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
		return
	}
	c.sessionSeq++
	pending.ID = sessionRequestBase + RequestID(c.sessionSeq)
	pending.Options = q.Options
	if q.Kind == "menu" {
		pending.Menu = true
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
	before := c.outbox.Len()
	c.deliverOpening(line, now, fx)
	// Said in a conversation, it never reached the outbox: keep it there as heard, so the bubble
	// lists it with its buttons while it waits (as permissions do, #97).
	if !c.outbox.HasTokenSince(before, token) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: c.outbox.PushHeard(line, now)})
	}
}

// onChoose: a tap on a session menu's option.
func (c *AgentController) onChoose(token string, option int, now Millis, fx *[]Effect) {
	p, ok := c.confirms.Choose(token, option)
	if !ok {
		c.tapNotWaiting(token, now, fx)
		return
	}
	c.answerSession(p, strconv.Itoa(option), now, fx)
}

// tapNotWaiting: a tap on token settled nothing. One no longer waiting (answered, gone, or from
// before a restart) is said and logged (#268); a waiting one tapped wrong (Allow on a menu, an
// option it hasn't) the bubble never offers, so it stays quiet.
func (c *AgentController) tapNotWaiting(token string, now Millis, fx *[]Effect) {
	if c.confirms.IsPendingToken(token) {
		return
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
	*fx = append(*fx, Effect{Kind: EfAnswerSession, Name: p.Session, Text: p.Question, Answer: answer,
		Line: fmt.Sprintf("%s: %s", p.Label, said)})
}

// orList is "A", "A or B", "A, B or C".
func orList(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
