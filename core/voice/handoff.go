package voice

// The hand-off safety net (#71): when Pixie answers a request with "I don't know" and calls no
// tool, the conversation hands the user's words to the delegate tool itself. These matchers
// decide when; they are English only and deliberately conservative: a missed hand-off leaves
// today's behaviour, a wrong one starts a job nobody asked for.

import (
	"encoding/json"
	"strings"
	"unicode"
)

// DelegateTool is the name of the agent's delegate tool, which the hand-off calls.
const DelegateTool = "delegate"

// normalize lowercases s, drops apostrophes (straight and curly, so "don't", "don’t" and
// "dont" match alike), turns other punctuation into spaces and collapses whitespace.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '\'' || r == '’' || r == '‘' || r == 'ʼ':
			// dropped
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// containsPhrase: phrase (normalized) occurs in text (normalized) on word boundaries.
func containsPhrase(text, phrase string) bool {
	return strings.Contains(" "+text+" ", " "+phrase+" ")
}

func hasAnyPhrase(text string, phrases []string) bool {
	for _, p := range phrases {
		if containsPhrase(text, p) {
			return true
		}
	}
	return false
}

func hasPrefixPhrase(text string, phrases []string) bool {
	for _, p := range phrases {
		if text == p || strings.HasPrefix(text, p+" ") {
			return true
		}
	}
	return false
}

// nonAnswerPhrases are Pixie admitting it can't answer (normalized: no apostrophes).
var nonAnswerPhrases = []string{
	"i dont know", "i do not know", "i dunno", "i have no idea", "ive no idea",
	"im not sure", "i am not sure", "im not certain", "i am not certain",
	"i cant check", "i cannot check", "i cant tell", "i cannot tell",
	"i cant look", "i cannot look", "i cant access", "i cannot access",
	"i cant see", "i cannot see", "i cant find", "i cannot find",
	"i cant browse", "i cannot browse", "i cant search", "i cannot search",
	"i cant get", "i cannot get", "i cant know", "i cannot know",
	"i dont have access", "i do not have access", "i dont have that", "i do not have that",
	"i dont have any information", "i do not have any information",
	"i dont have information", "i do not have information",
	"i dont have real time", "i dont have live", "i dont have current", "i dont have up to date",
	"i dont have a way", "i have no way", "no way for me to", "theres no way for me",
	"im unable to", "i am unable to", "im not able to", "i am not able to",
	"i wasnt able to", "i was not able to",
}

// notNonAnswer marks a reply that only sounds like one: it asks the user to clarify, offers a
// guess anyway, or is small talk ("I don't know about you, but…").
var notNonAnswer = []string{
	// clarifying
	"what you mean", "do you mean", "did you mean", "repeat that", "say that again",
	"didnt catch", "did not catch", "didnt quite catch", "missed that", "understand what",
	"which one", "could you clarify", "can you clarify",
	// a guess or an answer after all
	"maybe", "perhaps", "probably", "i think", "i guess", "id guess", "my guess", "id say",
	"how about", "i believe", "id go with", "id pick", "i suppose",
	// small talk
	"dont know about you", "do not know about you", "you tell me",
}

// nonAnswerMaxWords: a longer reply is doing more than not answering.
const nonAnswerMaxWords = 40

// IsNonAnswer reports whether Pixie's reply is an "I don't know": its first sentence says Pixie
// can't answer, the reply is short, and it neither asks the user to clarify nor guesses.
func IsNonAnswer(reply string) bool {
	all := normalize(reply)
	if all == "" || len(strings.Fields(all)) > nonAnswerMaxWords || hasAnyPhrase(all, notNonAnswer) {
		return false
	}
	return hasAnyPhrase(normalize(firstSentence(reply)), nonAnswerPhrases)
}

// firstSentence is reply up to its first '.', '!' or '?' (commas don't end it).
func firstSentence(reply string) string {
	if i := strings.IndexAny(reply, ".!?"); i >= 0 {
		return reply[:i]
	}
	return reply
}

// smallTalk are the user's words that ask for nothing: greetings, thanks, acknowledgements,
// goodbyes. Matched as the whole utterance or its start.
var smallTalk = []string{
	"hi", "hello", "hey", "yo", "good morning", "good afternoon", "good evening", "good night",
	"thanks", "thank you", "thx", "cheers", "ok", "okay", "cool", "nice", "great", "awesome",
	"got it", "sounds good", "all right", "alright", "sure", "yes", "yeah", "yep", "no", "nope",
	"never mind", "nevermind", "bye", "goodbye", "see you", "see ya", "later", "talk later",
	"talk to you later", "thats all", "thats it", "im done", "were done", "nothing", "stop",
	"wow", "haha", "lol", "hmm", "oh", "uh", "um",
}

