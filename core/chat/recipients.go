package chat

import (
	"strings"
	"unicode"
)

// Recipient is someone a message typed in the chat window can go to on its own (#302): a
// session of yours that is running, or one of Pixie's workers. Pixie, the default, isn't one:
// a message with no recipient goes to her. Name is the address and the @name that mentions it.
type Recipient struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Kind   string `json:"kind"` // session | worker
	Yours  bool   `json:"yours,omitempty"`
	Status string `json:"status,omitempty"`
	Color  int    `json:"color"`
}

// RosterRow is a row of the roster beside Pixie as the core sends it (agent.RosterRow).
type RosterRow struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Kind      string `json:"kind"`
	Reachable bool   `json:"reachable"`
}

// Recipients are the roster's rows a message can go to: your sessions that pix still reaches,
// then Pixie's workers. The coordinator and panes no job owns can't be told anything.
func Recipients(agents, yours []RosterRow) []Recipient {
	out := []Recipient{}
	for _, r := range yours {
		if r.Kind == "session" && r.Reachable && r.Name != "" {
			out = append(out, Recipient{Name: r.Name, Label: labelOr(r), Kind: "session", Yours: true, Status: r.Status, Color: ColorFor(r.Name)})
		}
	}
	for _, r := range agents {
		if r.Kind == "worker" && r.Name != "" {
			out = append(out, Recipient{Name: r.Name, Label: labelOr(r), Kind: "worker", Status: r.Status, Color: ColorFor(r.Name)})
		}
	}
	return out
}

func labelOr(r RosterRow) string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

// isHandle: a rune an @name can hold (addresses are letters, digits, -, _ and .).
func isHandle(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
}

// Mention is a recipient the text names, and how: the word after the @, as typed.
type Mention struct {
	Recipient
	Said string
}

// Mentions are the recipients the text names with @name, in order, each once. The name is the
// recipient's address or its label (a label of one word: "@beta"); an address is matched
// first, so a label never shadows another recipient's address, and with a target (its
// address, "" for none) a label it shares with others names the target. An @ counts only at
// the start or after a space, and the name must be the whole word, matched without regard to
// case: "a@b", an email address or "@docs2" for docs mention no one. A full stop after it ends
// the sentence, not the name. The app's box runs the same (ChatRecipients.mentions).
func Mentions(text string, rs []Recipient, target string) []Mention {
	var out []Mention
	seen := map[string]bool{}
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' || (i > 0 && !unicode.IsSpace(runes[i-1])) {
			continue
		}
		j := i + 1
		for j < len(runes) && isHandle(runes[j]) {
			j++
		}
		name := strings.TrimRight(string(runes[i+1:j]), ".")
		i = j - 1
		if name == "" {
			continue
		}
		if r := named(name, rs, target); r != nil && !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, Mention{*r, name})
		}
	}
	return out
}

// named is the recipient an @name is: the one with that address, else the target if it is its
// label, else the first with that label; nil for no one.
func named(name string, rs []Recipient, target string) *Recipient {
	for i := range rs {
		if strings.EqualFold(rs[i].Name, name) {
			return &rs[i]
		}
	}
	for i := range rs {
		if target != "" && rs[i].Name == target && strings.EqualFold(rs[i].Label, name) {
			return &rs[i]
		}
	}
	for i := range rs {
		if strings.EqualFold(rs[i].Label, name) {
			return &rs[i]
		}
	}
	return nil
}

// Refusal is why a message can't go as it is: what to tell you, as it is shown.
type Refusal struct{ Text string }

func (r *Refusal) Error() string { return r.Text }

// CheckOneRecipient is the rule that a message goes to one recipient (#302). target is the one
// chosen (nil: Pixie); label names it when it isn't running ("" for its address). It refuses a
// target that isn't a recipient now, a mention of anyone else beside a target, and, with no
// target, any mention of a recipient: one is picked from the list, never guessed from the text.
// Mentions of the target itself, and @words that name no recipient, are fine.
func CheckOneRecipient(text string, target *string, label string, rs []Recipient) error {
	to := ""
	if target != nil {
		to = *target
	}
	mentioned := Mentions(text, rs, to)
	if target == nil {
		switch {
		case len(mentioned) >= 2:
			return &Refusal{"Send to one at a time: pick @" + mentioned[0].Said + " or @" + mentioned[1].Said + "."}
		case len(mentioned) == 1:
			return &Refusal{"Pick @" + mentioned[0].Said + " from the list to send it to them."}
		}
		return nil
	}
	var picked *Recipient
	for i := range rs {
		if *target != "" && rs[i].Name == *target {
			picked = &rs[i]
			break
		}
	}
	if picked == nil {
		switch {
		case label != "":
		case *target != "":
			label = *target
		default:
			label = "That session"
		}
		return &Refusal{label + " isn't running anymore."}
	}
	for _, m := range mentioned {
		if m.Name != picked.Name {
			return &Refusal{"This goes to " + picked.Label + " only. Remove @" + m.Said + ", or pick them instead."}
		}
	}
	return nil
}