// chitChat are questions about Pixie itself (its feelings, tastes, opinions): Pixie answers those,
// and an "I don't know" to one is a fine answer.
var chitChat = []string{
	"how are you", "how are things", "how is it going", "hows it going",
	"how do you feel", "how was your", "are you", "do you like", "do you love", "do you enjoy",
	"do you want", "would you rather", "whats your favorite", "whats your favourite",
	"what is your favorite", "what is your favourite", "what do you think", "what do you like",
	"what would you", "who are you", "whats your name", "what is your name", "tell me a joke",
	"tell me about yourself", "what should we", "what shall we", "should we", "can we just",
	"do you think", "if you could",
}

// questionStarts open a question; requestStarts open a request.
var questionStarts = []string{
	"what", "whats", "who", "whos", "where", "wheres", "when", "whens", "why", "how", "hows",
	"which", "whose", "is", "isnt", "are", "arent", "was", "were", "do", "does", "did", "dont",
	"doesnt", "can", "cant", "could", "would", "will", "wont", "should", "shall", "has", "have",
	"had", "may", "might", "any idea", "any news",
}

var requestStarts = []string{
	"please", "tell me", "find", "look up", "look into", "lookup", "check", "show me", "search",
	"give me", "get me", "i need", "i want", "id like", "i would like", "help me", "let me know",
	"remind me", "make", "write", "draft", "open", "send", "explain", "summarize", "summarise",
	"research", "figure out", "work out", "compare", "list", "read", "fix", "update", "create",
	"build", "add", "set up", "book", "order", "plan", "ask", "find out", "hand", "pass", "delegate",
}

// requestAnywhere are request markers anywhere in the words ("hey Pixie, can you …").
var requestAnywhere = []string{
	"can you", "could you", "would you", "will you", "can u", "do you know", "i wonder",
	"i was wondering", "any idea", "tell me", "let me know", "find out", "look up",
}

// requestMinWords: "why?" alone is too little to hand off.
const requestMinWords = 3

// leadIns are words before the request that don't change it: "hey Pixie, thanks, so what's …".
var leadIns = map[string]bool{
	"hey": true, "hi": true, "hello": true, "ok": true, "okay": true, "so": true, "bit": true, "pixie": true,
	"um": true, "uh": true, "and": true, "also": true, "thanks": true, "well": true, "oh": true,
}

// IsRequest reports whether the user's words ask a question or for something to be done, not
// small talk, an acknowledgement, a goodbye or a question about Pixie itself.
func IsRequest(said string) bool {
	words := strings.Fields(normalize(said))
	for len(words) > 0 && leadIns[words[0]] {
		words = words[1:]
	}
	if len(words) < requestMinWords {
		return false
	}
	text := strings.Join(words, " ")
	if text == "whats up" || hasPrefixPhrase(text, chitChat) {
		return false
	}
	if hasPrefixPhrase(text, smallTalk) && !hasAnyPhrase(text, requestAnywhere) {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(said), "?") ||
		hasPrefixPhrase(text, questionStarts) || hasPrefixPhrase(text, requestStarts) ||
		hasAnyPhrase(text, requestAnywhere)
}

// ShouldHandOff: the user asked for something and Pixie's whole reply was a non-answer.
func ShouldHandOff(userSaid, botSaid string) bool {
	return IsRequest(userSaid) && IsNonAnswer(botSaid)
}

// Notice lead-ins for the hand-off; the delegation persona tells Pixie what to say for each.
const (
	HandedOffPrefix   = "Coordinator: handed off: "
	CantHandOffPrefix = "Coordinator: can't hand off: "
)

// delegationOff is how the delegate tool reports that delegation is off:
// "Delegation is off (<the agent's status line>)." (agent.AgentHandle.Delegate).
const delegationOff = "Delegation is off ("

// HandOffNotice is what Pixie is told after the hand-off's delegate call returned output: the
// task was handed off, or delegation is off and why (the agent's status line, e.g. "herdr not
// found"). Any other failure says nothing (Pixie's "I don't know" stands). why is for the log;
// it never includes the user's words.
func HandOffNotice(task string, output json.RawMessage) (n Notice, why string) {
	var out struct {
		JobID string `json:"job_id"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(output, &out)
	switch {
	case out.JobID != "":
		return Notice{Text: HandedOffPrefix + task}, "handed it to the coordinator as " + out.JobID
	case strings.HasPrefix(out.Error, delegationOff):
		reason := strings.TrimSuffix(strings.TrimPrefix(out.Error, delegationOff), ".")
		reason = strings.TrimSuffix(reason, ")")
		reason = strings.TrimPrefix(strings.TrimSpace(reason), "Coordinator: ")
		text := "delegation is off"
		if reason != "" {
			text += " (" + reason + ")"
		}
		return Notice{Text: CantHandOffPrefix + text + "."}, text
	case out.Error != "":
		return Notice{}, "the hand-off failed: " + out.Error
	}
	return Notice{}, "the hand-off returned nothing usable"
}
